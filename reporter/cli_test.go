package reporter

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

func report(t *testing.T, r CLI, results []runner.Result) (string, bool) {
	t.Helper()
	var out bytes.Buffer
	r.Out = &out
	passed := r.Report(results)
	return out.String(), passed
}

func TestReportSummary(t *testing.T) {
	results := []runner.Result{
		{Name: "a", Status: runner.StatusPass, Request: runner.Request{Method: "GET"}},
		{Name: "b", Status: runner.StatusFail, Request: runner.Request{Method: "POST"},
			Errors: []string{"statusCode: Expected status code '200', but got '500'."}},
		{Name: "c", Status: runner.StatusSkip},
		{Name: "d", Status: runner.StatusError, Errors: []string{"connection refused"}},
	}

	output, passed := report(t, CLI{}, results)

	if passed {
		t.Error("a run with failures should not report as passing")
	}
	if !strings.Contains(output, "4 total, 1 passing, 1 failing, 1 errors, 1 skipped") {
		t.Errorf("summary missing or wrong:\n%s", output)
	}
	for _, want := range []string{"pass: GET a", "fail: POST b", "skip:", "error:"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
	// The failure detail is printed after the run, so the per-transaction lines
	// stay readable while it is in progress.
	if !strings.Contains(output, "FAIL: b") {
		t.Errorf("failure detail missing:\n%s", output)
	}
	if !strings.Contains(output, "Expected status code '200', but got '500'.") {
		t.Errorf("failure message missing:\n%s", output)
	}
}

// TestErrorCountsAsFailure pins that a run which never reached the API cannot
// claim the API conforms.
func TestErrorCountsAsFailure(t *testing.T) {
	_, passed := report(t, CLI{}, []runner.Result{{Name: "a", Status: runner.StatusError}})
	if passed {
		t.Error("an error should make the run fail")
	}
}

func TestAllPassing(t *testing.T) {
	output, passed := report(t, CLI{}, []runner.Result{
		{Name: "a", Status: runner.StatusPass, Duration: time.Millisecond},
	})
	if !passed {
		t.Error("a clean run should pass")
	}
	if strings.Contains(output, "FAIL") {
		t.Errorf("a clean run should print no failures:\n%s", output)
	}
}

// TestSkipsDoNotFailTheRun pins that a suite which skips everything still
// passes — skipping is a deliberate choice, not a problem.
func TestSkipsDoNotFailTheRun(t *testing.T) {
	_, passed := report(t, CLI{}, []runner.Result{{Name: "a", Status: runner.StatusSkip}})
	if !passed {
		t.Error("skips should not fail the run")
	}
}

func TestFailureShowsTheExchange(t *testing.T) {
	output, _ := report(t, CLI{}, []runner.Result{{
		Name:   "a",
		Status: runner.StatusFail,
		Request: runner.Request{
			Method: "POST", URI: "/things",
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    `{"sent":true}`,
		},
		Actual: validate.Message{
			StatusCode: "500",
			Headers:    map[string]string{"content-type": "text/plain"},
			Body:       "boom",
		},
		Errors: []string{"statusCode: wrong"},
	}})

	for _, want := range []string{"POST /things", "Content-Type: application/json", `{"sent":true}`, "500", "boom"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

// TestLongBodiesAreTruncated pins that one enormous payload cannot bury the
// rest of the report.
func TestLongBodiesAreTruncated(t *testing.T) {
	output, _ := report(t, CLI{}, []runner.Result{{
		Name:    "a",
		Status:  runner.StatusFail,
		Actual:  validate.Message{StatusCode: "200", Body: strings.Repeat("x", 5000)},
		Request: runner.Request{Method: "GET"},
		Errors:  []string{"body: wrong"},
	}})

	if !strings.Contains(output, "bytes truncated") {
		t.Error("a long body should be truncated")
	}
	if len(output) > 4000 {
		t.Errorf("truncated output is still %d bytes", len(output))
	}
}

// TestABinaryBodyCannotCorruptTheTerminalReport pins that a body is treated as
// bytes the server chose rather than as text the report can trust. A failing
// `application/octet-stream` download reaches this printer, and printed as it
// stands its NULs and invalid UTF-8 garble the surrounding lines while an escape
// sequence in it can repaint them — including into a line that reads as a pass.
func TestABinaryBodyCannotCorruptTheTerminalReport(t *testing.T) {
	output, _ := report(t, CLI{}, []runner.Result{{
		Name:   "a",
		Status: runner.StatusFail,
		Actual: validate.Message{
			StatusCode: "200",
			Body:       "\x00\xff\x80before\x1b[2K\rpass: GET /forged\x07",
		},
		Request: runner.Request{Method: "GET"},
		Errors:  []string{"body: wrong"},
	}})

	for _, forbidden := range []string{"\x00", "\x1b", "\x07", "\xff", "\x80"} {
		if strings.Contains(output, forbidden) {
			t.Errorf("the report still carries the byte %q from the body", forbidden)
		}
	}
	// The readable part of the body has to survive, or the substitution has
	// destroyed the evidence rather than made it safe to look at.
	if !strings.Contains(output, "before") {
		t.Errorf("printable text should be kept:\n%s", output)
	}
	if !utf8.ValidString(output) {
		t.Error("the report should be valid UTF-8 whatever the body was")
	}
}

// TestCRLFBodiesPrintWithoutSubstitution pins that guarding against a stray
// carriage return does not disfigure the bodies that legitimately contain one.
// A CSV is specified with CRLF line endings, so treating every return as a
// cursor move would fill the commonest non-JSON body with replacement
// characters.
func TestCRLFBodiesPrintWithoutSubstitution(t *testing.T) {
	output, _ := report(t, CLI{}, []runner.Result{{
		Name:    "a",
		Status:  runner.StatusFail,
		Actual:  validate.Message{StatusCode: "200", Body: "id,name\r\n1,alice\r\n"},
		Request: runner.Request{Method: "GET"},
		Errors:  []string{"body: wrong"},
	}})

	if strings.Contains(output, "�") {
		t.Errorf("a CRLF body should print as it is:\n%s", output)
	}
	if !strings.Contains(output, "1,alice") {
		t.Errorf("output missing the CSV row:\n%s", output)
	}
}

func TestColour(t *testing.T) {
	plain, _ := report(t, CLI{Color: false}, []runner.Result{{Name: "a", Status: runner.StatusPass}})
	if strings.Contains(plain, "\033[") {
		t.Error("colour should be absent when disabled")
	}

	coloured, _ := report(t, CLI{Color: true}, []runner.Result{{Name: "a", Status: runner.StatusPass}})
	if !strings.Contains(coloured, "\033[") {
		t.Error("colour should be present when enabled")
	}
}

func TestDetailsShowsPassingExchanges(t *testing.T) {
	results := []runner.Result{{
		Name:    "a",
		Status:  runner.StatusPass,
		Request: runner.Request{Method: "GET", URI: "/things"},
		Actual:  validate.Message{StatusCode: "200", Body: "hello"},
	}}

	quiet, _ := report(t, CLI{}, results)
	if strings.Contains(quiet, "hello") {
		t.Error("a passing exchange should be hidden without --details")
	}

	verbose, _ := report(t, CLI{Details: true}, results)
	if !strings.Contains(verbose, "hello") {
		t.Error("--details should show passing exchanges")
	}
}

func TestAnnotations(t *testing.T) {
	var out bytes.Buffer
	CLI{Out: &out}.Annotations([]Annotation{
		{Type: "warning", Message: "something odd", Line: 12, Column: 5},
		{Type: "error", Message: "line one\nline two"},
	})

	output := out.String()
	if !strings.Contains(output, "warn: something odd (line 12, column 5)") {
		t.Errorf("warning missing its position:\n%s", output)
	}
	// A multi-line message is flattened so one diagnostic stays one line.
	if !strings.Contains(output, "error: line one line two") {
		t.Errorf("multi-line message should be flattened:\n%s", output)
	}
}
