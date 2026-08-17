package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/config"
)

// The signature must never be mistaken for a transaction line.
//
// It goes to stderr, which is not the same as hidden: the ordinary way to
// capture a run is `vertrag run 2>&1 | tee log`, and a suite then counts its own
// results out of that stream by anchoring on the method — `^pass: [A-Z]+ `.
// A line that matched one of those anchors would inflate a count with no sign
// anything was wrong, which is the failure this project keeps meeting.
func TestSignatureCannotBeReadAsATransactionLine(t *testing.T) {
	settings := config.Config{
		Spec:     "./openapi.json",
		Endpoint: "http://localhost:4000",
		Source:   "vertrag.yml",
	}

	line := signature(settings)

	for _, status := range []string{"pass", "fail", "skip", "error"} {
		anchor := regexp.MustCompile(`^` + status + `: [A-Z]+ `)
		if anchor.MatchString(line) {
			t.Errorf("the signature matches the %s anchor: %q", status, line)
		}
	}
	// Nor the summary line a reader or a script might look for.
	if regexp.MustCompile(`^\d+ total`).MatchString(line) {
		t.Errorf("the signature matches the summary anchor: %q", line)
	}
}

// What it has to carry, and why each part earns its place: the version explains
// a run whose numbers look wrong, and the endpoint and config file answer
// "which server" and "which file" — the two questions that cost this project
// the most time and which the report itself cannot answer.
func TestSignatureNamesVersionEndpointAndConfig(t *testing.T) {
	line := signature(config.Config{
		Spec:     "./openapi.json",
		Endpoint: "http://localhost:4000",
		Source:   "vertrag.yml",
	})

	for _, want := range []string{"vertrag", version, "./openapi.json", "http://localhost:4000", "vertrag.yml"} {
		if !strings.Contains(line, want) {
			t.Errorf("signature %q does not name %q", line, want)
		}
	}
}

// A run configured entirely from the command line has no config file, and the
// signature must not invent one or leave a dangling separator.
func TestSignatureWithoutAConfigFile(t *testing.T) {
	line := signature(config.Config{
		Spec:     "./openapi.json",
		Endpoint: "http://localhost:4000",
	})

	if strings.Contains(line, "vertrag.yml") {
		t.Errorf("signature named a config file that was not read: %q", line)
	}
	if strings.HasSuffix(strings.TrimSpace(line), "·") {
		t.Errorf("signature ends in a dangling separator: %q", line)
	}
	if !strings.Contains(line, "http://localhost:4000") {
		t.Errorf("signature dropped the endpoint: %q", line)
	}
}

// Defaults only: no description, no endpoint, no file. Still names the version,
// because that is the part worth having when nothing else is known.
func TestSignatureWithNothingConfigured(t *testing.T) {
	line := signature(config.Config{})

	if !strings.Contains(line, version) {
		t.Errorf("signature %q does not name the version", line)
	}
	if strings.Contains(line, "→") {
		t.Errorf("signature shows an arrow with nothing on either side: %q", line)
	}
}
