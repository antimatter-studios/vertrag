package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
)

// authzAPI is a small API with two protected operations and the permission
// check a test can break: /mail wants any bearer holding mail:read, /admin
// wants admin. /open is served, but its description says nothing about
// security, so no cell for it may ever be sent.
type authzAPI struct {
	// broken forgets /admin's role check: any valid token is let in.
	broken bool
	// tokenType is what the OAuth2 token endpoint calls its tokens.
	tokenType string
	// override answers "path token" with this status and body instead.
	override map[string][2]string
	// onRequest runs before an API request is answered.
	onRequest func()

	mu   sync.Mutex
	seen []string // "path authorization-header", API paths only
}

var authzRoles = map[string][]string{
	"reader-token": {"mail:read"},
	"admin-token":  {"mail:read", "admin"},
}

func (a *authzAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		if a.tokenType == "fail" {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "admin-token", "token_type": a.tokenType})
		return
	}
	authorization := r.Header.Get("Authorization")
	a.mu.Lock()
	a.seen = append(a.seen, r.URL.Path+" "+authorization)
	a.mu.Unlock()
	if a.onRequest != nil {
		a.onRequest()
	}
	token, _ := strings.CutPrefix(authorization, "Bearer ")
	reply := func(status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
	if answer, ok := a.override[r.URL.Path+" "+token]; ok {
		var status int
		fmt.Sscan(answer[0], &status)
		reply(status, answer[1])
		return
	}
	roles, known := authzRoles[token]
	switch {
	case r.URL.Path == "/open":
		reply(http.StatusOK, `{}`)
	case !known:
		reply(http.StatusUnauthorized, `{"error":"who are you"}`)
	case r.URL.Path == "/mail" && slices.Contains(roles, "mail:read"):
		reply(http.StatusOK, `{"messages":[]}`)
	case r.URL.Path == "/admin" && (a.broken || slices.Contains(roles, "admin")):
		reply(http.StatusOK, `{"users":[]}`)
	default:
		reply(http.StatusForbidden, `{"error":"not yours"}`)
	}
}

func (a *authzAPI) requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.seen)
}

func authzTransactions() []compile.Transaction {
	mail := transactionFor("GET", "/mail", "200", requires(compile.Requirement{need(bearerScheme, "mail:read")}))
	mail.Response.Schema = `{"type":"object","required":["messages"]}`
	admin := transactionFor("GET", "/admin", "200", requires(compile.Requirement{need(bearerScheme, "admin")}))
	admin.Response.Schema = `{"type":"object","required":["users"]}`
	return []compile.Transaction{mail, admin, transactionFor("GET", "/open", "200", compile.Security{})}
}

func authzPrincipals(endpoint string) []config.Principal {
	return []config.Principal{
		{Name: "anonymous"},
		{Name: "reader", Auth: config.Auth{Header: "Authorization: Bearer reader-token"}, Roles: []string{"mail:read"}},
		{Name: "admin", Auth: config.Auth{OAuth2: config.OAuth2{
			TokenURL: endpoint + "/token", ClientID: "vertrag", ClientSecret: "secret"}},
			Roles: []string{"mail:read", "admin"}},
	}
}

type authzRun struct {
	ctx       context.Context
	skip      func(compile.Transaction) string
	configure func(*runner.Runner)
}

// runAuthz plans and executes the matrix against api the way the phase will:
// principals read, credentials obtained once, every cell through SendAs.
func runAuthz(t *testing.T, api *authzAPI, run authzRun) ([]authorizationOutcome, map[string]string) {
	t.Helper()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	if run.ctx == nil {
		run.ctx = context.Background()
	}
	transactions := authzTransactions()
	configured := authzPrincipals(server.URL)
	principals, err := authorizationPrincipals(configured, transactions)
	if err != nil {
		t.Fatal(err)
	}
	plan := planAuthorization(transactions, principals, run.skip)
	engine := runner.New(server.URL)
	// A run-wide credential, as the advice for a missing one sets: it must
	// never reach a request sent as somebody else.
	engine.ExtraHeaders = []string{"Authorization: Bearer admin-token"}
	if run.configure != nil {
		run.configure(engine)
	}
	credentials, unsupported, err := authorizationCredentials(run.ctx, engine.Client, server.URL, configured, principals)
	if err != nil {
		t.Fatal(err)
	}
	return executeAuthorization(run.ctx, engine, plan, credentials, unsupported), unsupported
}

