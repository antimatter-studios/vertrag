package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vertrag.yml")
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

	if config.Spec != "./openapi.json" {
		t.Errorf("blueprint = %q", config.Spec)
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
	//
	// `server` is the exception here, and only because this fixture sets it to
	// a real command. vertrag does not start a server under test, and used to
	// accept the key and do nothing, which meant a project relying on it got a
	// run against a server that was never started and a wall of connection
	// errors that named no cause.
	if want := []string{"server"}; !slices.Equal(config.Unsupported, want) {
		t.Errorf("unsupported = %v, want %v", config.Unsupported, want)
	}
}

func TestReportsUnsupportedKeysThatAreActuallySet(t *testing.T) {
	config, err := Load(write(t, `
blueprint: api.yml
endpoint: http://localhost
require: ./setup.js
custom: {key: value}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	found := map[string]bool{}
	for _, key := range config.Unsupported {
		found[key] = true
	}
	if !found["require"] || !found["custom"] {
		t.Errorf("unsupported = %v, want require and custom", config.Unsupported)
	}
}

// TestDreddReporterKeysAreHonoured pins that a pipeline's existing Dredd
// configuration keeps working: `xunit` is what Dredd wrote where vertrag writes
// `junit`, and the reporter/output pairing is Dredd's own.
func TestDreddReporterKeysAreHonoured(t *testing.T) {
	config, err := Load(write(t, `
blueprint: api.yml
endpoint: http://localhost
reporter: [cli, xunit]
output: ["", report.xml]
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(config.Reporters) != 2 || config.Reporters[1] != "xunit" {
		t.Errorf("reporters = %v", config.Reporters)
	}
	// An empty entry means "this reporter writes to stdout", so it has to
	// survive: dropping it would shift report.xml onto the cli reporter.
	if len(config.Outputs) != 2 || config.Outputs[0] != "" || config.Outputs[1] != "report.xml" {
		t.Errorf("outputs = %v, want [\"\", report.xml]", config.Outputs)
	}
	for _, key := range config.Unsupported {
		if key == "reporter" || key == "output" {
			t.Errorf("%s is supported now and must not be reported as unsupported", key)
		}
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

// TestDiscoveryFindsOnlyVertragFiles pins the removal of the dredd.yml
// fallback. Discovery used to end with dredd.yml and dredd.yaml, which made
// adopting vertrag a no-op and made the meaning of a key depend on the name of
// the file it sat in. A project migrating now renames the file.
func TestDiscoveryFindsOnlyVertragFiles(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	if got := Discover(); got != "" {
		t.Errorf("Discover() = %q with no config present", got)
	}

	os.WriteFile("dredd.yml", []byte("blueprint: a.yml\n"), 0o600)
	if got := Discover(); got != "" {
		t.Errorf("Discover() = %q, want a dredd.yml to be passed over", got)
	}
	// Passed over, but not unnoticed: the caller refuses to run over it rather
	// than starting with no configuration while one sits in the directory.
	if got := DreddFile(); got != "dredd.yml" {
		t.Errorf("DreddFile() = %q, want dredd.yml to be seen", got)
	}

	os.WriteFile("vertrag.yml", []byte("blueprint: b.yml\n"), 0o600)
	if got := Discover(); got != "vertrag.yml" {
		t.Errorf("Discover() = %q, want vertrag.yml", got)
	}
}

// TestBothFilesPresentReadsOnlyTheVertragOne pins the case the removal must not
// break: a project running both testers keeps a file per tester, and vertrag
// reads its own without a word about the other. DreddFile is about a stranded
// file, not any file.
func TestBothFilesPresentReadsOnlyTheVertragOne(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	os.WriteFile("dredd.yml", []byte("spec: dredds.yml\nendpoint: http://dredd\n"), 0o600)
	os.WriteFile("vertrag.yml", []byte("spec: ours.yml\nendpoint: http://ours\n"), 0o600)

	settings, err := Load(Discover())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings.Spec != "ours.yml" || settings.Endpoint != "http://ours" {
		t.Errorf("read the wrong file: spec=%q endpoint=%q", settings.Spec, settings.Endpoint)
	}
	if note := strings.Join(settings.Notes, "\n"); strings.Contains(note, "dredd.yml") {
		t.Errorf("a dredd.yml alongside a vertrag.yml is simply not read, and needs no note:\n%s", note)
	}
}

// TestChecksDefaultOnAndCanBeTurnedOff pins vertrag's own section.
func TestChecksDefaultOnAndCanBeTurnedOff(t *testing.T) {
	if d := Default(); !d.Checks.ServerError || !d.Checks.ContentType {
		t.Error("the extra checks should be on by default")
	}

	config, err := Load(write(t, `
blueprint: api.yml
endpoint: http://localhost
checks:
  content-type: false
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if config.Checks.ContentType {
		t.Error("content-type check should be off")
	}
	if !config.Checks.ServerError {
		t.Error("an unmentioned check should keep its default")
	}
}

// TestHeaderSchemaCheckIsOffUntilAskedFor pins the one check that does not
// default on, and the config key that turns it on.
//
// Nothing has ever enforced a response header's schema, so a description is
// quite likely to carry one that was never true. A suite that goes red the day
// it adopts vertrag teaches people to distrust the tool rather than read the
// finding, and this check is worth more switched on deliberately than switched
// off in irritation.
func TestHeaderSchemaCheckIsOffUntilAskedFor(t *testing.T) {
	if Default().Checks.HeaderSchema {
		t.Error("the header-schema check should be off by default")
	}

	config, err := Load(write(t, `
blueprint: api.yml
endpoint: http://localhost
checks:
  header-schema: true
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !config.Checks.HeaderSchema {
		t.Error("`checks: header-schema: true` should turn the check on")
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(previous) })
}

func TestValidate(t *testing.T) {
	for _, test := range []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{"complete", Config{Spec: "api.yml", Endpoint: "http://localhost:4000"}, false},
		{"no spec", Config{Endpoint: "http://localhost"}, true},
		{"no endpoint", Config{Spec: "api.yml"}, true},
		{"endpoint without a scheme", Config{Spec: "api.yml", Endpoint: "localhost:4000"}, true},
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
