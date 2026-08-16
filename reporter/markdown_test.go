package reporter

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

func markdownReport(t *testing.T, r Markdown, results []runner.Result) (string, bool) {
	t.Helper()
	var out bytes.Buffer
	r.Out = &out
	passed := r.Report(results)
	return out.String(), passed
}

// TestMarkdownTableHasOneRowPerTransaction pins the part of the report a reader
// looks at first: the whole run, in order, whether or not it failed.
func TestMarkdownTableHasOneRowPerTransaction(t *testing.T) {
	output, passed := markdownReport(t, Markdown{}, []runner.Result{
		{Name: "a", Status: runner.StatusPass, Request: runner.Request{Method: "GET"}, Duration: time.Millisecond},
		{Name: "b", Status: runner.StatusFail, Request: runner.Request{Method: "POST"}, Errors: []string{"wrong"}},
		{Name: "c", Status: runner.StatusSkip},
	})

	if passed {
		t.Error("a run with a failure should not report as passing")
	}
	if !strings.Contains(output, "**1 passing, 1 failing, 0 errors, 1 skipped (3 total") {
		t.Errorf("summary missing or wrong:\n%s", output)
	}

	rows := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "| ") && !strings.HasPrefix(line, "| ---") && !strings.Contains(line, "Transaction") {
			rows++
		}
	}
	if rows != 3 {
		t.Errorf("table has %d rows, want one per transaction:\n%s", rows, output)
	}
}

// TestMarkdownEscapesPipesInTableCells pins that content cannot restructure the
// table. A transaction name carrying a pipe — a query string is enough — would
// otherwise split its row into extra columns and shift every later cell, so the
// table would confidently attribute the failure to the wrong transaction.
func TestMarkdownEscapesPipesInTableCells(t *testing.T) {
	output, _ := markdownReport(t, Markdown{}, []runner.Result{
		{Name: "/search?q=a|b > 200", Status: runner.StatusPass, Request: runner.Request{Method: "GET"}},
	})

	var row string
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "/search") {
			row = line
		}
	}
	if row == "" {
		t.Fatalf("the transaction is missing from the table:\n%s", output)
	}
	if strings.Count(strings.ReplaceAll(row, `\|`, ""), "|") != 5 {
		t.Errorf("the pipe in the name changed the row's shape: %q", row)
	}
}

// TestMarkdownFencesOutlastBackticksInAPayload pins that a body containing a
// code fence cannot end its own block. An API that returns Markdown — a docs
// endpoint, an error carrying a snippet — would otherwise close the fence early
// and leave the rest of the report rendering as prose.
func TestMarkdownFencesOutlastBackticksInAPayload(t *testing.T) {
	body := "here is a fence:\n```\ninside\n```\ndone"
	output, _ := markdownReport(t, Markdown{}, []runner.Result{{
		Name:    "a",
		Status:  runner.StatusFail,
		Request: runner.Request{Method: "GET", URI: "/docs"},
		Actual:  validate.Message{StatusCode: "200", Body: body},
		Errors:  []string{"body: wrong"},
	}})

	if !strings.Contains(output, "````") {
		t.Errorf("a body containing a fence needs a longer one around it:\n%s", output)
	}
	// Every fence must be paired, or everything after the odd one out renders as
	// code — or as prose — for the rest of the document.
	fences := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "````") {
			fences++
		}
	}
	if fences%2 != 0 {
		t.Errorf("%d fences of that length, so one is unclosed:\n%s", fences, output)
	}
}

// TestMarkdownRecordsWhatWasSentForFailures pins that a written report is
// enough on its own: a reader who did not run the suite can repeat the request
// by hand from what the document says.
func TestMarkdownRecordsWhatWasSentForFailures(t *testing.T) {
	output, _ := markdownReport(t, Markdown{}, []runner.Result{{
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

	for _, want := range []string{
		"## fail: a", "### Messages", "statusCode: wrong",
		"### Request", "POST /things", "Content-Type: application/json", `{"sent":true}`,
		"### Response", "500", "boom",
		"### Additional checks", "the server returned 500",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("report missing %q:\n%s", want, output)
		}
	}
}

// TestMarkdownHidesPassingExchangesUnlessAsked pins that a green run produces a
// short document. A report where every passing transaction quotes its payloads
// is long enough that nobody opens it, which is the same as having no report.
func TestMarkdownHidesPassingExchangesUnlessAsked(t *testing.T) {
	results := []runner.Result{{
		Name:    "a",
		Status:  runner.StatusPass,
		Request: runner.Request{Method: "GET", URI: "/things"},
		Actual:  validate.Message{StatusCode: "200", Body: "hello"},
	}}

	quiet, passed := markdownReport(t, Markdown{}, results)
	if !passed {
		t.Error("a clean run should pass")
	}
	if strings.Contains(quiet, "hello") {
		t.Errorf("a passing exchange should be hidden by default:\n%s", quiet)
	}

	verbose, _ := markdownReport(t, Markdown{Details: true}, results)
	if !strings.Contains(verbose, "hello") {
		t.Errorf("details should show passing exchanges:\n%s", verbose)
	}
}

// TestMarkdownTitleIsConfigurable pins that a pipeline reporting several suites
// can tell the documents apart.
func TestMarkdownTitleIsConfigurable(t *testing.T) {
	output, _ := markdownReport(t, Markdown{Title: "inpace"}, nil)
	if !strings.HasPrefix(output, "# inpace\n") {
		t.Errorf("the given title should head the document:\n%s", output)
	}

	fallback, _ := markdownReport(t, Markdown{}, nil)
	if !strings.HasPrefix(fallback, "# vertrag report\n") {
		t.Errorf("a document with no title given should still have one:\n%s", fallback)
	}
}