// verdicts reads outcomes as "path principal verdict" lines, in plan order.
func verdicts(outcomes []authorizationOutcome) []string {
	var lines []string
	for _, outcome := range outcomes {
		verdict := string(outcome.Result.Status)
		if outcome.Bypass {
			verdict += " bypass"
		}
		lines = append(lines, outcome.Cell.Transaction.Request.URI+" "+outcome.Cell.Principal+" "+verdict)
	}
	return lines
}

func expectVerdicts(t *testing.T, outcomes []authorizationOutcome, want ...string) {
	t.Helper()
	if got := verdicts(outcomes); !slices.Equal(got, want) {
		t.Fatalf("verdicts:\n  got  %q\n  want %q", got, want)
	}
}

func TestAuthorizationExecutorPassesACorrectServer(t *testing.T) {
	api := &authzAPI{}
	outcomes, unsupported := runAuthz(t, api, authzRun{})
	if len(unsupported) != 0 {
		t.Fatalf("unsupported principals: %v", unsupported)
	}
	expectVerdicts(t, outcomes,
		"/admin anonymous pass", "/admin reader pass", "/admin admin pass",
		"/mail anonymous pass", "/mail reader pass", "/mail admin pass",
		"/open anonymous skip", "/open reader skip", "/open admin skip")

	// Each principal sent its own credential and nobody else's: the run-wide
	// admin token reached only admin's requests.
	want := []string{
		"/admin ", "/admin Bearer reader-token", "/admin Bearer admin-token",
		"/mail ", "/mail Bearer reader-token", "/mail Bearer admin-token",
	}
	if got := api.requests(); !slices.Equal(got, want) {
		t.Fatalf("requests:\n  got  %q\n  want %q", got, want)
	}
	for _, outcome := range outcomes[6:] {
		if !strings.Contains(outcome.Unsent, "does not") || outcome.Result.Status == runner.StatusPass {
			t.Errorf("undetermined cell %s: unsent %q, status %s", outcome.Result.Name, outcome.Unsent, outcome.Result.Status)
		}
	}
}

func TestAuthorizationExecutorFindsABrokenPermissionCheck(t *testing.T) {
	// The redaction must not depend on the run sanitizing its reports.
	reporter.SetSanitize(false)
	t.Cleanup(func() { reporter.SetSanitize(true) })

	outcomes, _ := runAuthz(t, &authzAPI{broken: true}, authzRun{})
	expectVerdicts(t, outcomes,
		"/admin anonymous pass", "/admin reader fail bypass", "/admin admin pass",
		"/mail anonymous pass", "/mail reader pass", "/mail admin pass",
		"/open anonymous skip", "/open reader skip", "/open admin skip")

	bypass := outcomes[1]
	if len(bypass.Result.Errors) != 1 || !strings.Contains(bypass.Result.Errors[0], "authorization bypass") ||
		!strings.Contains(bypass.Result.Errors[0], "403") {
		t.Errorf("bypass errors: %q", bypass.Result.Errors)
	}
	if !strings.HasPrefix(bypass.Replay, "curl -X GET ") || !strings.Contains(bypass.Replay, "/admin") ||
		!strings.Contains(bypass.Replay, "Authorization: "+reporter.Redacted) {
		t.Errorf("replay is not a redacted reproduction: %s", bypass.Replay)
	}
	for _, text := range append([]string{bypass.Replay}, bypass.Result.Errors...) {
		if strings.Contains(text, "reader-token") || strings.Contains(text, "admin-token") {
			t.Errorf("a credential leaked into the finding: %s", text)
		}
	}
	for i, outcome := range outcomes {
		if i != 1 && outcome.Replay != "" {
			t.Errorf("%s has a replay without failing", outcome.Result.Name)
		}
	}
}

func TestAuthorizationExecutorFailsWrongRefusalsAndServerErrors(t *testing.T) {
	outcomes, _ := runAuthz(t, &authzAPI{override: map[string][2]string{
		"/admin reader-token": {"500", `{"error":"boom"}`},
		"/mail ":              {"404", `{"error":"hidden"}`},
		"/mail admin-token":   {"200", `{"nothing":true}`},
		"/admin admin-token":  {"503", `{"error":"down"}`},
	}}, authzRun{})
	expectVerdicts(t, outcomes,
		"/admin anonymous pass", "/admin reader fail", "/admin admin fail",
		"/mail anonymous fail", "/mail reader pass", "/mail admin fail",
		"/open anonymous skip", "/open reader skip", "/open admin skip")

	for index, want := range map[int]string{
		1: "failed with 500 where it should have refused reader with 403",
		2: "failed with 503 where 200 was documented",
		3: "refused anonymous with 404 where the description requires 401",
		5: "body",
	} {
		if errs := strings.Join(outcomes[index].Result.Errors, "\n"); !strings.Contains(errs, want) {
			t.Errorf("%s: errors %q, want %q", outcomes[index].Result.Name, errs, want)
		}
	}
}

