package reporter

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/runner"
)

// The sentinels are credentials no report may contain. One is registered the
// way the run registers a principal's obtained credential; the other names a
// header only the description knows, sent as a custom carrier.
const (
	sentinelToken  = "SENTINEL-TOKEN-0f9e8d7c6b5a"
	sentinelCookie = "SENTINEL-COOKIE-1a2b3c4d5e"
	sentinelKey    = "SENTINEL-KEY-9z8y7x6w5v"
)

// hostileMatrix has a cell of every verdict, names that try to inject markup,
// and reasons and replays that quote every sentinel — in a header, a cookie,
// a query string and a body — as a careless caller might hand them over.
func hostileMatrix() Authorization {
	op1 := `GET /mail<script>alert(1)</script>`
	op2 := "DELETE /admin|x`y`\x1b[32m"
	reader := `reader"><img src=x onerror=alert(2)>`
	admin := "admin|&"
	return Authorization{
		Operations: []string{op1, op2},
		Principals: []string{reader, admin},
		Cells: []AuthorizationCell{
			{Operation: op1, Principal: reader, Expected: "200", Actual: "200", Verdict: AuthorizationPass},
			{Operation: op1, Principal: admin, Expected: "403", Actual: "200", Verdict: AuthorizationPass, Bypass: true,
				Reason: "authorization bypass: answered 200 with body {\"token\":\"" + sentinelToken + "\"}",
				Replay: "curl -X GET 'http://api/mail?api_key=" + sentinelKey + "' -H 'X-Tenant-Key: " + sentinelKey +
					"' -H 'Cookie: session=" + sentinelCookie + "' -H 'Authorization: Bearer " + sentinelToken + "' ```"},
			{Operation: op2, Principal: reader, Expected: "403", Verdict: AuthorizationUnsent,
				Reason: "not sent: the irreversible-operation guard"},
			// A pass with nothing that came back is not a pass.
			{Operation: op2, Principal: admin, Expected: "204", Verdict: AuthorizationPass},
		},
	}
}

func registerSentinels(t *testing.T) {
	t.Helper()
	t.Cleanup(ForgetSecrets)
	RegisterCredential("Bearer " + sentinelToken)
	RegisterCredential("session=" + sentinelCookie)
	RegisterSecret(sentinelKey)
}

// renderAll renders the matrix through every format that carries it, beside a
// passing result, and returns each format's output and whether it passed.
func renderAll(matrix *Authorization) map[string]string {
	results := []runner.Result{{Name: "GET /mail as reader", Status: runner.StatusPass,
		Request: runner.Request{Method: "GET", URI: "/mail"}}}
	out := map[string]string{}
	var b bytes.Buffer
	WithAuthorization(Markdown{Out: &b}, matrix).Report(results)
	out["markdown"] = b.String()
	b.Reset()
	WithAuthorization(HTML{Out: &b}, matrix).Report(results)
	out["html"] = b.String()
	b.Reset()
	WithAuthorization(JUnit{Out: &b}, matrix).Report(results)
	out["junit"] = b.String()
	b.Reset()
	WithAuthorization(JSON{Out: &b}, matrix).Report(results)
	out["json"] = b.String()
	if matrix != nil {
		b.Reset()
		WriteAuthorization(&b, *matrix, false)
		out["cli"] = b.String()
	}
	return out
}

func TestTheAuthorizationMatrixDisclosesNoCredential(t *testing.T) {
	registerSentinels(t)
	// --no-sanitize is for reading one's own header; a registered credential
	// is still kept out of the matrix, which is written for somebody else.
	SetSanitize(false)
	t.Cleanup(func() { SetSanitize(true) })

	matrix := hostileMatrix()
	for format, text := range renderAll(&matrix) {
		for _, sentinel := range []string{sentinelToken, sentinelCookie, sentinelKey} {
			if strings.Contains(text, sentinel) {
				t.Errorf("%s report contains the credential %s:\n%s", format, sentinel, text)
			}
		}
		if !strings.Contains(text, "authorization") && !strings.Contains(text, "Authorization") {
			t.Errorf("%s report has no matrix:\n%s", format, text)
		}
	}
}

