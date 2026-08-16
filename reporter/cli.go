// Package reporter renders a run's results.
package reporter

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/antimatter-studios/vertrag/runner"
)

// CLI writes Dredd's default output: one line per transaction, then the
// failures in full, then a summary.
type CLI struct {
	Out   io.Writer
	Color bool
	// Details prints the request and response of passing transactions too,
	// which is what makes a run debuggable when the server is doing something
	// unexpected but still technically conforming.
	Details bool
}

// ANSI codes, used only when the output is a terminal the user asked to colour.
const (
	reset  = "\033[0m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
	dim    = "\033[2m"
)

// paint colours text when the destination is a terminal the user asked to
// colour. Every reporter that colours anything goes through here, so a failure
// is the same red whichever format a reader is looking at.
func paint(enabled bool, code, text string) string {
	if !enabled {
		return text
	}
	return code + text + reset
}

func (r CLI) paint(code, text string) string {
	return paint(r.Color, code, text)
}

// Report writes the results and returns true when the run passed.
//
// An error counts as a failure: the API was never successfully asked, so the
// run cannot claim it conforms.
func (r CLI) Report(results []runner.Result) bool {
	passed := true

	for _, result := range results {
		r.line(result)
		if result.Status == runner.StatusFail || result.Status == runner.StatusError {
			passed = false
		}
	}

	r.failures(results)
	r.summary(results)
	return passed
}

func (r CLI) line(result runner.Result) {
	var label string
	switch result.Status {
	case runner.StatusPass:
		label = r.paint(green, "pass")
	case runner.StatusFail:
		label = r.paint(red, "fail")
	case runner.StatusSkip:
		label = r.paint(yellow, "skip")
	case runner.StatusError:
		label = r.paint(red, "error")
	}

	fmt.Fprintf(r.Out, "%s: %s %s (%s)\n",
		label, result.Request.Method, result.Name, result.Duration.Round(1e6))

	// A skip says why on the same line. It is the one outcome a reader cannot
	// interpret without one — a transaction excluded on purpose and a step
	// abandoned because what it depended on failed look identical otherwise,
	// and only one of them is a problem.
	if result.Status == runner.StatusSkip && len(result.Errors) > 0 {
		fmt.Fprintf(r.Out, "      %s\n", r.paint(dim, strings.Join(result.Errors, "; ")))
	}

	if r.Details && result.Status == runner.StatusPass {
		r.exchange(result)
	}
}

// failures prints each failure in full, after the run, so the per-transaction
// lines stay readable while a run is in progress.
func (r CLI) failures(results []runner.Result) {
	for _, result := range results {
		if result.Status != runner.StatusFail && result.Status != runner.StatusError {
			continue
		}

		fmt.Fprintf(r.Out, "\n%s: %s\n", r.paint(red, strings.ToUpper(string(result.Status))), result.Name)
		for _, message := range result.Errors {
			for _, line := range strings.Split(message, "\n") {
				fmt.Fprintf(r.Out, "  %s\n", line)
			}
		}

		// Findings Dredd would not have raised are labelled, so an upgrade is
		// not mistaken for a regression.
		for _, message := range result.Beyond {
			fmt.Fprintf(r.Out, "  %s %s\n", r.paint(cyan, "[additional check]"), message)
		}
		r.exchange(result)
	}
}

// exchange prints what was sent and what came back.
func (r CLI) exchange(result runner.Result) {
	fmt.Fprintf(r.Out, "  %s %s %s\n",
		r.paint(dim, "request:"), result.Request.Method, result.Request.URI)
	r.headers("    ", result.Request.Headers)
	r.body("    ", result.Request.Body)

	if result.Actual.StatusCode != "" {
		fmt.Fprintf(r.Out, "  %s %s\n", r.paint(dim, "response:"), result.Actual.StatusCode)
		r.headers("    ", result.Actual.Headers)
		r.body("    ", result.Actual.Body)
	}
}

func (r CLI) headers(indent string, headers map[string]string) {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(r.Out, "%s%s\n", indent, r.paint(dim, name+": "+headers[name]))
	}
}

// body prints a payload, shortened when it is long enough to bury everything
// else in the report.
func (r CLI) body(indent, body string) {
	if body == "" {
		return
	}
	const limit = 2000
	if len(body) > limit {
		body = body[:limit] + fmt.Sprintf("… (%d bytes truncated)", len(body)-limit)
	}
	for _, line := range strings.Split(body, "\n") {
		// A carriage return ending a line is the other half of a CRLF, which is
		// how a CSV body is meant to be written; dropping it here is what keeps
		// such a body readable while still treating a return in the middle of a
		// line as the cursor move it is.
		fmt.Fprintf(r.Out, "%s%s\n", indent, printable(strings.TrimSuffix(line, "\r")))
	}
}

// printable replaces the bytes a response body must not be allowed to put on a
// terminal as they stand.
//
// A body is whatever the server under test chose to send, and the report is the
// only account the reader gets of it. Written out verbatim, an escape sequence
// in a body can move the cursor, erase or recolour what is already on screen,
// and so forge a passing line for a transaction that failed; the NUL bytes and
// invalid UTF-8 of a binary body garble the lines around them. Neither is
// exotic — an `application/octet-stream` download reaches this function on its
// first mismatch.
//
// U+FFFD is the substitute because the XML encoder already puts exactly that in
// the JUnit report for the same bytes, so the two reports of one run agree about
// what came back.
func printable(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	for _, r := range text {
		// Invalid UTF-8 decodes to U+FFFD here already, which is the substitution
		// this function would make anyway.
		switch {
		case r == '\t', r >= 0x20 && r != 0x7f && !(r >= 0x80 && r <= 0x9f):
			out.WriteRune(r)
		default:
			out.WriteRune('�')
		}
	}
	return out.String()
}

func (r CLI) summary(results []runner.Result) {
	counts := map[runner.Status]int{}
	for _, result := range results {
		counts[result.Status]++
	}

	parts := []string{
		fmt.Sprintf("%d passing", counts[runner.StatusPass]),
		fmt.Sprintf("%d failing", counts[runner.StatusFail]),
		fmt.Sprintf("%d errors", counts[runner.StatusError]),
		fmt.Sprintf("%d skipped", counts[runner.StatusSkip]),
	}

	colour := green
	if counts[runner.StatusFail] > 0 || counts[runner.StatusError] > 0 {
		colour = red
	}

	fmt.Fprintf(r.Out, "\n%s\n", r.paint(colour,
		fmt.Sprintf("%d total, %s", len(results), strings.Join(parts, ", "))))
}

// Annotations prints the diagnostics the parser raised about the description
// itself, before any transaction runs.
//
// They come first because a document vertrag could not fully read explains a
// short or empty run better than the run does.
func (r CLI) Annotations(annotations []Annotation) {
	for _, annotation := range annotations {
		colour, label := yellow, "warn"
		if annotation.Type == "error" {
			colour, label = red, "error"
		}

		location := ""
		if annotation.Line > 0 {
			location = fmt.Sprintf(" (line %d, column %d)", annotation.Line, annotation.Column)
		}

		fmt.Fprintf(r.Out, "%s: %s%s\n", r.paint(colour, label),
			strings.ReplaceAll(annotation.Message, "\n", " "),
			r.paint(cyan, location))
	}
}

// Annotation is a diagnostic about the description document.
type Annotation struct {
	Type    string
	Message string
	Line    int
	Column  int
}
