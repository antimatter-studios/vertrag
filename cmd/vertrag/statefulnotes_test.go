package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestADanglingLinkIsReportedByTheStatefulPhase pins a gap found while briefing
// a project that had just started emitting Link Objects from a generator.
//
// `--sequence` printed the plan's notes and the stateful phase did not, so a
// link naming an operation the description never defines was named in one mode
// and silent in the other. That silence is the expensive kind: a chain that did
// not form looks exactly like a description with no chains in it — the phase
// says "nothing to sequence" and the reason sits in a slice nobody reads.
//
// The project in question had 77 operations and, until that day, not one
// operationId. Every link they declared would have dangled.
func TestADanglingLinkIsReportedByTheStatefulPhase(t *testing.T) {
	binary := build(t)

	dir := t.TempDir()
	description := `openapi: 3.0.3
info: {title: Dangling, version: "1.0"}
paths:
  /things:
    post:
      operationId: createThing
      responses:
        "201":
          description: made
          content: {application/json: {schema: {type: object}}}
          links:
            Read:
              operationId: readThingThatDoesNotExist
              parameters: {id: "$response.body#/id"}
`
	if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(description), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"),
		[]byte("spec: ./api.yml\nendpoint: http://127.0.0.1:1\nphases: [examples, stateful]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	output, _ := runIn(t, dir, binary, "run")

	// The dangling target must be named, and named specifically enough to fix.
	for _, want := range []string{"readThingThatDoesNotExist", "does not define"} {
		if !strings.Contains(output, want) {
			t.Errorf("a dangling link was not reported; the run does not mention %q:\n%s", want, output)
		}
	}
}

// TestADescriptionWithNoLinksAtAllStillSaysSo guards the fix against
// over-reaching: the original message is correct when it is true, and replacing
// it everywhere would trade one wrong answer for another.
func TestADescriptionWithNoLinksAtAllStillSaysSo(t *testing.T) {
	binary := build(t)

	dir := t.TempDir()
	description := `openapi: 3.0.3
info: {title: Plain, version: "1.0"}
paths:
  /things:
    get:
      operationId: listThings
      responses:
        "200":
          description: ok
          content: {application/json: {schema: {type: object}}}
`
	if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(description), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"),
		[]byte("spec: ./api.yml\nendpoint: http://127.0.0.1:1\nphases: [examples, stateful]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	output, _ := runIn(t, dir, binary, "run")
	if !strings.Contains(output, "No operation links to another") {
		t.Errorf("a description that really has no links should say so:\n%s", output)
	}
}
