package main

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/corpus"
)

// The stateful phase against the corpus's chained description — create, read
// what was created, delete it — with the server minting real identifiers.
//
// Each test is a property OF THE SEQUENCE, which is the whole reason the
// phase exists: every request in these runs is individually well formed and
// individually answered as its description promises. Only the order reveals
// the fault.

// TestStatefulIsCleanAgainstAConformingServer is the soundness baseline. A
// server that really creates, really serves, and really deletes must produce
// no finding however the chain is run.
func TestStatefulIsCleanAgainstAConformingServer(t *testing.T) {
	binary := build(t)
	endpoint, description := serveStateful(t, "chained")

	// --sequence as well: against a server that mints identifiers, the
	// documented read carries the description's example id, which the create
	// never made. Ordering the examples by their links is what fixes that,
	// and it is the pairing a stateful suite actually uses.
	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color",
		"--sequence", "--phases", "examples,stateful", description)
	if code != 0 {
		t.Errorf("exit = %d, want 0 against a conforming stateful server\n%s", code, output)
	}
	if !strings.Contains(output, "chain(s) run, 0 finding(s)") {
		t.Errorf("the chain did not run clean:\n%s", output)
	}
	// A clean result only means something if a chain was actually found.
	if strings.Contains(output, "0 chain(s) run") || strings.Contains(output, "no sequence to run") {
		t.Errorf("no chain was built from a description that declares links:\n%s", output)
	}
}

// TestStatefulFindsAResourceThatWasNeverStored: the server answers 201 to
// the create — so the create itself passes — and then cannot find what it
// said it made.
//
// A sequenced run of the documented transactions ALSO fails here, because
// the read it was ordered into gets a 404, so the exit code is 1: a
// documented transaction failed. What the stateful phase adds is the
// diagnosis. "Expected status code '200', but got '404'" describes the
// symptom; "the create reported success, so following its own link must find
// what it made" names the defect, and the difference is how long someone
// spends looking in the wrong place.
func TestStatefulFindsAResourceThatWasNeverStored(t *testing.T) {
	binary := build(t)
	endpoint, description := serveStateful(t, "chained", corpus.FaultCreatedResourceMissing)

	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color",
		"--sequence", "--phases", "examples,stateful", description)
	if code != 1 {
		t.Errorf("exit = %d, want 1 — the documented read fails too\n%s", code, output)
	}
	if !strings.Contains(output, "resource availability") {
		t.Errorf("the finding is not named as a resource-availability one:\n%s", output)
	}
	if !strings.Contains(output, "just created") {
		t.Errorf("the finding does not explain what the sequence established:\n%s", output)
	}
}

// TestStatefulFindsAResourceThatOutlivedItsDelete is the case that justifies
// the whole phase: EVERY documented transaction passes — the create creates,
// the read reads, the delete answers 204 — and the resource is still there
// afterwards.
//
// No pass over the documented transactions can see it, `--sequence` included,
// because each runs exactly once and none of them reads after the delete.
// The stateful phase repeats the read, which nobody documented, and that
// repeat is the only witness. Exit 2 says precisely that: the contract held
// and the sequence did not.
func TestStatefulFindsAResourceThatOutlivedItsDelete(t *testing.T) {
	binary := build(t)
	endpoint, description := serveStateful(t, "chained", corpus.FaultResourceLingersAfterDelete)

	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color",
		"--sequence", "--phases", "examples,stateful", description)
	if code != 2 {
		t.Errorf("exit = %d, want 2 (every documented transaction passes; the sequence does not)\n%s", code, output)
	}
	if !strings.Contains(output, "use after free") {
		t.Errorf("the lingering resource was not reported:\n%s", output)
	}
	// The proof that it is unique to this phase: no documented transaction
	// failed, so the examples phase saw nothing.
	if strings.Contains(output, "FAIL: /items") {
		t.Errorf("a documented transaction failed, so this does not prove unique detection:\n%s", output)
	}
}

// TestALifecycleFindingSaysItIsInferredRatherThanDocumented pins the one
// change this phase needed.
//
// Its two assertions cannot be written in OpenAPI. There is no keyword for "a
// deleted resource stops being readable", so a reader cannot check the finding
// against their description, cannot affirm it and cannot deny it — while every
// other thing vertrag reports is a claim the document makes. This codebase
// refuses that move elsewhere on one sentence: a description promises which
// values are WELL FORMED, not which resources exist.
//
// The assertion stays, because it catches what nothing else can. What it now
// carries is the assumption it rests on, so an author who means a soft delete
// can see in one line which inference they are disputing.
func TestALifecycleFindingSaysItIsInferredRatherThanDocumented(t *testing.T) {
	binary := build(t)
	endpoint, description := serveStateful(t, "chained", corpus.FaultResourceLingersAfterDelete)

	output, _ := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color",
		"--sequence", "--phases", "examples,stateful", description)

	for _, want := range []string{"inferred, not documented", "RFC 9110"} {
		if !strings.Contains(output, want) {
			t.Errorf("a lifecycle finding does not say %q, so it reads as something the description said:\n%s",
				want, output)
		}
	}
}

// TestStatefulSaysSoWhenThereIsNothingToRun: a description with no links
// produces no chains, and says why rather than reporting a clean run — a
// phase that silently did nothing would read as a phase that found nothing.
func TestStatefulSaysSoWhenThereIsNothingToRun(t *testing.T) {
	binary := build(t)
	endpoint, description := serve(t, "widgets")

	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color",
		"--phases", "examples,stateful", description)
	if code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, output)
	}
	if !strings.Contains(output, "no sequence to run") {
		t.Errorf("a description with no links should say so:\n%s", output)
	}
}
