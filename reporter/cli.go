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
		fmt.Fprintf(r.Out, "%s%s\n", indent, line)
	}
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
