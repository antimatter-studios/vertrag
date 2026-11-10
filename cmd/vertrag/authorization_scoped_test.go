package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
)

func TestScopedAuthorizationRefusal(t *testing.T) {
	for _, test := range []struct {
		name, status, body string
		skip               bool
		want               runner.Status
	}{
		{"correct refusal", "403", `{"error":{"code":"two_factor_required"}}`, false, runner.StatusPass},
		{"bypass", "200", `{"messages":[]}`, false, runner.StatusFail},
		{"wrong status despite global refuse", "404", `{"error":{"code":"two_factor_required"}}`, false, runner.StatusFail},
		{"wrong code", "403", `{"error":{"code":"invalid_token"}}`, false, runner.StatusFail},
		{"missing code", "403", `{}`, false, runner.StatusFail},
		{"invalid JSON", "403", `no`, false, runner.StatusFail},
		{"unsent", "403", `{"error":{"code":"two_factor_required"}}`, true, runner.StatusFail},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(&authzAPI{override: map[string][2]string{"/mail reader-token": {test.status, test.body}}})
			defer server.Close()
			path := filepath.Join(t.TempDir(), "vertrag.yml")
			body := "spec: api.yaml\nendpoint: " + server.URL + "\nprincipals:\n  - name: anonymous\n  - name: reader\n    auth: {header: 'Authorization: Bearer reader-token'}\n    roles: [mail:read]\n  - name: admin\n    auth: {header: 'Authorization: Bearer admin-token'}\n    roles: [mail:read, admin]\nauthorization:\n  refuse: [404]\n  refusals:\n    - operation: readMail\n      principal: reader\n      status: 403\n      body: {error: {code: two_factor_required}}\n"
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			settings, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			transactions := authzTransactions()
			transactions[0].OperationID = "readMail"
			skips := map[string]string{}
			if test.skip {
				skips[transactions[0].Name] = "test safety skip"
			}
			plan, principals, err := planAuthorizationPhase(settings, transactions, skips)
			if err != nil {
				t.Fatal(err)
			}
			engine := runner.New(server.URL)
			credentials, unsupported, err := authorizationCredentials(context.Background(), engine.Client, server.URL, settings.Principals, principals)
			if err != nil {
				t.Fatal(err)
			}
			outcomes := executeAuthorization(context.Background(), engine, plan, credentials, unsupported)
			if test.skip {
				matrix := authorizationMatrix(plan, outcomes)
				for _, cell := range matrix.Cells {
					if cell.Principal == "reader" && cell.Operation == "GET /mail" && cell.Verdict != reporter.AuthorizationFail {
						t.Errorf("unsent scoped matrix cell must fail the run, got %s", cell.Verdict)
					}
				}
			}
			scoped := 0
			for _, outcome := range outcomes {
				if outcome.Cell.Transaction.OperationID != "readMail" {
					if outcome.Cell.Refusal != nil {
						t.Errorf("scope leaked to operation %s", outcome.Cell.Operation)
					}
					continue
				}
				if outcome.Cell.Principal == "reader" {
					scoped++
					if outcome.Result.Status != test.want {
						t.Errorf("scoped cell: got %s, want %s: %v", outcome.Result.Status, test.want, outcome.Result.Errors)
					}
					if !test.skip && outcome.Result.Request.Headers["Authorization"] != "Bearer reader-token" {
						t.Errorf("credential changed: %+v", outcome.Result.Request.Headers)
					}
					if outcome.Bypass != (test.name == "bypass") {
						t.Errorf("bypass marker = %v", outcome.Bypass)
					}
				} else if !test.skip && outcome.Result.Status != runner.StatusPass {
					t.Errorf("scope leaked to %s: %+v", outcome.Cell.Principal, outcome.Result)
				}
			}
			if scoped != 1 {
				t.Fatalf("tested %d scoped cells, want exactly one", scoped)
			}
		})
	}
}

func TestScopedAuthorizationRefusalRejectsInvalidScope(t *testing.T) {
	transactions := authzTransactions()
	transactions[0].OperationID = "readMail"
	refusal := config.AuthorizationRefusal{Operation: "readMail", Principal: "reader", Status: 403, Body: map[string]any{"error": "denied"}}
	for _, test := range []struct {
		name     string
		refusals []config.AuthorizationRefusal
	}{
		{"unknown operation", []config.AuthorizationRefusal{{Operation: "typo", Principal: "reader", Status: 403, Body: refusal.Body}}},
		{"unknown principal", []config.AuthorizationRefusal{{Operation: "readMail", Principal: "typo", Status: 403, Body: refusal.Body}}},
		{"duplicate", []config.AuthorizationRefusal{refusal, refusal}},
		{"success status", []config.AuthorizationRefusal{{Operation: "readMail", Principal: "reader", Status: 200, Body: refusal.Body}}},
		{"missing body", []config.AuthorizationRefusal{{Operation: "readMail", Principal: "reader", Status: 403}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := config.Config{Principals: authzPrincipals("http://unused"), Authorization: config.AuthorizationSettings{Refusals: test.refusals}}
			if _, _, err := planAuthorizationPhase(settings, transactions, nil); err == nil {
				t.Fatal("invalid scoped expectation accepted")
			}
		})
	}
}

func TestScopedAuthorizationRefusalCannotSkipOrChangeCredential(t *testing.T) {
	transactions := authzTransactions()
	transactions[0].OperationID = "readMail"
	settings := config.Config{Principals: authzPrincipals("http://unused"), Authorization: config.AuthorizationSettings{Refusals: []config.AuthorizationRefusal{{
		Operation: "readMail", Principal: "reader", Status: 403, Body: map[string]any{"error": "denied"},
	}}}}
	plan, _, err := planAuthorizationPhase(settings, transactions, nil)
	if err != nil {
		t.Fatal(err)
	}
	var cell authorizationCell
	for _, candidate := range plan.Cells {
		if candidate.Refusal != nil {
			cell = candidate
		}
	}
	for _, mode := range []string{"skip", "credential", "cancelled", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(&authzAPI{})
			defer server.Close()
			engine := runner.New(server.URL)
			engine.Hooks = authzHooks{before: func(transaction *runner.Transaction) {
				if mode == "skip" {
					transaction.Skip = true
				}
				if mode == "credential" {
					transaction.Request.Headers["Authorization"] = "Bearer admin-token"
				}
			}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			unsupported := ""
			if mode == "unsupported" {
				unsupported = "unexpected carrier"
			}
			outcome := executeCell(ctx, engine, plan, cell, map[string]runner.Credential{"reader": {Header: "Authorization: Bearer reader-token"}}, unsupported)
			if outcome.Result.Status != runner.StatusFail || outcome.Unsent == "" {
				t.Fatalf("unsent scoped cell was not a failure: %+v", outcome)
			}
		})
	}
}
