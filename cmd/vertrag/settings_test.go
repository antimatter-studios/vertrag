package main

import "testing"

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
