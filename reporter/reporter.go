package reporter

import "github.com/antimatter-studios/vertrag/runner"

// Reporter renders a run's results and says whether the run passed.
//
// It is the contract every format in this package satisfies — CLI, Dot,
// Markdown, HTML, JUnit — and it lives here, beside them, so the package
// states what a reporter is rather than leaving each caller to declare it.
type Reporter interface {
	Report(results []runner.Result) bool
}

// Multi runs several reporters over the same results, which is how a
// pipeline gets a readable terminal log and a machine-readable file from one
// run. It passes only if every reporter says the run passed.
type Multi []Reporter

// Report renders through every reporter in turn.
func (m Multi) Report(results []runner.Result) bool {
	passed := true
	for _, r := range m {
		if !r.Report(results) {
			passed = false
		}
	}
	return passed
}

// Every format satisfies the contract, checked at compile time so a new
// reporter that forgets a method fails here, beside the interface, and not in
// whichever caller happens to build it first.
var (
	_ Reporter = CLI{}
	_ Reporter = Dot{}
	_ Reporter = Markdown{}
	_ Reporter = HTML{}
	_ Reporter = JUnit{}
	_ Reporter = Multi{}
)
