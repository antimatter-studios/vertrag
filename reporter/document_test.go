package reporter

import (
	"strings"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// TestEveryFormatAgreesOnTheCounts pins that the reporters share one tally.
//
// A pipeline commonly writes a terminal log and a file from the same run and
// publishes both. A reader who finds them contradicting each other stops
// trusting the tool, and cannot tell which of the two was right.
func TestEveryFormatAgreesOnTheCounts(t *testing.T) {
	results := []runner.Result{
		{Name: "a", Status: runner.StatusPass, Duration: 2 * time.Millisecond},
		{Name: "b", Status: runner.StatusFail, Errors: []string{"wrong"}},
		{Name: "c", Status: runner.StatusError, Errors: []string{"unreachable"}},
		{Name: "d", Status: runner.StatusSkip},
	}

	counted := tally(results)
	if counted.Total != 4 || counted.Passing != 1 || counted.Failing != 1 ||
		counted.Errors != 1 || counted.Skipped != 1 {
		t.Fatalf("counts = %+v", counted)
	}
	if counted.Passed() {
		t.Error("a run with a failure and an unreachable server has not passed")
	}

	line := counted.Line()
	dot, _ := dotReport(t, Dot{}, results)
	markdown, _ := markdownReport(t, Markdown{}, results)
	html, _ := htmlReport(t, HTML{}, results)
	for name, report := range map[string]string{"dot": dot, "markdown": markdown, "html": html} {
		if !strings.Contains(report, line) {
			t.Errorf("the %s report does not carry the shared summary %q", name, line)
		}
	}
}

// TestHeadersAreOrderedSoReportsCanBeDiffed pins the sort. Go iterates maps in a
// random order, so without it two reports of the same unchanged failure differ
// every run, and a diff between yesterday's report and today's says nothing.
func TestHeadersAreOrderedSoReportsCanBeDiffed(t *testing.T) {
	request := runner.Request{
		Method: "GET", URI: "/things",
		Headers: map[string]string{"Zulu": "1", "Alpha": "2", "Mike": "3"},
	}

	first := requestText(request)
	if !strings.Contains(first, "Alpha: 2\nMike: 3\nZulu: 1") {
		t.Errorf("headers should be in name order:\n%s", first)
	}
	for range 20 {
		if again := requestText(request); again != first {
			t.Fatalf("the same request rendered two ways:\n%s\n---\n%s", first, again)
		}
	}
}

// TestAnUnreachableServerHasNoResponseBlock pins the difference between an empty
// response and no response at all. Printing an empty block for a request that
// never arrived would report the server as answering with nothing, which is a
// different and far more alarming fault than being unreachable.
func TestAnUnreachableServerHasNoResponseBlock(t *testing.T) {
	if text := responseText(validate.Message{}); text != "" {
		t.Errorf("a response that never came should render as nothing, got %q", text)
	}
	if text := responseText(validate.Message{StatusCode: "204"}); text != "204" {
		t.Errorf("a response with no body is still a response, got %q", text)
	}
}

// TestDocumentsTruncateEnormousPayloads pins that one huge body cannot bury the
// rest of a report — the failure a reader is looking for is usually not the one
// that returned a megabyte.
func TestDocumentsTruncateEnormousPayloads(t *testing.T) {
	doc := newDocument("t", []runner.Result{{
		Name:    "a",
		Status:  runner.StatusFail,
		Request: runner.Request{Method: "GET", URI: "/big"},
		Actual:  validate.Message{StatusCode: "200", Body: strings.Repeat("x", 5000)},
		Errors:  []string{"body: wrong"},
	}}, false)

	if !strings.Contains(doc.Cases[0].Response, "bytes truncated") {
		t.Error("a long body should be truncated")
	}
	if len(doc.Cases[0].Response) > 3000 {
		t.Errorf("the truncated response is still %d bytes", len(doc.Cases[0].Response))
	}
}

// TestASkippedTransactionNeedsNoSection pins that a skip stays a single row.
// Hooks skip whole groups of transactions routinely, and a section apiece would
// pad the report with entries that say nothing happened.
func TestASkippedTransactionNeedsNoSection(t *testing.T) {
	doc := newDocument("t", []runner.Result{{Name: "a", Status: runner.StatusSkip}}, false)
	if doc.Cases[0].Detailed() {
		t.Error("a skipped transaction has nothing to detail")
	}
}