func TestTheAuthorizationMatrixInjectsNoMarkup(t *testing.T) {
	matrix := hostileMatrix()
	out := renderAll(&matrix)

	for _, raw := range []string{"<script>", "<img", "\x1b["} {
		for _, format := range []string{"html", "markdown", "cli", "junit"} {
			// A terminal shows markup as text; only its escapes are a danger.
			if format == "cli" && raw != "\x1b[" {
				continue
			}
			if strings.Contains(out[format], raw) {
				t.Errorf("%s report carries %q unescaped:\n%s", format, raw, out[format])
			}
		}
	}
	// JSON is data, not markup: the name is a quoted string that parses back
	// to exactly what the description said.
	var doc struct {
		Authorization struct{ Operations []string } `json:"authorization"`
	}
	if err := json.Unmarshal([]byte(out["json"]), &doc); err != nil {
		t.Fatalf("the JSON report does not parse: %v", err)
	}
	if got := doc.Authorization.Operations; len(got) != 2 || got[0] != matrix.Operations[0] {
		t.Errorf("JSON operations = %q", got)
	}
	if strings.Contains(out["json"], "\x1b") || strings.Contains(out["json"], "\u001b") {
		t.Errorf("the JSON report carries a terminal escape:\n%s", out["json"])
	}
	if !strings.Contains(out["html"], "&lt;script&gt;") {
		t.Errorf("the HTML report lost the operation's name instead of escaping it:\n%s", out["html"])
	}

	// Every Markdown row of the matrix has the header's columns: a pipe in a
	// name must not split a cell.
	lines := strings.Split(out["markdown"], "\n")
	start := -1
	for i, line := range lines {
		if line == "## Authorization matrix" {
			start = i + 2
		}
	}
	if start < 0 {
		t.Fatalf("no matrix section:\n%s", out["markdown"])
	}
	columns := func(line string) int { return strings.Count(strings.ReplaceAll(line, `\|`, ""), "|") }
	want := columns(lines[start])
	for _, line := range lines[start : start+4] {
		if columns(line) != want {
			t.Errorf("row %q has %d separators, the header %d", line, columns(line), want)
		}
	}
	// The replay quotes three backticks, so its fence is longer.
	if !strings.Contains(out["markdown"], "````sh\n") {
		t.Errorf("the replay's fence can be closed by the replay:\n%s", out["markdown"])
	}

	// The JUnit file is still XML, and says what the matrix says.
	var suite struct {
		Properties []struct {
			Name  string `xml:"name,attr"`
			Value string `xml:"value,attr"`
		} `xml:"properties>property"`
	}
	if err := xml.Unmarshal([]byte(out["junit"]), &suite); err != nil {
		t.Fatalf("the JUnit report is not XML: %v\n%s", err, out["junit"])
	}
	properties := map[string]string{}
	for _, p := range suite.Properties {
		properties[p.Name] = p.Value
	}
	if got := properties["vertrag.authorization.bypasses"]; got != "1" {
		t.Errorf("bypasses property = %q, want 1", got)
	}
	cell := properties["vertrag.authorization.cell["+matrix.Operations[0]+" as "+matrix.Principals[0]+"]"]
	if cell != "pass expected 200 got 200" {
		t.Errorf("the passing cell's property is %q", cell)
	}
}

