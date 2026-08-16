package reporter

import (
	"fmt"
	"io"
	"strings"

	"github.com/antimatter-studios/vertrag/runner"
)

// Dot writes one character per transaction and says nothing else until the run
// is over.
//
// This is Dredd's dot reporter, and mocha's before it. On a suite of hundreds
// of transactions the CLI reporter's per-transaction lines push the failures
// off the top of the terminal before anyone can read them; compressing progress
// to a single character each keeps the whole run and its failures on one
// screen.
type Dot struct {
	Out   io.Writer
	Color bool
}

// The characters are Dredd's, so someone who knows one tool reads the other's
// output without a legend: a dot passed, a dash was skipped, F failed the
// contract, and E never reached the server at all.
const (
	dotPass  = "."
	dotSkip  = "-"
	dotFail  = "F"
	dotError = "E"
)

// dotsPerLine wraps the progress stream. Dredd emits it unbroken and lets the
// terminal soft-wrap, which leaves a report redirected to a file as one line
// thousands of characters wide.
const dotsPerLine = 72

// Report writes the progress, then the failures in full, then the summary. It
// returns true when the run passed.
func (r Dot) Report(results []runner.Result) bool {
	for i, result := range results {
		if i > 0 && i%dotsPerLine == 0 {
			fmt.Fprintln(r.Out)
		}
		fmt.Fprint(r.Out, r.mark(result.Status))
	}
	if len(results) > 0 {
		fmt.Fprintln(r.Out)
	}

	r.failures(results)

	counted := tally(results)
	colour := green
	if !counted.Passed() {
		colour = red
	}
	fmt.Fprintf(r.Out, "\n%s\n", r.paint(colour, "complete: "+counted.Line()))

	return counted.Passed()
}

func (r Dot) mark(status runner.Status) string {
	switch status {
	case runner.StatusPass:
		return r.paint(green, dotPass)
	case runner.StatusSkip:
		return r.paint(yellow, dotSkip)
	case runner.StatusError:
		return r.paint(red, dotError)
	default:
		return r.paint(red, dotFail)
	}
}

// failures prints what each failing transaction was and what it did, since the
// progress line above says only that something failed. Without this the reader
// has to run the suite again under a different reporter to learn anything.
func (r Dot) failures(results []runner.Result) {
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
		for _, message := range result.Beyond {
			fmt.Fprintf(r.Out, "  %s %s\n", r.paint(cyan, "[additional check]"), message)
		}

		if request := requestText(result.Request); request != "" {
			r.block("request", request)
		}
		if response := responseText(result.Actual); response != "" {
			r.block("response", response)
		}
	}
}

func (r Dot) block(label, text string) {
	fmt.Fprintf(r.Out, "  %s\n", r.paint(dim, label+":"))
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(r.Out, "    %s\n", line)
	}
}

func (r Dot) paint(code, text string) string {
	return paint(r.Color, code, text)
}
