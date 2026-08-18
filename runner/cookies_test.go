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

// cookieSeenBy runs one transaction against a server that records the Cookie
// header it received, and returns that header.
func cookieSeenBy(t *testing.T, transaction compile.Transaction, configure func(*runner.Runner)) string {
	t.Helper()

	var mu sync.Mutex
	var seen string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Header.Get("Cookie")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	engine := runner.New(server.URL)
	configure(engine)

	if _, err := engine.Run(context.Background(), []compile.Transaction{transaction}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	return seen
}

// cookieTransaction is a transaction whose description declared cookie
// parameters, which reach the compiled request as one Cookie header.
func cookieTransaction(cookies string) compile.Transaction {
	return compile.Transaction{
		Name: "session > 200",
		Request: compile.Request{
			Method: "GET", URI: "/session",
			Headers: []compile.Header{{Name: "Cookie", Value: cookies}},
		},
		Response: compile.Response{
			Status:  "200",
			Headers: []compile.Header{{Name: "Content-Type", Value: "application/json"}},
		},
	}
}

// TestACookieParameterDoesNotClobberTheSessionCredential is the interaction
// that makes cookie parameters worth having at all. The run has logged in and
// carries a session cookie; a description that also declares a cookie
// parameter must not cost it that session, and must not lose its own cookie
// either.
func TestACookieParameterDoesNotClobberTheSessionCredential(t *testing.T) {
	got := cookieSeenBy(t, cookieTransaction("locale=en-GB"), func(engine *runner.Runner) {
		engine.Auth = runner.Credential{Header: "Cookie: jwt_token=valid"}
	})

	if want := "locale=en-GB; jwt_token=valid"; got != want {
		t.Errorf("Cookie = %q, want %q", got, want)
	}
}

// TestTheSessionCredentialWinsACookieNameCollision pins the tie-break. The
// description's value is an example; the credential is the live session the
// rest of the run depends on, so the credential is the one that goes out.
func TestTheSessionCredentialWinsACookieNameCollision(t *testing.T) {
	got := cookieSeenBy(t, cookieTransaction("jwt_token=from-the-document; locale=en-GB"),
		func(engine *runner.Runner) {
			engine.Auth = runner.Credential{Header: "Cookie: jwt_token=valid"}
		})

	if want := "jwt_token=valid; locale=en-GB"; got != want {
		t.Errorf("Cookie = %q, want %q", got, want)
	}
}

// TestAnExplicitCookieHeaderMergesWithTheDocumentsCookies covers `--header
// 'Cookie: …'`, which is how a run supplies a cookie the description does not
// mention. Replacing the whole header would silently drop the documented
// parameters.
func TestAnExplicitCookieHeaderMergesWithTheDocumentsCookies(t *testing.T) {
	got := cookieSeenBy(t, cookieTransaction("locale=en-GB"), func(engine *runner.Runner) {
		engine.ExtraHeaders = []string{"Cookie: tenant=acme"}
	})

	if want := "locale=en-GB; tenant=acme"; got != want {
		t.Errorf("Cookie = %q, want %q", got, want)
	}
}

// TestARangedTransactionPassesOnAnyStatusInItsBand is the range's whole point
// seen from the run: a `2XX` transaction used to expect exactly 200, so a
// server answering the 201 its document permits was reported as wrong.
func TestARangedTransactionPassesOnAnyStatusInItsBand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	transaction := compile.Transaction{
		Name:    "things > 2XX",
		Request: compile.Request{Method: "GET", URI: "/things"},
		Response: compile.Response{
			Status:  "2XX",
			Headers: []compile.Header{{Name: "Content-Type", Value: "application/json"}},
		},
	}

	engine := runner.New(server.URL)
	engine.Checks = runner.Checks{ServerError: true, ContentType: true}
	results, err := engine.Run(context.Background(), []compile.Transaction{transaction})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if results[0].Status != runner.StatusPass {
		t.Errorf("status = %s, errors = %v, beyond = %v",
			results[0].Status, results[0].Errors, results[0].Beyond)
	}
}

// TestARangedTransactionFailsOutsideItsBand is the other half: a band that
// matched everything would be a test that asks nothing.
func TestARangedTransactionFailsOutsideItsBand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	transaction := compile.Transaction{
		Name:     "things > 2XX",
		Request:  compile.Request{Method: "GET", URI: "/things"},
		Response: compile.Response{Status: "2XX"},
	}

	engine := runner.New(server.URL)
	results, err := engine.Run(context.Background(), []compile.Transaction{transaction})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if results[0].Status != runner.StatusFail {
		t.Errorf("status = %s, want fail: a 404 is not in the 2XX band", results[0].Status)
	}
}
