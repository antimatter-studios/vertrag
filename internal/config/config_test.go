package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dredd.yml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

// TestLoadsInpaceStyleConfig uses a real project's dredd.yml verbatim. Adopting
// vertrag should not mean rewriting configuration to find out whether it works.
func TestLoadsInpaceStyleConfig(t *testing.T) {
	path := write(t, `
color: true
dry-run: null
hookfiles: ./dredd-hooks.js
language: nodejs
require: null
server: "npm run test:api:hub"
server-wait: 30
init: false
custom: {}
names: false
only: []
reporter: []
output: []
header: []
sorted: false
user: null
inline-errors: false
details: false
method: []
loglevel: warning
path: []
hooks-worker-timeout: 5000
hooks-worker-connect-timeout: 1500
hooks-worker-handler-host: 127.0.0.1
hooks-worker-handler-port: 61321
config: ./dredd.yml
blueprint: './openapi.json'
endpoint: 'http://localhost:4000'
`)

	config, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if config.Blueprint != "./openapi.json" {
		t.Errorf("blueprint = %q", config.Blueprint)
	}
	if config.Endpoint != "http://localhost:4000" {
		t.Errorf("endpoint = %q", config.Endpoint)
	}
	if len(config.Hookfiles) != 1 || config.Hookfiles[0] != "./dredd-hooks.js" {
		t.Errorf("hookfiles = %v", config.Hookfiles)
	}
	if config.Language != "nodejs" {
		t.Errorf("language = %q", config.Language)
	}
	if config.ServerWait != 30*time.Second {
		t.Errorf("server-wait = %v, want 30s", config.ServerWait)
	}
	if config.HooksWorkerTimeout != 5000*time.Millisecond {
		t.Errorf("hooks-worker-timeout = %v, want 5s", config.HooksWorkerTimeout)
	}
	if config.HooksWorkerHandlerPort != 61321 {
		t.Errorf("port = %d", config.HooksWorkerHandlerPort)
	}

	// Keys written as null or empty are at their default and must not be
	// reported as unsupported — that would warn about nothing on every run.
	if len(config.Unsupported) != 0 {
		t.Errorf("unsupported = %v, want none: those keys are all empty", config.Unsupported)
	}
}

func TestReportsUnsupportedKeysThatAreActuallySet(t *testing.T) {
	config, err := Load(write(t, `
blueprint: api.yml
endpoint: http://localhost
reporter: [xunit]
output: [report.xml]
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	found := map[string]bool{}
	for _, key := range config.Unsupported {
		found[key] = true
	}
	if !found["reporter"] || !found["output"] {
		t.Errorf("unsupported = %v, want reporter and output", config.Unsupported)
	}
}

// TestHookfilesAcceptsBothShapes pins that a list and a single string both work,
// because both appear in real projects.
func TestHookfilesAcceptsBothShapes(t *testing.T) {
	single, err := Load(write(t, "hookfiles: one.js\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(single.Hookfiles) != 1 {
		t.Errorf("single hookfile = %v", single.Hookfiles)
	}

	many, err := Load(write(t, "hookfiles:\n  - one.js\n  - two.js\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(many.Hookfiles) != 2 {
		t.Errorf("list of hookfiles = %v", many.Hookfiles)
	}
}

// TestExplicitFalseOverridesDefault pins the reason every field is a pointer.
func TestExplicitFalseOverridesDefault(t *testing.T) {
	if Default().Color != true {
		t.Fatal("colour is on by default")
	}
	config, err := Load(write(t, "color: false\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if config.Color {
		t.Error("an explicit `color: false` should be honoured, not treated as absent")
	}
}

func TestValidate(t *testing.T) {
	for _, test := range []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{"complete", Config{Blueprint: "api.yml", Endpoint: "http://localhost:4000"}, false},
		{"no blueprint", Config{Endpoint: "http://localhost"}, true},
		{"no endpoint", Config{Blueprint: "api.yml"}, true},
		{"endpoint without a scheme", Config{Blueprint: "api.yml", Endpoint: "localhost:4000"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			if (err != nil) != test.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestLoadReportsMissingAndMalformedFiles(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yml")); err == nil {
		t.Error("a missing config file should be an error")
	}
	if _, err := Load(write(t, "blueprint: [unclosed\n")); err == nil {
		t.Error("malformed YAML should be an error")
	}
}
