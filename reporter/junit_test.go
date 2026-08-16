package reporter

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

func junitReport(t *testing.T, results []runner.Result) (string, bool) {
	t.Helper()
	var out bytes.Buffer
	passed := JUnit{Out: &out}.Report(results)
	return out.String(), passed
}

// TestJUnitIsWellFormed pins that the output actually parses. A report a CI
// system cannot read is worse than no report: the build goes green because
// nothing was understood.
func TestJUnitIsWellFormed(t *testing.T) {
	report, _ := junitReport(t, []runner.Result{
		{Name: "a", Status: runner.StatusPass, Request: runner.Request{Method: "GET"}},
		{Name: "b", Status: runner.StatusFail, Request: runner.Request{Method: "POST"},
			Errors: []string{"statusCode: wrong"}},
	})

	var parsed junitSuite
	if err := xml.Unmarshal([]byte(strings.TrimPrefix(report, xml.Header)), &parsed); err != nil {
		t.Fatalf("output does not parse as XML: %v\n%s", err, report)
	}
	if !strings.HasPrefix(report, "<?xml") {
		t.Error("a JUnit document should carry an XML declaration")
	}
}

func TestJUnitCounts(t *testing.T) {
	report, passed := junitReport(t, []runner.Result{
		{Name: "a", Status: runner.StatusPass, Duration: time.Millisecond},
		{Name: "b", Status: runner.StatusFail, Errors: []string{"wrong"}},
		{Name: "c", Status: runner.StatusError, Errors: []string{"unreachable"}},
		{Name: "d", Status: runner.StatusSkip},
	})

	if passed {
		t.Error("a run with failures should not report as passing")
	}

	var parsed junitSuite
	xml.Unmarshal([]byte(strings.TrimPrefix(report, xml.Header)), &parsed)

	if parsed.Tests != 4 || parsed.Failures != 1 || parsed.Errors != 1 || parsed.Skipped != 4-1-1-1 {
		t.Errorf("counts = tests %d, failures %d, errors %d, skipped %d",
			parsed.Tests, parsed.Failures, parsed.Errors, parsed.Skipped)
	}

	// A failure and an unreachable server are different elements, because they
	// mean different things: one is the API disagreeing, the other is never
	// having reached it.
	if !strings.Contains(report, "<failure") || !strings.Contains(report, "<error") {
		t.Errorf("a failure and an error should use their own elements:\n%s", report)
	}
	if !strings.Contains(report, "<skipped") {
		t.Errorf("a skip should be marked as one:\n%s", report)
	}
}

// TestJUnitEscapesPayloads pins that a body containing XML cannot break the
// document — the case where an API returning XML would corrupt its own report.
func TestJUnitEscapesPayloads(t *testing.T) {
	report, _ := junitReport(t, []runner.Result{{
		Name:    "a</testcase><injected/>",
		Status:  runner.StatusFail,
		Request: runner.Request{Method: "GET"},
		Actual:  validate.Message{StatusCode: "200", Body: `<result>&"'</result>`},
		Errors:  []string{"body: wrong"},
	}})

	var parsed junitSuite
	if err := xml.Unmarshal([]byte(strings.TrimPrefix(report, xml.Header)), &parsed); err != nil {
		t.Fatalf("a payload containing XML broke the document: %v", err)
	}
	if strings.Contains(report, "<injected/>") {
		t.Error("markup in a transaction name reached the document unescaped")
	}
	if len(parsed.Cases) != 1 {
		t.Errorf("cases = %d, want 1: the document was restructured by its own content", len(parsed.Cases))
	}
}

// TestJUnitDetailIsReproducible pins that the failure body says what was sent,
// so a reader can repeat the request without rerunning the suite.
func TestJUnitDetailIsReproducible(t *testing.T) {
	report, _ := junitReport(t, []runner.Result{{
		Name:   "a",
		Status: runner.StatusFail,
		Request: runner.Request{
			Method: "POST", URI: "/things",
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    `{"sent":true}`,
		},
		Actual: validate.Message{StatusCode: "500", Body: "boom"},
		Errors: []string{"statusCode: wrong"},
		Beyond: []string{"the server returned 500"},
	}})

	for _, want := range []string{"POST /things", "Content-Type: application/json", `{"sent":true}`, "500", "boom"} {
		if !strings.Contains(report, want) && !strings.Contains(unescape(report), want) {
			t.Errorf("report should record %q for reproduction:\n%s", want, report)
		}
	}
	// Findings Dredd would not raise stay labelled here too, so a CI failure
	// explains itself without the reader consulting the docs.
	if !strings.Contains(unescape(report), "[not checked by Dredd]") {
		t.Error("beyond-Dredd findings should be labelled in the report")
	}
}

func TestJUnitSuiteName(t *testing.T) {
	var out bytes.Buffer
	JUnit{Out: &out, Name: "inpace"}.Report([]runner.Result{{Name: "a", Status: runner.StatusPass}})
	if !strings.Contains(out.String(), `name="inpace"`) {
		t.Error("a suite name should be used when given")
	}
}

func TestJUnitPassingRun(t *testing.T) {
	report, passed := junitReport(t, []runner.Result{{Name: "a", Status: runner.StatusPass}})
	if !passed {
		t.Error("a clean run should pass")
	}
	if strings.Contains(report, "<failure") {
		t.Error("a clean run should record no failures")
	}
}

// unescape reverses XML entity encoding, so assertions can read the payload as
// it will appear once a CI system decodes it.
func unescape(s string) string {
	var buf bytes.Buffer
	xml.EscapeText(&buf, nil)
	replacer := strings.NewReplacer(
		"&lt;", "<", "&gt;", ">", "&amp;", "&", "&#39;", "'", "&#34;", `"`, "&#xA;", "\n")
	return replacer.Replace(s)
}
