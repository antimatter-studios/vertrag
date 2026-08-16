package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/config"
)

func writeConfig(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

func TestAuthFromVertragFile(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
blueprint: ./api.yml
endpoint: http://localhost:4210
auth:
  login:
    path: /auth/login
    body: {username: admin, password: secret}
  cookie: jwt_token
  except:
    - 'login > 401'
`)

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !settings.Auth.Configured() {
		t.Fatal("Configured() = false, want true")
	}
	if settings.Auth.Login.Path != "/auth/login" {
		t.Errorf("login path = %q", settings.Auth.Login.Path)
	}
	// A login is a POST unless it says otherwise.
	if settings.Auth.Login.Method != "POST" {
		t.Errorf("login method = %q, want POST by default", settings.Auth.Login.Method)
	}
	// Naming a cookie is saying the credential is carried as one.
	if settings.Auth.Carry != "cookie" {
		t.Errorf("carry = %q, want cookie inferred from the cookie name", settings.Auth.Carry)
	}
	if settings.Auth.Login.Body["username"] != "admin" {
		t.Errorf("login body = %v", settings.Auth.Login.Body)
	}
	if len(settings.Auth.Except) != 1 || settings.Auth.Except[0] != "login > 401" {
		t.Errorf("except = %v", settings.Auth.Except)
	}
}

// The boundary that keeps two testers honest: vertrag's own keys are read only
// from a vertrag file. Dredd ignores keys it does not know without a word, so
// honouring `auth` from a dredd.yml would have vertrag authenticated and Dredd
// not, testing different things from one file that looks shared.
func TestAuthIsIgnoredInADreddFile(t *testing.T) {
	path := writeConfig(t, "dredd.yml", `
blueprint: ./api.yml
endpoint: http://localhost:4210
auth:
  login: {path: /auth/login}
  cookie: jwt_token
`)

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if settings.Auth.Configured() {
		t.Error("auth was honoured from a dredd.yml")
	}
	if len(settings.Notes) == 0 {
		t.Fatal("no note explaining why auth was ignored")
	}
	note := strings.Join(settings.Notes, "\n")
	for _, want := range []string{"auth", "dredd.yml", "vertrag.yml"} {
		if !strings.Contains(note, want) {
			t.Errorf("note does not mention %q: %s", want, note)
		}
	}
	// "not supported yet" would be false — it is supported, from the other file.
	if len(settings.Unsupported) != 0 {
		t.Errorf("auth was reported as unsupported: %v", settings.Unsupported)
	}
}

func TestAuthAbsentIsNotConfigured(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", "blueprint: ./api.yml\nendpoint: http://x\n")

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings.Auth.Configured() {
		t.Error("Configured() = true with no auth block")
	}
	if len(settings.Notes) != 0 {
		t.Errorf("unexpected notes: %v", settings.Notes)
	}
}

func TestAuthStaticHeaderNeedsNoLogin(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
blueprint: ./api.yml
endpoint: http://x
auth:
  header: 'X-API-Key: abc123'
`)

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !settings.Auth.Configured() {
		t.Error("a static header alone did not count as configured")
	}
	if settings.Auth.Login.Method != "" {
		t.Errorf("a login method was invented for a static credential: %q", settings.Auth.Login.Method)
	}
}
