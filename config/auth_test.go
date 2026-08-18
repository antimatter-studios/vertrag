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
spec: ./api.yml
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

// `auth` is read from whatever file it was found in — see
// TestTagIsHonouredWhateverTheFileIsCalled for why it used to be refused from
// this one, and why that gate went with the dredd.yml fallback.
//
// This is the key the gate existed for: authenticating vertrag's run and not
// Dredd's was the concrete way two testers could disagree about what they
// tested. It is safe now because vertrag never picks this file up on its own.
func TestAuthIsHonouredWhateverTheFileIsCalled(t *testing.T) {
	path := writeConfig(t, "dredd.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
auth:
  login: {path: /auth/login}
  cookie: jwt_token
`)

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !settings.Auth.Configured() {
		t.Error("auth was not honoured from a file named dredd.yml")
	}
	if settings.Auth.Login.Path != "/auth/login" || settings.Auth.Cookie != "jwt_token" {
		t.Errorf("Auth = %+v", settings.Auth)
	}
	if len(settings.Unsupported) != 0 {
		t.Errorf("auth was reported as unsupported: %v", settings.Unsupported)
	}
}

func TestAuthAbsentIsNotConfigured(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", "spec: ./api.yml\nendpoint: http://x\n")

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

// `blueprint` was the former name for `spec`. It still works — no existing
// config should break over a rename — but it is no longer documented, and a run
// says so once so the rename can actually finish rather than lingering forever.
func TestBlueprintIsStillReadAndSaysSo(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", "blueprint: ./api.yml\nendpoint: http://x\n")

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings.Spec != "./api.yml" {
		t.Errorf("spec = %q, want the value blueprint gave", settings.Spec)
	}
	if len(settings.Notes) != 1 || !strings.Contains(settings.Notes[0], "`spec`") {
		t.Errorf("notes = %v, want one naming the new key", settings.Notes)
	}
}

// A file carrying both is the state a half-finished rename leaves behind. `spec`
// wins, and which one won is said rather than left to be found by experiment.
func TestSpecWinsOverBlueprint(t *testing.T) {
	path := writeConfig(t, "vertrag.yml",
		"spec: ./new.yml\nblueprint: ./old.yml\nendpoint: http://x\n")

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings.Spec != "./new.yml" {
		t.Errorf("spec = %q, want ./new.yml to win", settings.Spec)
	}
	if len(settings.Notes) != 1 {
		t.Fatalf("notes = %v, want exactly one", settings.Notes)
	}
	for _, want := range []string{"both", "./new.yml"} {
		if !strings.Contains(settings.Notes[0], want) {
			t.Errorf("note does not mention %q: %s", want, settings.Notes[0])
		}
	}
}

// The order the two keys appear in must not decide the winner: YAML is a
// mapping, and a reader would not expect line order to matter.
func TestSpecWinsWhateverTheOrder(t *testing.T) {
	path := writeConfig(t, "vertrag.yml",
		"blueprint: ./old.yml\nspec: ./new.yml\nendpoint: http://x\n")

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings.Spec != "./new.yml" {
		t.Errorf("spec = %q, want ./new.yml to win regardless of order", settings.Spec)
	}
}

func TestAuthStaticHeaderNeedsNoLogin(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
spec: ./api.yml
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
