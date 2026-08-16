package reporter

import (
	"bytes"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

func dotReport(t *testing.T, r Dot, results []runner.Result) (string, bool) {
	t.Helper()
	var out bytes.Buffer
	r.Out = &out
	passed := r.Report(results)
	return out.String(), passed
}

// TestDotWritesOneCharacterPerOutcome pins the shorthand itself.
//
// The characters are Dredd's — a dot passed, a dash was skipped, F failed and E
// never reached the server. Someone reading a vertrag report with Dredd's
// legend in their head must not be misled, so a change here is a change to what
// the output means, not to how it looks.
func TestDotWritesOneCharacterPerOutcome(t *testing.T) {
	output, _ := dotReport(t, Dot{}, []runner.Result{
		{Name: "a", Status: runner.StatusPass},
		{Name: "b", Status: runner.StatusFail, Errors: []string{"wrong"}},
		{Name: "c", Status: runner.StatusSkip},
		{Name: "d", Status: runner.StatusError, Errors: []string{"unreachable"}},
	})

	progress := strings.SplitN(output, "\n", 2)[0]
	if progress != ".F-E" {
		t.Errorf("progress = %q, want %q", progress, ".F-E")
	}
}

// TestDotExplainsEveryFailureAfterTheProgress pins that the compressed output
// stays actionable. A run reported as a row of characters and nothing else
// would have to be repeated under another reporter before anyone could learn
// what broke.
func TestDotExplainsEveryFailureAfterTheProgress(t *testing.T) {
	output, _ := dotReport(t, Dot{}, []runner.Result{{
		Name:   "/things > Create > 201",
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

	for _, want := range []string{
		"/things > Create > 201", "statusCode: wrong", "POST /things",
		"Content-Type: application/json", `{"sent":true}`, "500", "boom",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
	// Findings Dredd would not raise stay labelled, so a project arriving from
	// Dredd does not read one as a regression.
	if !strings.Contains(output, "[additional check]") {
		t.Errorf("beyond-Dredd findings should be labelled:\n%s", output)
	}
}

// TestDotWrapsALongRun pins the wrapping. Dredd emits the characters unbroken
// and lets the terminal fold them, which leaves a report written to a file as a
// single line as wide as the suite is long.
func TestDotWrapsALongRun(t *testing.T) {
	results := make([]runner.Result, dotsPerLine*2)
	for i := range results {
		results[i] = runner.Result{Name: "a", Status: runner.StatusPass}
	}

	output, _ := dotReport(t, Dot{}, results)

	for _, line := range strings.Split(output, "\n") {
		if len(line) > dotsPerLine {
			t.Fatalf("a line is %d characters wide, over the %d limit", len(line), dotsPerLine)
		}
	}
	if strings.Count(output, dotPass) != len(results) {
		t.Errorf("wrapping lost characters: %d of %d", strings.Count(output, dotPass), len(results))
	}
}

// TestDotSummaryCounts pins that the summary agrees with the characters above
// it, and that an unreachable server fails the run while a skip does not.
func TestDotSummaryCounts(t *testing.T) {
	output, passed := dotReport(t, Dot{}, []runner.Result{
		{Name: "a", Status: runner.StatusPass},
		{Name: "b", Status: runner.StatusSkip},
		{Name: "c", Status: runner.StatusError, Errors: []string{"unreachable"}},
	})

	if passed {
		t.Error("a run that never reached the server cannot claim the API conforms")
	}
	if !strings.Contains(output, "complete: 1 passing, 0 failing, 1 errors, 1 skipped (3 total") {
		t.Errorf("summary missing or wrong:\n%s", output)
	}

	_, passed = dotReport(t, Dot{}, []runner.Result{{Name: "a", Status: runner.StatusSkip}})
	if !passed {
		t.Error("skipping is a deliberate choice, not a failure")
	}
}

// TestDotColourIsOptional pins that a report written to a file carries no
// escape codes, which would otherwise be read as part of the text.
func TestDotColourIsOptional(t *testing.T) {
	plain, _ := dotReport(t, Dot{Color: false}, []runner.Result{{Name: "a", Status: runner.StatusPass}})
	if strings.Contains(plain, "\033[") {
		t.Errorf("colour should be absent when disabled: %q", plain)
	}

	coloured, _ := dotReport(t, Dot{Color: true}, []runner.Result{{Name: "a", Status: runner.StatusPass}})
	if !strings.Contains(coloured, "\033[") {
		t.Error("colour should be present when enabled")
	}
}

// TestDotReportsAnEmptyRun pins that no transactions produces a summary rather
// than an empty file, which is indistinguishable from a crash.
func TestDotReportsAnEmptyRun(t *testing.T) {
	output, passed := dotReport(t, Dot{}, nil)
	if !passed {
		t.Error("a run with nothing in it has nothing to fail")
	}
	if !strings.Contains(output, "0 passing") {
		t.Errorf("an empty run should still be summarised:\n%q", output)
	}
}
