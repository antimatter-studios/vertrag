package main

import (
	"fmt"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/link"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
)

// Link resolution runs on every `vertrag run`, and costs no request.
//
// The examples phase has already sent every documented request and kept what
// came back, so asking whether a link's expressions resolve against those
// responses is a pass over data the run is holding. That is why this is not a
// phase and not a flag: the arguments for making a check opt-in are cost and
// noise, and there is no cost here. `checks.link-resolution: false` turns it
// off for a description whose links are known to be aspirational.
//
// It reports a description contradicting itself, which is why its findings are
// exit 2 rather than exit 1: every documented transaction may have passed. See
// errFindings.

// runLinkCheck resolves the description's links against the run's own
// responses and reports what did not hold.
//
// The results it returns go into the same report as everything else, so a
// pipeline archiving JUnit gets them; the terminal print is for the run whose
// report is a file, where otherwise nothing would say a link had been checked
// at all.
func runLinkCheck(
	transactions []compile.Transaction,
	defined []compile.Transaction,
	results []runner.Result,
	sequencer *link.Sequencer,
	color bool,
) ([]runner.Result, bool) {
	if !declaresLinks(transactions) {
		// Silence, and it means something: most descriptions declare no links,
		// and a line per run saying that nothing was claimed would train the
		// reader to skip the place where the findings appear.
		return nil, false
	}

	// The whole description is handed over alongside the run's own list. A
	// link naming an operation `--only` or `--tag` left out is not a link
	// naming an operation the document does not define, and a check that
	// confused the two would greet every narrowed run with findings about a
	// description that is entirely consistent.
	report := link.Check(transactions, defined, observedFrom(transactions, results, sequencer))

	var out []runner.Result
	for _, finding := range report.Findings {
		printLinkFinding(finding, color)
		result := runner.Result{
			Name: finding.Source + " · " + finding.Link, Status: runner.StatusFail,
			Errors: []string{finding.Message},
		}
		// The exchange the claim was judged against, so the reader can see the
		// body that did not carry what the link said it would. Rebuilding it
		// would be a second construction of something the run already has.
		if source, found := resultFor(transactions, results, finding.Source); found {
			result.Request, result.Actual = source.Request, source.Actual
		}
		out = append(out, result)
	}
	for _, unchecked := range report.Unchecked {
		out = append(out, runner.Result{
			Name: unchecked.Source + " · " + unchecked.Link, Status: runner.StatusSkip,
			Errors: []string{"the response it is declared on never arrived: " + unchecked.Reason},
		})
	}

	fmt.Printf("\n%d link(s) checked, %d finding(s)", report.Checked, len(report.Findings))
	if len(report.Unchecked) > 0 {
		// Named rather than folded into the count, because "checked and clean"
		// and "not checked" are opposite facts and a single number reads as
		// the first.
		fmt.Printf(", %d not checked", len(report.Unchecked))
	}
	fmt.Println()

	return out, len(report.Findings) > 0
}

// observedFrom says, per transaction, what a link declared on its response may
// be resolved against.
//
// A transaction that did not pass supplies no exchange, and the reason is
// carried rather than dropped: a link below a create that answered 400 has not
// been shown to be wrong, and the reader needs to be sent to the create.
func observedFrom(
	transactions []compile.Transaction,
	results []runner.Result,
	sequencer *link.Sequencer,
) map[int]link.Observed {
	observed := make(map[int]link.Observed, len(transactions))
	for i := range transactions {
		if i >= len(results) {
			break
		}
		result := results[i]

		if reason := whyUnusable(result); reason != "" {
			observed[i] = link.Observed{Missing: reason}
			continue
		}

		// A sequenced run's exchange comes from the sequencer, which is the
		// only place the values a link put INTO a request survive. Rebuilding
		// it from the compiled transaction would resolve `$request.path.id`
		// to the description's example, and in a sequenced run that is
		// precisely the value that was replaced.
		if sequencer != nil {
			if exchange, recorded := sequencer.Exchange(i); recorded {
				observed[i] = link.Observed{Exchange: exchange}
				continue
			}
		}
		observed[i] = link.Observed{Exchange: link.ExchangeFrom(transactions[i], result)}
	}
	return observed
}

// whyUnusable explains why a result is no basis for resolving a link, or ""
// when it is one.
func whyUnusable(result runner.Result) string {
	switch result.Status {
	case runner.StatusPass:
		return ""
	case runner.StatusSkip:
		return "it was not run"
	case runner.StatusError:
		return "it could not be sent"
	default:
		if result.Actual.StatusCode != "" && result.Expected.StatusCode != "" {
			return fmt.Sprintf("it answered %s where the description promises %s",
				result.Actual.StatusCode, result.Expected.StatusCode)
		}
		return "it did not pass"
	}
}

func declaresLinks(transactions []compile.Transaction) bool {
	for _, transaction := range transactions {
		if len(transaction.Links) > 0 {
			return true
		}
	}
	return false
}

// resultFor finds the completed transaction a finding is about. Matching by
// name rather than carrying an index: the finding is what a report holds, and
// a report addresses transactions by name everywhere else.
func resultFor(transactions []compile.Transaction, results []runner.Result, name string) (runner.Result, bool) {
	for i := range transactions {
		if i < len(results) && transactions[i].Name == name {
			return results[i], true
		}
	}
	return runner.Result{}, false
}

func printLinkFinding(finding link.Finding, color bool) {
	paint := func(code, text string) string { return reporter.Paint(color, code, text) }

	fmt.Printf("\n%s %s\n", paint(reporter.Red, "finding:"), "link resolution")
	fmt.Printf("  %s\n", finding.Message)
	fmt.Printf("  %s %s\n", paint(reporter.Dim, "source:"), finding.Source)
	if finding.Target != "" {
		fmt.Printf("  %s %s\n", paint(reporter.Dim, "target:"), finding.Target)
	}
}
