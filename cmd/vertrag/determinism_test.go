package main

import "regexp"

// A run's output carries three things that vary between two runs of the same
// suite without the suite having behaved differently, and a test comparing two
// runs byte for byte has to take them out or it asserts something stronger than
// the property it is named for.
//
// This exists as one helper rather than a regexp in each test because the same
// mistake was made three times: durations were normalised in one place and not
// another, and then CI failed on a `date:` header ticking over a second
// boundary — a wall clock, in output being compared for determinism. The next
// clock-like thing should be added here and be fixed everywhere at once.
var (
	// Elapsed times, printed beside every transaction.
	varyingTimings = regexp.MustCompile(`\(\d+(?:\.\d+)?(?:ns|µs|ms|s)\)`)
	// The server's Date response header, when headers are shown.
	varyingDates = regexp.MustCompile(`(?i)date: [A-Za-z]{3}, [^\n]*GMT`)
	// The signature line, which carries a temporary path and a port.
	varyingSignature = regexp.MustCompile(`vertrag [^\n]*\n`)
)

// steady removes what a clock and a temporary directory contribute, and
// nothing else. Everything a run decides — the probes, their order, the
// findings, the counts — still has to match.
func steady(output string) string {
	output = varyingTimings.ReplaceAllString(output, "(elapsed)")
	output = varyingDates.ReplaceAllString(output, "date: (when)")
	return varyingSignature.ReplaceAllString(output, "")
}