// stubHooks is a hook worker that does what before says to every request.
type authzHooks struct{ before func(*runner.Transaction) }

func (authzHooks) BeforeAll([]*runner.Transaction) error { return nil }
func (h authzHooks) BeforeEach(transaction *runner.Transaction) error {
	h.before(transaction)
	return nil
}
func (authzHooks) BeforeEachValidation(*runner.Transaction) error { return nil }
func (authzHooks) AfterEach(*runner.Transaction) error            { return nil }
func (authzHooks) AfterAll([]*runner.Transaction) error           { return nil }

func TestAuthorizationExecutorNeverPassesAnUnsentCell(t *testing.T) {
	t.Run("the run's safety decision", func(t *testing.T) {
		api := &authzAPI{}
		outcomes, _ := runAuthz(t, api, authzRun{skip: func(transaction compile.Transaction) string {
			if transaction.Request.URI == "/mail" {
				return "irreversible"
			}
			return ""
		}})
		expectVerdicts(t, outcomes,
			"/admin anonymous pass", "/admin reader pass", "/admin admin pass",
			"/mail anonymous skip", "/mail reader skip", "/mail admin skip",
			"/open anonymous skip", "/open reader skip", "/open admin skip")
		if outcomes[3].Unsent != "irreversible" || !strings.Contains(outcomes[3].Result.Errors[0], "not sent") {
			t.Errorf("unsent reason: %q, errors %q", outcomes[3].Unsent, outcomes[3].Result.Errors)
		}
		for _, request := range api.requests() {
			if !strings.HasPrefix(request, "/admin ") {
				t.Errorf("an unsent cell was sent: %s", request)
			}
		}
	})

	t.Run("a hook skips", func(t *testing.T) {
		api := &authzAPI{}
		outcomes, _ := runAuthz(t, api, authzRun{configure: func(engine *runner.Runner) {
			engine.Hooks = authzHooks{before: func(transaction *runner.Transaction) {
				transaction.Skip = strings.HasPrefix(transaction.Name, "/admin")
			}}
		}})
		expectVerdicts(t, outcomes,
			"/admin anonymous skip", "/admin reader skip", "/admin admin skip",
			"/mail anonymous pass", "/mail reader pass", "/mail admin pass",
			"/open anonymous skip", "/open reader skip", "/open admin skip")
		if outcomes[0].Unsent != "a hook skipped it" {
			t.Errorf("unsent reason: %q", outcomes[0].Unsent)
		}
	})

	t.Run("a hook changes who is asking", func(t *testing.T) {
		// The hook makes every request admin's. Only admin's own requests,
		// whose credential it leaves as it was, may be sent; the rest would
		// test admin under another principal's name.
		api := &authzAPI{broken: true}
		outcomes, _ := runAuthz(t, api, authzRun{configure: func(engine *runner.Runner) {
			engine.Hooks = authzHooks{before: func(transaction *runner.Transaction) {
				for name := range transaction.Request.Headers {
					if strings.EqualFold(name, "Authorization") {
						delete(transaction.Request.Headers, name)
					}
				}
				transaction.Request.Headers["Authorization"] = "Bearer admin-token"
			}}
		}})
		expectVerdicts(t, outcomes,
			"/admin anonymous skip", "/admin reader skip", "/admin admin pass",
			"/mail anonymous skip", "/mail reader skip", "/mail admin pass",
			"/open anonymous skip", "/open reader skip", "/open admin skip")
		if !strings.Contains(outcomes[0].Unsent, "hook changed the credential") {
			t.Errorf("unsent reason: %q", outcomes[0].Unsent)
		}
		want := []string{"/admin Bearer admin-token", "/mail Bearer admin-token"}
		if got := api.requests(); !slices.Equal(got, want) {
			t.Fatalf("requests:\n  got  %q\n  want %q", got, want)
		}
	})
}

