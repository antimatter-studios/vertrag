package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/runner"
)

// accountAPI serves each tenant's own account at /accounts/{id}/notes: tenant
// a is account 11, tenant b account 12, and another tenant's account is not
// found. No operation makes an account, so only the tenants' declared ids can
// address one (#110). brokenOwner forgets the check.
type accountAPI struct{ brokenOwner bool }

var accountOfTenant = map[string]string{"a": "11", "b": "12"}

func (a *accountAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	caller, known := tenantTokens[token]
	w.Header().Set("Content-Type", "application/json")
	if !known {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{}`)
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/accounts/"), "/notes")
	if id != accountOfTenant[caller.tenant] && !a.brokenOwner {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"no such account"}`)
		return
	}
	fmt.Fprint(w, `{"notes":[]}`)
}

func accountNotes() compile.Transaction {
	notes := transactionFor("GET", "/accounts/{id}/notes", "200", requires(compile.Requirement{need(bearerScheme, "items:read")}))
	notes.OperationID, notes.Request.URI = "listNotes", "/accounts/1/notes"
	notes.Request.Parameters = []compile.Parameter{{In: compile.InPath, Name: "id", Value: "1", HasValue: true}}
	return notes
}

func runAccountTenants(t *testing.T, api *accountAPI, ids map[string]map[string]string) []authorizationOutcome {
	t.Helper()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	settings := tenantSettings()
	settings.TenantIDs = ids
	plan, err := planOwnership(settings, []compile.Transaction{accountNotes()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	engine := runner.New(server.URL)
	credentials, unsupported, err := authorizationCredentials(context.Background(), engine.Client, server.URL,
		settings.Principals, plan.Members)
	if err != nil {
		t.Fatal(err)
	}
	return executeOwnership(context.Background(), engine, plan, credentials, unsupported)
}

func TestOwnershipSendsAnotherTenantsDeclaredIDs(t *testing.T) {
	declared := map[string]map[string]string{"a": {"path.id": "11"}, "b": {"id": "12"}}

	outcomes := runAccountTenants(t, &accountAPI{}, declared)
	want := []string{
		"GET /accounts/{id}/notes admin-b → a pass 404", "GET /accounts/{id}/notes user-b → a pass 404",
		"GET /accounts/{id}/notes admin-a → b pass 404", "GET /accounts/{id}/notes user-a → b pass 404",
	}
	if got := ownershipLines(outcomes); !slices.Equal(got, want) {
		t.Fatalf("cells:\n  got  %q\n  want %q", got, want)
	}
	for _, outcome := range outcomes {
		victim := strings.TrimSpace(outcome.Cell.Principal[strings.LastIndex(outcome.Cell.Principal, "→")+len("→"):])
		if want := "/accounts/" + accountOfTenant[victim] + "/notes"; !strings.HasSuffix(outcome.Result.Request.URL, want) {
			t.Errorf("%s sent %s, want the other tenant's account %s", outcome.Cell.Principal, outcome.Result.Request.URL, want)
		}
	}

	broken := runAccountTenants(t, &accountAPI{brokenOwner: true}, declared)
	for _, outcome := range broken {
		if !outcome.Bypass || outcome.Result.Status != runner.StatusFail {
			t.Errorf("%s: bypass %v status %s, want an ownership bypass", outcome.Cell.Principal, outcome.Bypass, outcome.Result.Status)
		}
	}

	// Without declared ids nothing names the account, and the cells stay unsent.
	for _, outcome := range runAccountTenants(t, &accountAPI{}, nil) {
		if outcome.Unsent == "" || !strings.Contains(outcome.Unsent, "declares no ids") {
			t.Errorf("%s: unsent %q", outcome.Cell.Principal, outcome.Unsent)
		}
	}
}
