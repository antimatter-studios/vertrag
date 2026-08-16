package runner_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/runner"
)

// TestCredentialIsWithheldFromExceptedTransactions covers the case that makes
// `auth.except` necessary: a login endpoint documents its own 401, and that
// response cannot be produced while the request carries a valid credential.
func TestCredentialIsWithheldFromExceptedTransactions(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Get("Cookie")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	transaction := func(name, path string) compile.Transaction {
		return compile.Transaction{
			Name:    name,
			Request: compile.Request{Method: "GET", URI: path},
			Response: compile.Response{
				Status:  "200",
				Headers: []compile.Header{{Name: "Content-Type", Value: "application/json"}},
			},
		}
	}

	const excepted = "login > 401"
	transactions := []compile.Transaction{
		transaction("devices > 200", "/devices"),
		transaction(excepted, "/login-401"),
		// A second authenticated transaction after the excepted one, so that
		// withholding the credential once cannot withhold it from everything
		// that follows.
		transaction("users > 200", "/users"),
	}

	engine := runner.New(server.URL)
	engine.Auth = runner.Credential{
		Header: "Cookie: jwt_token=valid",
		Except: map[string]bool{excepted: true},
	}

	if _, err := engine.Run(context.Background(), transactions); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for path, want := range map[string]string{
		"/devices":   "jwt_token=valid",
		"/login-401": "",
		"/users":     "jwt_token=valid",
	} {
		if got := seen[path]; got != want {
			t.Errorf("%s received Cookie %q, want %q", path, got, want)
		}
	}
}

// TestCredentialLeavesExtraHeadersIntact checks that withholding the credential
// withholds only the credential: the run-wide `--header` values must still reach
// a transaction that goes out unauthenticated.
func TestCredentialLeavesExtraHeadersIntact(t *testing.T) {
	var mu sync.Mutex
	seen := map[string][2]string{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = [2]string{r.Header.Get("X-Run"), r.Header.Get("Authorization")}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	transaction := func(name, path string) compile.Transaction {
		return compile.Transaction{
			Name:    name,
			Request: compile.Request{Method: "GET", URI: path},
			Response: compile.Response{
				Status:  "200",
				Headers: []compile.Header{{Name: "Content-Type", Value: "application/json"}},
			},
		}
	}

	engine := runner.New(server.URL)
	engine.ExtraHeaders = []string{"X-Run: shared"}
	engine.Auth = runner.Credential{
		Header: "Authorization: Bearer t",
		Except: map[string]bool{"open": true},
	}

	_, err := engine.Run(context.Background(), []compile.Transaction{
		transaction("open", "/open"),
		transaction("closed", "/closed"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := seen["/open"]; got[0] != "shared" || got[1] != "" {
		t.Errorf("/open got X-Run=%q Authorization=%q, want the shared header and no credential", got[0], got[1])
	}
	if got := seen["/closed"]; got[0] != "shared" || got[1] != "Bearer t" {
		t.Errorf("/closed got X-Run=%q Authorization=%q, want both", got[0], got[1])
	}
}