func TestNoUnsentCellAppearsPassing(t *testing.T) {
	matrix := hostileMatrix()
	var b bytes.Buffer
	if (JSON{Out: &b, Authorization: &matrix}).Report(nil) {
		t.Error("a matrix with a bypass passed")
	}
	var doc struct {
		Summary       struct{ Passed bool } `json:"summary"`
		Authorization struct {
			Summary AuthorizationCounts `json:"summary"`
			Cells   []AuthorizationCell `json:"cells"`
		} `json:"authorization"`
	}
	if err := json.Unmarshal(b.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Summary.Passed {
		t.Error("the JSON summary says a run with a bypass passed")
	}
	want := AuthorizationCounts{Cells: 4, Passing: 1, Failing: 1, Unsent: 2, Bypasses: 1}
	if doc.Authorization.Summary != want {
		t.Errorf("summary = %+v, want %+v", doc.Authorization.Summary, want)
	}
	verdicts := []AuthorizationVerdict{}
	for _, each := range doc.Authorization.Cells {
		verdicts = append(verdicts, each.Verdict)
	}
	wantVerdicts := []AuthorizationVerdict{AuthorizationPass, AuthorizationFail, AuthorizationUnsent, AuthorizationUnsent}
	if strings.Join(asStrings(verdicts), ",") != strings.Join(asStrings(wantVerdicts), ",") {
		t.Errorf("verdicts = %v, want %v", verdicts, wantVerdicts)
	}
	if reason := doc.Authorization.Cells[3].Reason; reason != "nothing was checked" {
		t.Errorf("a pass with no reply says %q", reason)
	}

	// Only failures and errors fail the matrix; unsent cells are shown.
	unsentOnly := Authorization{Operations: []string{"GET /a"}, Principals: []string{"p"},
		Cells: []AuthorizationCell{{Operation: "GET /a", Principal: "p", Expected: "200", Verdict: AuthorizationUnsent, Reason: "a hook skipped it"}}}
	var cli bytes.Buffer
	WriteAuthorization(&cli, unsentOnly, false)
	if strings.Contains(cli.String(), "pass") && !strings.Contains(cli.String(), "0 passing") {
		t.Errorf("an unsent cell reads as passing:\n%s", cli.String())
	}
	if !strings.Contains(cli.String(), "a hook skipped it") || !strings.Contains(cli.String(), "1 unsent") {
		t.Errorf("the unsent cell's reason is not shown:\n%s", cli.String())
	}

	// A cell the plan left out is unsent, not missing.
	gap := Authorization{Operations: []string{"GET /a"}, Principals: []string{"p", "q"},
		Cells: []AuthorizationCell{{Operation: "GET /a", Principal: "p", Expected: "200", Actual: "200", Verdict: AuthorizationPass}}}
	if got := gap.Counts(); got.Unsent != 1 || got.Passing != 1 {
		t.Errorf("counts with a missing cell = %+v", got)
	}

	// An unknown verdict is an error, never a pass.
	odd := Authorization{Operations: []string{"GET /a"}, Principals: []string{"p"},
		Cells: []AuthorizationCell{{Operation: "GET /a", Principal: "p", Expected: "200", Actual: "200", Verdict: "maybe"}}}
	if odd.Met() || odd.Counts().Errors != 1 {
		t.Errorf("an unknown verdict counted as %+v", odd.Counts())
	}
}

func TestAuthorizationVerdictOfNeverDefaultsToPass(t *testing.T) {
	cases := []struct {
		status runner.Status
		unsent bool
		want   AuthorizationVerdict
	}{
		{runner.StatusPass, false, AuthorizationPass},
		{runner.StatusPass, true, AuthorizationUnsent},
		{runner.StatusSkip, false, AuthorizationUnsent},
		{runner.StatusFail, false, AuthorizationFail},
		{runner.StatusError, false, AuthorizationError},
		{runner.Status("?"), false, AuthorizationError},
	}
	for _, c := range cases {
		if got := AuthorizationVerdictOf(c.status, c.unsent); got != c.want {
			t.Errorf("AuthorizationVerdictOf(%q, %v) = %q, want %q", c.status, c.unsent, got, c.want)
		}
	}
}

// A run without the phase renders exactly as it did before the matrix existed.
func TestAnAbsentMatrixChangesNoReport(t *testing.T) {
	for format, text := range renderAll(nil) {
		if strings.Contains(strings.ToLower(text), "authorization") {
			t.Errorf("%s report mentions authorization with no matrix:\n%s", format, text)
		}
	}
	results := []runner.Result{{Name: "GET /a", Status: runner.StatusPass}}
	var with, without bytes.Buffer
	WithAuthorization(Markdown{Out: &with}, nil).Report(results)
	Markdown{Out: &without}.Report(results)
	if with.String() != without.String() {
		t.Errorf("a nil matrix changed the Markdown report:\n%s\nvs\n%s", with.String(), without.String())
	}
}

func asStrings(verdicts []AuthorizationVerdict) []string {
	out := make([]string, len(verdicts))
	for i, v := range verdicts {
		out[i] = string(v)
	}
	return out
}
