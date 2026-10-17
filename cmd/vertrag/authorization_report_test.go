package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// The sentinels are the credentials of principals whose carriers are not
// Authorization: a description-named header and a cookie. No report of the
// phase may contain either, wherever the server echoes them.
const (
	carrierKey    = "SENTINEL-APIKEY-7d6c5b4a"
	carrierCookie = "SENTINEL-SID-3e2f1a0b"
)

// carrierAPI checks an X-Api-Key and a sid cookie, with /keyed's role check
// broken, and echoes whatever credential it was given in every body — the
// careless server a report must not turn into a leak.
func carrierAPI(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("X-Api-Key")
	sid := ""
	if cookie, err := r.Cookie("sid"); err == nil {
		sid = cookie.Value
	}
	reply := func(status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
	echo := fmt.Sprintf(`"key":%q,"sid":%q`, key, sid)
	switch {
	case r.URL.Path == "/keyed" && key == carrierKey:
		// Broken: the key holder has no admin role and is let in anyway.
		reply(http.StatusOK, `{"users":[],`+echo+`}`)
	case r.URL.Path == "/cookied" && sid == carrierCookie:
		// Allowed, but the body breaks the schema: a failing allowed cell.
		reply(http.StatusOK, `{`+echo+`}`)
	default:
		reply(http.StatusUnauthorized, `{"error":"who are you",`+echo+`}`)
	}
}

func carrierTransactions() []compile.Transaction {
	keyed := transactionFor("GET", "/keyed", "200", requires(compile.Requirement{need(keyScheme, "admin")}))
	keyed.Response.Schema = `{"type":"object","required":["users"]}`
	cookied := transactionFor("GET", "/cookied", "200", requires(compile.Requirement{need(cookieScheme, "admin")}))
	cookied.Response.Schema = `{"type":"object","required":["users"]}`
	return []compile.Transaction{keyed, cookied}
}

func carrierPrincipals() []config.Principal {
	return []config.Principal{
		{Name: "keyholder", Auth: config.Auth{Header: "X-Api-Key: " + carrierKey}},
		{Name: "sessionadmin", Auth: config.Auth{Header: "Cookie: sid=" + carrierCookie}, Roles: []string{"admin"}},
	}
}

// runCarrierMatrix plans, executes and adapts the matrix the way the phase
// will, and returns it beside the cells' results.
func runCarrierMatrix(t *testing.T, server *httptest.Server) (*reporter.Authorization, []runner.Result) {
	t.Helper()
	if server == nil {
		server = httptest.NewServer(http.HandlerFunc(carrierAPI))
		t.Cleanup(server.Close)
	}
	transactions := carrierTransactions()
	configured := carrierPrincipals()
	principals, err := authorizationPrincipals(configured, transactions)
	if err != nil {
		t.Fatal(err)
	}
	plan := planAuthorization(transactions, principals, nil)
	engine := runner.New(server.URL)
	credentials, unsupported, err := authorizationCredentials(context.Background(), engine.Client, server.URL, configured, principals)
	if err != nil {
		t.Fatal(err)
	}
	outcomes := executeAuthorization(context.Background(), engine, plan, credentials, unsupported)
	results := make([]runner.Result, 0, len(outcomes))
	for _, outcome := range outcomes {
		results = append(results, outcome.Result)
	}
	return authorizationMatrix(plan, outcomes), results
}

func TestTheAuthorizationMatrixReportsTheExecutorsOutcomes(t *testing.T) {
	t.Cleanup(reporter.ForgetSecrets)
	matrix, results := runCarrierMatrix(t, nil)

	got := []string{}
	for _, each := range matrix.Normal().Cells {
		line := each.Operation + " " + each.Principal + " " + string(each.Verdict) + " " + each.Expected + "/" + each.Actual
		if each.Bypass {
			line += " bypass"
		}
		got = append(got, line)
	}
	want := []string{
		"GET /cookied keyholder pass 401/401",
		"GET /cookied sessionadmin fail 200/200",
		"GET /keyed keyholder fail 403/200 bypass",
		"GET /keyed sessionadmin pass 401/401",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("matrix:\n  got  %q\n  want %q", got, want)
	}

	// Every format, results and matrix together, as a run will write them:
	// neither credential appears, though the server echoed both.
	var cli, markdown, html, junit, document bytes.Buffer
	reports := reporter.WithAuthorization(reporter.Multi{
		reporter.CLI{Out: &cli, Details: true},
		reporter.Markdown{Out: &markdown, Details: true},
		reporter.HTML{Out: &html, Details: true},
		reporter.JUnit{Out: &junit},
		reporter.JSON{Out: &document},
	}, matrix)
	if reports.Report(results) {
		t.Error("a matrix with a bypass passed")
	}
	reporter.WriteAuthorization(&cli, *matrix, false)
	for format, text := range map[string]string{"cli": cli.String(), "markdown": markdown.String(),
		"html": html.String(), "junit": junit.String(), "json": document.String()} {
		for _, secret := range []string{carrierKey, carrierCookie} {
			if strings.Contains(text, secret) {
				t.Errorf("the %s report carries %s:\n%s", format, secret, text)
			}
		}
		if !strings.Contains(strings.ToLower(text), "authorization") {
			t.Errorf("the %s report has no matrix", format)
		}
	}

	var parsed struct {
		Authorization struct {
			Summary reporter.AuthorizationCounts `json:"summary"`
			Cells   []reporter.AuthorizationCell `json:"cells"`
		} `json:"authorization"`
	}
	if err := json.Unmarshal(document.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if want := (reporter.AuthorizationCounts{Cells: 4, Passing: 2, Failing: 2, Bypasses: 1}); parsed.Authorization.Summary != want {
		t.Errorf("JSON summary = %+v, want %+v", parsed.Authorization.Summary, want)
	}
	bypass := parsed.Authorization.Cells[2]
	if !strings.HasPrefix(bypass.Replay, "curl ") || !strings.Contains(bypass.Replay, "X-Api-Key: "+reporter.Redacted) ||
		!strings.Contains(bypass.Reason, "authorization bypass") {
		t.Errorf("the bypass is not a redacted, replayable finding: %+v", bypass)
	}
}

// Sanitizing off is for reading one's own credential in the ordinary results;
// the matrix is still written for somebody else.
func TestTheAuthorizationMatrixRedactsWithoutSanitizing(t *testing.T) {
	t.Cleanup(reporter.ForgetSecrets)
	reporter.SetSanitize(false)
	t.Cleanup(func() { reporter.SetSanitize(true) })
	matrix, _ := runCarrierMatrix(t, nil)

	var cli, document bytes.Buffer
	reporter.WriteAuthorization(&cli, *matrix, false)
	(reporter.JSON{Out: &document, Authorization: matrix}).Report(nil)
	for _, text := range []string{cli.String(), document.String()} {
		for _, secret := range []string{carrierKey, carrierCookie} {
			if strings.Contains(text, secret) {
				t.Errorf("the matrix carries %s with sanitizing off:\n%s", secret, text)
			}
		}
	}
}

// Two runs against the same server give the same matrix, byte for byte.
func TestTheAuthorizationMatrixIsDeterministic(t *testing.T) {
	t.Cleanup(reporter.ForgetSecrets)
	server := httptest.NewServer(http.HandlerFunc(carrierAPI))
	t.Cleanup(server.Close)
	render := func() string {
		matrix, _ := runCarrierMatrix(t, server)
		var b bytes.Buffer
		(reporter.JSON{Out: &b, Authorization: matrix}).Report(nil)
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(b.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		return string(doc["authorization"])
	}
	if first, second := render(), render(); first != second {
		t.Errorf("two runs differ:\n%s\n%s", first, second)
	}
}

// The adapter keeps an unsent cell unsent, with no status, whatever its
// result says.
func TestTheAuthorizationAdapterNeverPassesAnUnsentOutcome(t *testing.T) {
	cell := authorizationCell{Operation: "GET /a", Principal: "p", Status: "200", Expect: expectAllowed}
	matrix := authorizationMatrix(
		authorizationPlan{Operations: []string{"GET /a"}, Principals: []string{"p"}, Cells: []authorizationCell{cell}},
		[]authorizationOutcome{{Cell: cell, Unsent: "a hook skipped it",
			Result: runner.Result{Status: runner.StatusPass, Actual: validate.Message{StatusCode: "200"}}}})
	got := matrix.Normal().Cells[0]
	if got.Verdict != reporter.AuthorizationUnsent || got.Actual != "" || got.Reason != "not sent: a hook skipped it" {
		t.Errorf("unsent outcome became %+v", got)
	}
}
