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

// TestJUnitSurvivesABinaryBody pins behaviour that already holds rather than
// behaviour that was fixed: the XML encoder substitutes U+FFFD for the bytes XML
// forbids, so a failing binary download cannot produce a document the CI system
// then refuses to parse — which would turn a reported failure into no report at
// all. It is pinned because it is a property of encoding/xml rather than of any
// code here, and nothing else would notice if a hand-rolled writer replaced it.
func TestJUnitSurvivesABinaryBody(t *testing.T) {
	report, _ := junitReport(t, []runner.Result{{
		Name:    "GET /download",
		Status:  runner.StatusFail,
		Actual:  validate.Message{StatusCode: "200", Body: "\x00\x01\xff\xfe\x00ab\x80\x00"},
		Request: runner.Request{Method: "GET"},
		// The first error line becomes the `message` attribute, which XML escapes
		// by a different path from character data. A hook can put anything there,
		// so both paths are worth covering.
		Errors: []string{"body: unexpected \x00 in \xff response"},
	}})

	var parsed junitSuite
	if err := xml.Unmarshal([]byte(strings.TrimPrefix(report, xml.Header)), &parsed); err != nil {
		t.Fatalf("a binary body broke the document: %v\n%s", err, report)
	}
	if strings.ContainsRune(report, 0) {
		t.Error("a NUL byte reached the document")
	}
	if parsed.Failures != 1 {
		t.Errorf("failures = %d, want the failure still reported", parsed.Failures)
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
	if !strings.Contains(unescape(report), "[additional check]") {
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

// TestJUnitCarriesProvenance pins that the report says which binary, which
// description, which server and which config file produced it. The signature
// line on stderr answers those questions at the terminal; the archived XML is
// what anybody reads later, and until now it could not.
func TestJUnitCarriesProvenance(t *testing.T) {
	var out bytes.Buffer
	JUnit{Out: &out, Run: Provenance{
		Version:  "0.4.0",
		Spec:     "./openapi.json",
		Endpoint: "http://localhost:4000",
		Config:   "vertrag.yml",
	}}.Report([]runner.Result{{Name: "a", Status: runner.StatusPass}})
	report := out.String()

	var parsed junitSuite
	if err := xml.Unmarshal([]byte(strings.TrimPrefix(report, xml.Header)), &parsed); err != nil {
		t.Fatalf("output does not parse as XML: %v\n%s", err, report)
	}
	if parsed.Properties == nil {
		t.Fatalf("no <properties> element:\n%s", report)
	}

	got := map[string]string{}
	for _, p := range parsed.Properties.Properties {
		got[p.Name] = p.Value
	}
	want := map[string]string{
		"vertrag.version":  "0.4.0",
		"vertrag.spec":     "./openapi.json",
		"vertrag.endpoint": "http://localhost:4000",
		"vertrag.config":   "vertrag.yml",
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("property %s = %q, want %q", name, got[name], value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("properties = %v, want exactly %v", got, want)
	}

	// The JUnit schema puts <properties> before the cases; consumers that
	// validate against it reject the other order.
	if strings.Index(report, "<properties>") > strings.Index(report, "<testcase") {
		t.Errorf("<properties> should precede the first <testcase>:\n%s", report)
	}
}

// TestJUnitOmitsUnknownProvenance pins two things: a value that is not known
// is not emitted as an empty property, and a run that knows nothing about
// itself has no <properties> element at all rather than an empty one.
func TestJUnitOmitsUnknownProvenance(t *testing.T) {
	var out bytes.Buffer
	JUnit{Out: &out, Run: Provenance{
		Version:  "0.4.0",
		Spec:     "./openapi.json",
		Endpoint: "http://localhost:4000",
		// No config file: a run configured entirely from the command line.
	}}.Report([]runner.Result{{Name: "a", Status: runner.StatusPass}})
	report := out.String()

	var parsed junitSuite
	if err := xml.Unmarshal([]byte(strings.TrimPrefix(report, xml.Header)), &parsed); err != nil {
		t.Fatalf("output does not parse as XML: %v\n%s", err, report)
	}
	for _, p := range parsed.Properties.Properties {
		if p.Name == "vertrag.config" {
			t.Errorf("a run without a config file should not report one: %q", p.Value)
		}
		if p.Value == "" {
			t.Errorf("property %s emitted with an empty value", p.Name)
		}
	}
	if len(parsed.Properties.Properties) != 3 {
		t.Errorf("properties = %d, want 3:\n%s", len(parsed.Properties.Properties), report)
	}

	report, _ = junitReport(t, []runner.Result{{Name: "a", Status: runner.StatusPass}})
	if strings.Contains(report, "<properties") {
		t.Errorf("a report with no provenance should carry no <properties> element:\n%s", report)
	}
}

// TestJUnitEscapesProvenance pins that the values are attribute-escaped. An
// endpoint carries `&` the moment it has two query parameters, and a bare one
// is invalid XML — the report the pipeline archived would then be unreadable
// for the sake of the line meant to make it explicable.
func TestJUnitEscapesProvenance(t *testing.T) {
	var out bytes.Buffer
	JUnit{Out: &out, Run: Provenance{
		Version:  "0.4.0",
		Spec:     `spec "quoted" <here>.json`,
		Endpoint: "http://localhost:4000/?tenant=a&mock=1",
		Config:   "it's.yml",
	}}.Report([]runner.Result{{Name: "a", Status: runner.StatusPass}})
	report := out.String()

	var parsed junitSuite
	if err := xml.Unmarshal([]byte(strings.TrimPrefix(report, xml.Header)), &parsed); err != nil {
		t.Fatalf("a provenance value broke the document: %v\n%s", err, report)
	}
	got := map[string]string{}
	for _, p := range parsed.Properties.Properties {
		got[p.Name] = p.Value
	}
	if got["vertrag.endpoint"] != "http://localhost:4000/?tenant=a&mock=1" {
		t.Errorf("endpoint round-tripped as %q", got["vertrag.endpoint"])
	}
	if got["vertrag.spec"] != `spec "quoted" <here>.json` {
		t.Errorf("spec round-tripped as %q", got["vertrag.spec"])
	}
	if got["vertrag.config"] != "it's.yml" {
		t.Errorf("config round-tripped as %q", got["vertrag.config"])
	}
	if strings.Contains(report, "a&mock") {
		t.Errorf("a bare & reached the document:\n%s", report)
	}
}
