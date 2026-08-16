package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/runner"
)

func named(name string) compile.Transaction {
	return compile.Transaction{
		Name:     name,
		Request:  compile.Request{Method: "GET", URI: "/thing"},
		Response: compile.Response{Status: "200"},
	}
}

// applyConfiguredRules is the seam between a config file and a run: everything
// either side of it has tests, and a break here would leave a configured
// credential silently doing nothing.
func TestApplyConfiguredRulesLogsInAndCarriesTheCredential(t *testing.T) {
	logins := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logins++
		http.SetCookie(w, &http.Cookie{Name: "jwt_token", Value: "abc"})
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	engine := runner.New(server.URL)
	settings := config.Config{
		Endpoint: server.URL,
		Auth: config.Auth{
			Login:  config.Login{Method: "POST", Path: "/auth/login"},
			Carry:  "cookie",
			Cookie: "jwt_token",
			Except: []string{"open"},
		},
	}

	err := applyConfiguredRules(context.Background(), engine, settings,
		[]compile.Transaction{named("open"), named("closed")})
	if err != nil {
		t.Fatalf("applyConfiguredRules: %v", err)
	}

	if logins != 1 {
		t.Errorf("logged in %d times, want exactly once for the whole run", logins)
	}
	if want := "Cookie: jwt_token=abc"; engine.Auth.Header != want {
		t.Errorf("credential = %q, want %q", engine.Auth.Header, want)
	}
	if !engine.Auth.Except["open"] {
		t.Error("the excepted transaction was not recorded")
	}
	if engine.Auth.Except["closed"] {
		t.Error("a transaction that was not excepted was recorded as one")
	}
}

// A login that fails must stop the run. Continuing would send every transaction
// unauthenticated and report a wall of 401s, none of which names the cause.
func TestApplyConfiguredRulesFailsWhenTheLoginDoes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"wrong password"}`))
	}))
	defer server.Close()

	engine := runner.New(server.URL)
	settings := config.Config{
		Endpoint: server.URL,
		Auth: config.Auth{
			Login: config.Login{Method: "POST", Path: "/auth/login"},
			Carry: "cookie",
		},
	}

	err := applyConfiguredRules(context.Background(), engine, settings, []compile.Transaction{named("a")})
	if err == nil {
		t.Fatal("applyConfiguredRules succeeded with a rejected login")
	}
	if !strings.Contains(err.Error(), "wrong password") {
		t.Errorf("error %q does not carry what the server said", err)
	}
}

func TestApplyConfiguredRulesCarriesHeadersAndSkips(t *testing.T) {
	engine := runner.New("http://example.invalid")
	settings := config.Config{
		ConditionalHeaders: []config.HeaderRule{
			{Name: "X-Mock-Scenario", Value: "absent", Status: "404"},
		},
		Skip: []config.SkipRule{
			{Name: "flaky", Reason: "tracked in JIRA-123"},
			{Name: "renamed away", Reason: "should not match"},
		},
	}

	err := applyConfiguredRules(context.Background(), engine, settings,
		[]compile.Transaction{named("flaky"), named("other")})
	if err != nil {
		t.Fatalf("applyConfiguredRules: %v", err)
	}

	if len(engine.ConditionalHeaders) != 1 || engine.ConditionalHeaders[0].Status != "404" {
		t.Errorf("conditional headers = %+v", engine.ConditionalHeaders)
	}
	if got := engine.Skip["flaky"]; got != "tracked in JIRA-123" {
		t.Errorf("skip reason = %q, want the configured one", got)
	}
	// A rule matching nothing is warned about, not silently kept: keeping it
	// would let a renamed transaction look as though it were still covered by a
	// decision somebody made.
	if _, present := engine.Skip["renamed away"]; present {
		t.Error("a skip rule matching no transaction was kept")
	}
}

// Probing calls Send, which does not consult Runner.Skip, so fuzz filters the
// list itself. If that ever stops happening `skip` works in one command and
// quietly does nothing in the other.
func TestWithoutSkippedRemovesOnlySkipped(t *testing.T) {
	transactions := []compile.Transaction{named("a"), named("b"), named("c")}

	kept, removed := withoutSkipped(transactions, map[string]string{"b": "why"})
	if len(kept) != 2 || kept[0].Name != "a" || kept[1].Name != "c" {
		t.Errorf("kept = %v, want a and c in order", names(kept))
	}
	if len(removed) != 1 || removed[0] != "b" {
		t.Errorf("removed = %v, want [b]", removed)
	}

	// No skips means the list is handed straight back, order untouched.
	same, none := withoutSkipped(transactions, nil)
	if len(same) != 3 || len(none) != 0 {
		t.Errorf("an empty skip list changed the run: %v, %v", names(same), none)
	}
}

func names(transactions []compile.Transaction) []string {
	out := make([]string, 0, len(transactions))
	for _, transaction := range transactions {
		out = append(out, transaction.Name)
	}
	return out
}
