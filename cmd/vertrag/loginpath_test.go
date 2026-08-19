package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/runner"
)

// loginFixture is a server that grants a cookie, and settings naming the given
// login path — the one thing these two tests vary.
func loginFixture(t *testing.T, path string) (*httptest.Server, config.Config) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "jwt_token", Value: "abc"})
		w.WriteHeader(http.StatusOK)
	}))
	return server, config.Config{
		Endpoint: server.URL,
		Auth: config.Auth{
			Login:  config.Login{Method: "POST", Path: path},
			Carry:  "cookie",
			Cookie: "jwt_token",
		},
	}
}

// TestALoginPathMatchingNothingIsReported pins a control that was present,
// inert and silent — and it was mine.
//
// The operation that grants the credential never receives it, which the runner
// calls definitional rather than configuration. But the exclusion matches by
// exact path, so `/auth/login` written against a compiled `/api/v1/auth/login`
// leaves it doing nothing, with no way to tell from the output.
//
// What that costs is in Credential's own comment: the login request goes out
// carrying a freshly minted session, and a server may take it as "already
// authenticated" and answer a login it never performed. The probing phases then
// report that the endpoint accepted a body its schema forbids — a finding about
// the API, manufactured by the tester. A real project hit exactly that shape and
// could not explain it from its own handler, which is what sent me looking.
func TestALoginPathMatchingNothingIsReported(t *testing.T) {
	server, settings := loginFixture(t, "/auth/login") // compiled URI is /api/v1/auth/login
	defer server.Close()

	warning := captureStderr(t, func() {
		engine := runner.New(settings.Endpoint)
		if err := applyConfiguredRules(context.Background(), engine, settings, loginTransactions()); err != nil {
			t.Fatalf("applyConfiguredRules: %v", err)
		}
	})

	if !strings.Contains(warning, "auth.login.path") {
		t.Errorf("a login path matching nothing was not reported:\n%s", warning)
	}
	if !strings.Contains(warning, "/auth/login") {
		t.Errorf("the warning does not quote the path that matched nothing:\n%s", warning)
	}
}

// TestALoginPathThatMatchesSaysNothing guards the warning against becoming
// noise: it must be silent for every correctly configured suite, or it is worth
// no more than the silence it replaced.
func TestALoginPathThatMatchesSaysNothing(t *testing.T) {
	server, settings := loginFixture(t, "/api/v1/auth/login")
	defer server.Close()

	warning := captureStderr(t, func() {
		engine := runner.New(settings.Endpoint)
		if err := applyConfiguredRules(context.Background(), engine, settings, loginTransactions()); err != nil {
			t.Fatalf("applyConfiguredRules: %v", err)
		}
	})

	if strings.Contains(warning, "auth.login.path") {
		t.Errorf("a correct login path was warned about:\n%s", warning)
	}
}

func loginTransactions() []compile.Transaction {
	return []compile.Transaction{
		{Name: "login", Request: compile.Request{Method: "POST", URI: "/api/v1/auth/login"}},
		{Name: "profile", Request: compile.Request{Method: "GET", URI: "/api/v1/profile"}},
	}
}

// captureStderr collects what a function writes to stderr.
func captureStderr(t *testing.T, run func()) string {
	t.Helper()
	real := os.Stderr
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = write
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := read.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	run()
	write.Close()
	os.Stderr = real
	return <-done
}
