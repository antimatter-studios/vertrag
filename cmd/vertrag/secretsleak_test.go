package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestARecordedRunNeverCarriesTheCredentialItWasGiven drives the real thing:
// a configured login, a real exchange, a VCR cassette written to disk.
//
// The unit tests pin the redaction; this pins the WIRING, which is the half
// that silently does nothing if the credential is registered after the report
// is built, or in a code path the run does not take. A cassette is written to
// be committed, so this is the file where a leak would travel furthest.
func TestARecordedRunNeverCarriesTheCredentialItWasGiven(t *testing.T) {
	binary := build(t)

	const password = "hunter2-correct-horse-battery"
	const token = "eyJhbGciOiJIUzI1NiJ9.the-session-token"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/auth/login" {
			// The login response hands back the token in its body — which is
			// exactly where header redaction cannot reach it.
			_, _ = fmt.Fprintf(w, `{"token":%q}`, token)
			return
		}
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	description := `openapi: 3.0.3
info: {title: Secrets, version: "1.0"}
paths:
  /auth/login:
    post:
      operationId: logIn
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
  /things:
    get:
      operationId: listThings
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
`
	if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(description), 0o600); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`spec: ./api.yml
endpoint: %s
auth:
  login:
    path: /auth/login
    body: {username: admin, password: %s}
  carry: bearer
reporter: [vcr]
output: [cassette.yml]
`, server.URL, password)
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, code := runIn(t, dir, binary, "run"); code > 1 {
		t.Fatalf("the run did not complete, exit %d", code)
	}

	recorded, err := os.ReadFile(filepath.Join(dir, "cassette.yml"))
	if err != nil {
		t.Fatalf("no cassette was written: %v", err)
	}
	if len(recorded) == 0 {
		t.Fatal("the cassette is empty, so it proves nothing")
	}

	for what, secret := range map[string]string{"the posted password": password, "the returned token": token} {
		if strings.Contains(string(recorded), secret) {
			t.Errorf("%s was written into the cassette", what)
		}
	}
	// It is still a usable recording, not a redacted blank.
	if !strings.Contains(string(recorded), "http_interactions") {
		t.Errorf("the cassette lost its shape:\n%s", recorded)
	}
}

// TestTheCredentialIsRegisteredBeforeTheLoginRequestIsMade guards the ordering.
// Registering after auth.Obtain would leave the login exchange itself — the one
// exchange carrying the credential in its body both ways — unprotected.
func TestTheCredentialIsRegisteredBeforeTheLoginRequestIsMade(t *testing.T) {
	source, err := os.ReadFile("rules.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	register := strings.Index(text, "reporter.RegisterSecretsIn(settings.Auth.Login.Body)")
	obtain := strings.Index(text, "auth.Obtain(")
	if register < 0 || obtain < 0 {
		t.Fatal("the registration or the login call has moved; this guard needs updating")
	}
	if register > obtain {
		t.Error("the credential is registered after the login request is made, so the login exchange itself is unprotected")
	}
	// And the returned credential is registered too. Matched on the argument
	// rather than the function name, so renaming the registrar does not break
	// a guard that is about ordering.
	if !strings.Contains(text, "(credential)") {
		t.Error("the credential returned by the login exchange is not registered")
	}
}
