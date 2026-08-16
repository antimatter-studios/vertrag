package reporter

import (
	"bytes"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

func htmlReport(t *testing.T, r HTML, results []runner.Result) (string, bool) {
	t.Helper()
	var out bytes.Buffer
	r.Out = &out
	passed := r.Report(results)
	return out.String(), passed
}

// TestHTMLEscapesEverythingItQuotes is the reason this reporter renders from a
// template rather than from Markdown the way Dredd's does.
//
// A response body is attacker-controlled in exactly the case a contract test is
// most useful — a staging server returning something unexpected — and the
// report is then opened in a browser, often from a CI artefact store on an
// internal domain. Markup reaching the page unescaped is a stored cross-site
// scripting hole with the test suite as its delivery mechanism.
func TestHTMLEscapesEverythingItQuotes(t *testing.T) {
	output, _ := htmlReport(t, HTML{}, []runner.Result{{
		Name:    `</h2><script>alert("name")</script>`,
		Status:  runner.StatusFail,
		Request: runner.Request{Method: "GET", URI: `/x"><script>alert(1)</script>`},
		Actual: validate.Message{
			StatusCode: "200",
			Body:       `<script>alert("body")</script>`,
		},
		Errors: []string{`<img src=x onerror=alert(1)>`},
	}})

	// Escaping is judged on tags, not on the text inside them: `onerror=` as
	// characters in a paragraph is inert, `<img` opening an element is not.
	for _, tag := range []string{"<script", "<img", "</h2><"} {
		if strings.Contains(output, tag) {
			t.Errorf("markup from the run reached the page as an element (%q):\n%s", tag, output)
		}
	}
	if !strings.Contains(output, "&lt;script&gt;") {
		t.Errorf("the payload should still be readable, escaped:\n%s", output)
	}
}

// TestHTMLIsSelfContained pins that the page renders with no network at all. A
// report is read from an artefact store, an email attachment or a laptop on a
// train, and one that fetches its styling from elsewhere is unreadable in all
// three.
func TestHTMLIsSelfContained(t *testing.T) {
	output, _ := htmlReport(t, HTML{}, []runner.Result{{Name: "a", Status: runner.StatusPass}})

	if !strings.HasPrefix(output, "<!DOCTYPE html>") {
		t.Error("the page should declare a doctype so browsers do not use quirks mode")
	}
	if !strings.Contains(output, "</html>") {
		t.Errorf("the document is unterminated:\n%s", output)
	}
	for _, forbidden := range []string{"http://", "https://", "<link", "<script", " src="} {
		if strings.Contains(output, forbidden) {
			t.Errorf("the page reaches outside itself with %q", forbidden)
		}
	}
	if !strings.Contains(output, "<style>") {
		t.Error("the styling should travel with the page")
	}
}

// TestHTMLShowsTheRunAndItsFailures pins the content: every transaction in the
// table, and enough of each failure to act on without rerunning the suite.
func TestHTMLShowsTheRunAndItsFailures(t *testing.T) {
	output, passed := htmlReport(t, HTML{}, []runner.Result{
		{Name: "listing", Status: runner.StatusPass, Request: runner.Request{Method: "GET"}},
		{Name: "creating", Status: runner.StatusFail,
			Request: runner.Request{Method: "POST", URI: "/things", Body: `{"sent":true}`},
			Actual:  validate.Message{StatusCode: "500", Body: "boom"},
			Errors:  []string{"statusCode: wrong"},
			Beyond:  []string{"the server returned 500"}},
	})

	if passed {
		t.Error("a run with a failure should not report as passing")
	}
	for _, want := range []string{
		"1 passing, 1 failing", "listing", "creating",
		"statusCode: wrong", "POST /things", `{&#34;sent&#34;:true}`, "500", "boom",
		"[additional check]",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("page missing %q:\n%s", want, output)
		}
	}
	// The passing transaction earns a table row and nothing more, so the
	// failures are what the page is about.
	if strings.Count(output, "<section>") != 1 {
		t.Errorf("only the failure should get a section:\n%s", output)
	}
}

// TestHTMLDetailsShowsPassingExchanges pins that the flag means the same thing
// here as in the CLI reporter, so a run cannot produce a detailed log next to a
// summary-only page.
func TestHTMLDetailsShowsPassingExchanges(t *testing.T) {
	results := []runner.Result{{
		Name:    "a",
		Status:  runner.StatusPass,
		Request: runner.Request{Method: "GET", URI: "/things"},
		Actual:  validate.Message{StatusCode: "200", Body: "hello"},
	}}

	quiet, passed := htmlReport(t, HTML{}, results)
	if !passed {
		t.Error("a clean run should pass")
	}
	if strings.Contains(quiet, "hello") {
		t.Errorf("a passing exchange should be hidden by default:\n%s", quiet)
	}

	verbose, _ := htmlReport(t, HTML{Details: true}, results)
	if !strings.Contains(verbose, "hello") {
		t.Errorf("details should show passing exchanges:\n%s", verbose)
	}
}

// TestHTMLTitleIsConfigurable pins that the title reaches both the tab and the
// heading, which is how a reader tells two suites' reports apart once they are
// open side by side.
func TestHTMLTitleIsConfigurable(t *testing.T) {
	output, _ := htmlReport(t, HTML{Title: "inpace"}, nil)
	if !strings.Contains(output, "<title>inpace</title>") || !strings.Contains(output, "<h1>inpace</h1>") {
		t.Errorf("the given title should name the page:\n%s", output)
	}

	fallback, _ := htmlReport(t, HTML{}, nil)
	if !strings.Contains(fallback, "<title>vertrag report</title>") {
		t.Errorf("a page with no title given should still have one:\n%s", fallback)
	}
	// An empty run has no table to draw, and an empty one reads as a rendering
	// failure rather than as a run that did nothing.
	if strings.Contains(fallback, "<table>") {
		t.Errorf("an empty run should not draw an empty table:\n%s", fallback)
	}
}