func TestAuthorizationExecutorChecksTheObtainedCarrier(t *testing.T) {
	t.Run("an OAuth2 token that is not Bearer", func(t *testing.T) {
		api := &authzAPI{broken: true, tokenType: "MAC"}
		outcomes, unsupported := runAuthz(t, api, authzRun{})
		if reason := unsupported["admin"]; !strings.Contains(reason, "Authorization mac") ||
			!strings.Contains(reason, "Authorization bearer") || len(unsupported) != 1 {
			t.Fatalf("unsupported: %v", unsupported)
		}
		expectVerdicts(t, outcomes,
			"/admin anonymous pass", "/admin reader fail bypass", "/admin admin skip",
			"/mail anonymous pass", "/mail reader pass", "/mail admin skip",
			"/open anonymous skip", "/open reader skip", "/open admin skip")
		for _, request := range api.requests() {
			if strings.Contains(request, "admin-token") {
				t.Errorf("an unsupported credential was sent: %s", request)
			}
		}
	})

	t.Run("a credential that cannot be obtained ends the phase", func(t *testing.T) {
		server := httptest.NewServer(&authzAPI{tokenType: "fail"})
		defer server.Close()
		configured := authzPrincipals(server.URL)
		principals, err := authorizationPrincipals(configured, authzTransactions())
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = authorizationCredentials(context.Background(), server.Client(), server.URL, configured, principals)
		if err == nil || !strings.Contains(err.Error(), `principal "admin"`) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a cookie carrier read as written", func(t *testing.T) {
		for line, want := range map[string]credentialCarrier{
			"":                         {},
			"Cookie: sid=1":            {Cookie: "sid"},
			"authorization: Basic abc": {Header: "authorization", Scheme: "basic"},
			"X-Api-Key: k":             {Header: "X-Api-Key"},
		} {
			got, err := observedCarrier(line)
			if err != nil || !sameCarrier(got, want) {
				t.Errorf("observedCarrier(%q) = %+v, %v; want %+v", line, got, err, want)
			}
		}
		if _, err := observedCarrier("Cookie: a=1; b=2"); err == nil {
			t.Error("two cookies were read as one credential")
		}
		if sameCarrier(credentialCarrier{Cookie: "SID"}, credentialCarrier{Cookie: "sid"}) {
			t.Error("cookie names compared without case")
		}
	})
}

func TestAuthorizationExecutorStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := &authzAPI{onRequest: cancel}
	outcomes, _ := runAuthz(t, api, authzRun{ctx: ctx})
	if got := len(api.requests()); got != 1 {
		t.Fatalf("%d requests after cancelling at the first, want 1", got)
	}
	if status := outcomes[0].Result.Status; status != runner.StatusPass && status != runner.StatusSkip {
		t.Errorf("the cell in flight: %s", status)
	}
	for _, outcome := range outcomes[1:] {
		if outcome.Result.Status != runner.StatusSkip || outcome.Unsent == "" {
			t.Errorf("%s after cancelling: %s, unsent %q", outcome.Result.Name, outcome.Result.Status, outcome.Unsent)
		}
	}
	if !strings.Contains(outcomes[1].Unsent, "cancelled") {
		t.Errorf("unsent reason: %q", outcomes[1].Unsent)
	}
}

func TestAuthorizationReplayRedactsEveryCarrier(t *testing.T) {
	reporter.SetSanitize(false)
	t.Cleanup(func() { reporter.SetSanitize(true) })

	replay := authorizationReplay(runner.Request{
		Method: "DELETE", URL: "http://api.test/admin/users/7",
		Headers: map[string]string{
			"X-Tenant-Key": "tenant-secret",
			"Cookie":       "sid=session-secret; theme=dark",
			"Accept":       "application/json",
		},
	}, runner.Carriers{Headers: []string{"x-tenant-key"}, Cookies: []string{"sid"}})
	for _, secret := range []string{"tenant-secret", "session-secret"} {
		if strings.Contains(replay, secret) {
			t.Errorf("%s leaked into %s", secret, replay)
		}
	}
	for _, kept := range []string{"X-Tenant-Key: " + reporter.Redacted, "sid=" + reporter.Redacted, "theme=dark",
		"Accept: application/json", "http://api.test/admin/users/7"} {
		if !strings.Contains(replay, kept) {
			t.Errorf("%q missing from %s", kept, replay)
		}
	}
}
