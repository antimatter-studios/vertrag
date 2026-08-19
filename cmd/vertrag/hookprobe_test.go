package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// strictLogin answers 200 only for a body carrying both fields, which is what
// makes the hook's substitution visible: the server is behaving correctly
// throughout.
func strictLogin(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		user, _ := body["username"].(string)
		pass, _ := body["password"].(string)
		code := http.StatusUnauthorized
		if user != "" && pass != "" {
			code = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestAHookThatFillsInABodyDoesNotManufactureAFinding pins the second of two
// causes of a false security finding against a real API.
//
// Hooks run on generated requests deliberately — a hook may be the thing
// holding a dangerous field at a safe value, and that matters most while a
// generator is drawing. The other direction was not thought through. A login
// hook that fills in credentials, which is what nearly every hook file
// inherited from Dredd does, overwrites the body the generator drew. The server
// then quite correctly answers 200 to the valid credentials it was handed, and
// the probe reported that the server accepted a body its schema forbids —
// displaying the body that was never sent.
//
// Its owners escalated that as a security finding and then could not reproduce
// it, because the request they were shown was not the request that went out.
func TestAHookThatFillsInABodyDoesNotManufactureAFinding(t *testing.T) {
	binary := build(t)
	server := strictLogin(t)
	dir := hookedLoginProject(t, server.URL)

	output, _ := runIn(t, dir, binary, "run", "--no-color")

	// Matched on the finding's own sentence, not on "schema forbids" alone —
	// the explanation of why the probe was abandoned quotes that phrase too,
	// and a test that cannot tell the finding from its retraction proves
	// nothing.
	if strings.Contains(output, "the server returned") {
		t.Errorf("a hook's own body was reported as the server accepting a forbidden one:\n%s", output)
	}
	if !strings.Contains(output, "0 failing") {
		t.Errorf("the run did not come out clean:\n%s", output)
	}
}

// TestAProbeAHookEndedIsReportedRatherThanPassed is the other half, and the
// more important one. Removing the false finding must not leave silence: a
// probe that tested nothing looks exactly like a probe that found nothing, and
// only one of those means the operation is sound.
func TestAProbeAHookEndedIsReportedRatherThanPassed(t *testing.T) {
	binary := build(t)
	server := strictLogin(t)
	dir := hookedLoginProject(t, server.URL)

	output, _ := runIn(t, dir, binary, "run", "--no-color")

	if !strings.Contains(output, "replaced by a hook") {
		t.Errorf("a probe ended by a hook was not reported:\n%s", output)
	}
	if !strings.Contains(output, "tested nothing") {
		t.Errorf("the run does not count the probe that tested nothing:\n%s", output)
	}
}

func hookedLoginProject(t *testing.T, endpoint string) string {
	t.Helper()
	dir := t.TempDir()
	description := `openapi: 3.0.3
info: {title: Hooked, version: "1.0"}
paths:
  /login:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [username, password]
              properties:
                username: {type: string}
                password: {type: string}
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
`
	// The shape a Dredd-era hook file has: fill in the credentials for login.
	hook := `const hooks = require('vertrag_hooks');
hooks.beforeEach((transaction) => {
  if (transaction.name.includes('/login')) {
    transaction.request.body = JSON.stringify({username: 'admin', password: 'password'});
  }
});
`
	for name, content := range map[string]string{"api.yml": description, "hooks.js": hook} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := fmt.Sprintf("spec: ./api.yml\nendpoint: %s\nhookfiles: ./hooks.js\nlanguage: nodejs\nphases: [examples, fuzz]\nfuzz: {cases: 4, seed: 1}\n", endpoint)
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}
