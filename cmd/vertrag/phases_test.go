package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/corpus"
)

// The phases of a run and the three exit codes they produce, pinned against
// the corpus: a conforming server, one with a contract fault, and one with an
// input-handling fault only a probing phase can reach.
//
// The exit codes are the whole point of keeping the phases in one command:
// 0 all clear; 1 a documented transaction failed (a regression, block the
// merge); 2 the documented transactions passed but a probing phase found
// something (a bug, file it). A pipeline that could not tell 1 from 2 would
// treat both as the first — or learn to ignore both.

// TestDefaultPhasesAreExamplesOnly pins that a run without `phases` is every
// run before phases existed: no probes sent, no probe results in the report.
func TestDefaultPhasesAreExamplesOnly(t *testing.T) {
	binary := build(t)
	endpoint, description := serve(t, "widgets", corpus.FaultAcceptsAnyParameter)

	// The fault is one only probing finds; a default run must not see it.
	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color", description)
	if code != 0 {
		t.Errorf("exit = %d, want 0 — a default run sends no probes\n%s", code, output)
	}
	if strings.Contains(output, "coverage:") || strings.Contains(output, "fuzz:") {
		t.Errorf("a default run reported probe results:\n%s", output)
	}
}

// TestCoveragePhaseFindsWhatExamplesCannotAndExitsTwo: with coverage on, the
// same fault is found, reported in the SAME report as the examples, and the
// exit code says "findings, not regression".
func TestCoveragePhaseFindsWhatExamplesCannotAndExitsTwo(t *testing.T) {
	binary := build(t)
	endpoint, description := serve(t, "widgets", corpus.FaultAcceptsAnyParameter)

	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color",
		"--phases", "examples,coverage", description)
	if code != 2 {
		t.Errorf("exit = %d, want 2 (examples passed, coverage found something)\n%s", code, output)
	}
	if !strings.Contains(output, "coverage:") {
		t.Errorf("coverage results are not in the report:\n%s", output)
	}
	// Both classes in one report: documented transactions still listed.
	if !strings.Contains(output, "pass: ") {
		t.Errorf("the documented transactions vanished from the report:\n%s", output)
	}
}

// TestAContractFailureOutranksFindings: exit 1 when a documented transaction
// fails, even if probing phases also found things — the regression is the
// headline.
func TestAContractFailureOutranksFindings(t *testing.T) {
	binary := build(t)
	endpoint, description := serve(t, "widgets", corpus.FaultWrongStatus, corpus.FaultAcceptsAnyParameter)

	_, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color",
		"--phases", "examples,coverage", description)
	if code != 1 {
		t.Errorf("exit = %d, want 1 — a contract failure outranks findings", code)
	}
}

// TestPhasesFromTheConfigFile: `phases:` and a pinned `fuzz:` block in
// vertrag.yml, honoured; the seed is echoed so the run replays.
func TestPhasesFromTheConfigFile(t *testing.T) {
	binary := build(t)
	endpoint, description := serve(t, "widgets")

	dir := t.TempDir()
	cfg := filepath.Join(dir, "vertrag.yml")
	os.WriteFile(cfg, []byte("spec: "+description+"\nendpoint: "+endpoint+"\nphases: [examples, fuzz]\nfuzz:\n  seed: 42\n  cases: 5\n"), 0o600)

	output, code := runCommand(t, binary, "run", "--config", cfg, "--no-color")
	if code != 0 {
		t.Errorf("exit = %d, want 0 against a conforming server\n%s", code, output)
	}
	if !strings.Contains(output, "seed: 42") {
		t.Errorf("the pinned seed is not echoed:\n%s", output)
	}
	if !strings.Contains(output, "fuzz:") {
		t.Errorf("fuzz results are not in the report:\n%s", output)
	}
}

// TestUnknownPhaseIsRefused: a typo stops the run rather than silently
// running only examples — which would look exactly like success.
func TestUnknownPhaseIsRefused(t *testing.T) {
	binary := build(t)
	endpoint, description := serve(t, "widgets")
	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--phases", "examples,fuzzing", description)
	if code == 0 || !strings.Contains(output, "unknown phase") {
		t.Errorf("a misspelt phase should be refused by name; exit=%d\n%s", code, output)
	}
}
