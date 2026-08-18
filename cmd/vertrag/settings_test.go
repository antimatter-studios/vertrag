package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestAFlagWinsOverTheConfigFile pins the one rule the settings merge follows.
//
// The file records what a project normally does and a flag says what this run
// should do instead, so a flag that was given wins. Extracting the merge out of
// runRun made it testable without a server, a description or a subprocess,
// which is most of the reason to have extracted it.
func TestAFlagWinsOverTheConfigFile(t *testing.T) {
	// No config file is discoverable from a temporary directory, so these
	// exercise the flag half against the defaults.
	t.Chdir(t.TempDir())

	settings, err := settingsFor(runFlags{
		endpoint:   "http://example.com",
		positional: []string{"api.yml"},
	})
	if err != nil {
		t.Fatalf("settingsFor: %v", err)
	}
	if settings.Endpoint != "http://example.com" {
		t.Errorf("endpoint = %q, want the flag's value", settings.Endpoint)
	}

	// The booleans are one-way: a flag turns something on for this run, and
	// there is no spelling that means "go back to whatever the file said".
	on, err := settingsFor(runFlags{
		endpoint:          "http://example.com",
		positional:        []string{"api.yml"},
		dryRun:            true,
		sorted:            true,
		details:           true,
		checkHeaderSchema: true,
	})
	if err != nil {
		t.Fatalf("settingsFor: %v", err)
	}
	for name, got := range map[string]bool{
		"DryRun":       on.DryRun,
		"Sorted":       on.Sorted,
		"Details":      on.Details,
		"HeaderSchema": on.Checks.HeaderSchema,
	} {
		if !got {
			t.Errorf("%s was not turned on by its flag", name)
		}
	}
}

// TestNamingAReporterReplacesTheConfiguredList pins that asking for one format
// gives that format, rather than it plus whatever else was configured.
func TestNamingAReporterReplacesTheConfiguredList(t *testing.T) {
	t.Chdir(t.TempDir())

	settings, err := settingsFor(runFlags{
		endpoint:     "http://example.com",
		positional:   []string{"api.yml"},
		reporterName: "junit",
		output:       "report.xml",
	})
	if err != nil {
		t.Fatalf("settingsFor: %v", err)
	}

	if len(settings.Reporters) != 1 || settings.Reporters[0] != "junit" {
		t.Errorf("reporters = %v, want exactly [junit]", settings.Reporters)
	}
	if len(settings.Outputs) != 1 || settings.Outputs[0] != "report.xml" {
		t.Errorf("outputs = %v, want exactly [report.xml]", settings.Outputs)
	}
}

// TestRepeatableFlagsAccumulate pins that --header, --only and --method add to
// what the file listed rather than replacing it. They are the flags where
// replacing would silently drop a project's own configuration.
func TestRepeatableFlagsAccumulate(t *testing.T) {
	t.Chdir(t.TempDir())

	settings, err := settingsFor(runFlags{
		endpoint:   "http://example.com",
		positional: []string{"api.yml"},
		headers:    stringList{"X-A: 1", "X-B: 2"},
		only:       stringList{"one"},
		methods:    stringList{"GET", "POST"},
	})
	if err != nil {
		t.Fatalf("settingsFor: %v", err)
	}

	if len(settings.Header) != 2 {
		t.Errorf("headers = %v, want both", settings.Header)
	}
	if len(settings.Method) != 2 {
		t.Errorf("methods = %v, want both", settings.Method)
	}
	if len(settings.Only) != 1 {
		t.Errorf("only = %v, want one", settings.Only)
	}
}

// TestAStrandedDreddFileIsRefusedNotIgnored pins the one place the removal of
// the dredd.yml fallback could have gone quietly wrong.
//
// vertrag used to discover a dredd.yml, so a project holding only that one was
// fully configured. Now it is not discovered — and the tempting implementation,
// passing over it, is the dangerous one: the run would start with no endpoint,
// no headers and no skips, most likely against whatever host the description
// happens to name, and the only evidence would be a wall of connection errors
// naming a port nobody asked for. So it refuses, and names the rename.
func TestAStrandedDreddFileIsRefusedNotIgnored(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("dredd.yml", []byte("spec: ./api.yml\nendpoint: http://configured:4210\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := settingsFor(runFlags{})
	if err == nil {
		t.Fatal("a dredd.yml and no vertrag.yml should be refused, not silently ignored")
	}
	// The message has to carry the fix, not just the complaint.
	for _, want := range []string{"dredd.yml", "vertrag.yml", "--config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// TestANamedDreddFileIsReadInFull is the other half: refusing to *discover* the
// file is not refusing to read it. Someone who points at it gets everything in
// it, including the keys that used to be gated on the name.
func TestANamedDreddFileIsReadInFull(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("dredd.yml", []byte("spec: ./api.yml\nendpoint: http://configured:4210\nworkers: 4\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	settings, err := settingsFor(runFlags{configPath: "dredd.yml"})
	if err != nil {
		t.Fatalf("settingsFor: %v", err)
	}
	if settings.Endpoint != "http://configured:4210" {
		t.Errorf("endpoint = %q, want the named file's", settings.Endpoint)
	}
	if settings.Workers != 4 {
		t.Errorf("Workers = %d, want 4 — a named file is read in full", settings.Workers)
	}
}

// TestNoConfigAtAllIsStillFine guards the refusal against overreach: an empty
// directory is not an error, it is the command line supplying everything.
func TestNoConfigAtAllIsStillFine(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := settingsFor(runFlags{positional: []string{"api.yml", "http://x"}}); err != nil {
		t.Errorf("no config file present should not be an error: %v", err)
	}
}

// TestTheResponseTimeBoundIsCarriedFromTheCommandLine follows the one `run`
// flag whose value is a duration rather than a switch, from the text somebody
// typed to the setting the engine is built from.
//
// Both halves can fail silently. A duration the flag set does not parse would
// have to be caught by flag.Parse, and a bound parsed but never merged looks
// exactly like a server that answered in time — so the transaction it should
// have reported passes, and nobody learns that the check did nothing.
func TestTheResponseTimeBoundIsCarriedFromTheCommandLine(t *testing.T) {
	t.Chdir(t.TempDir())

	flags, err := parseRunFlags([]string{"api.yml", "http://example.com", "--max-response-time", "750ms"})
	if err != nil {
		t.Fatalf("parseRunFlags: %v", err)
	}
	if flags.maxResponseTime != 750*time.Millisecond {
		t.Errorf("--max-response-time parsed as %s, want 750ms", flags.maxResponseTime)
	}

	flags.endpoint = "http://example.com"
	settings, err := settingsFor(flags)
	if err != nil {
		t.Fatalf("settingsFor: %v", err)
	}
	if settings.Checks.MaxResponseTime != 750*time.Millisecond {
		t.Errorf("bound = %s, want the flag's 750ms", settings.Checks.MaxResponseTime)
	}

	// Not given means not timed, rather than timed against zero — which would
	// report every transaction in the suite and read as vertrag being broken.
	unbounded, err := settingsFor(runFlags{endpoint: "http://example.com", positional: []string{"api.yml"}})
	if err != nil {
		t.Fatalf("settingsFor: %v", err)
	}
	if unbounded.Checks.MaxResponseTime != 0 {
		t.Errorf("bound = %s, want none without the flag", unbounded.Checks.MaxResponseTime)
	}
}
