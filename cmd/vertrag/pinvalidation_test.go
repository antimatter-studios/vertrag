package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const intentAPI = `openapi: 3.0.3
info: {title: Intent, version: "1.0"}
paths:
  /intent:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [target_frac, dry_run]
              properties:
                target_frac: {type: number}
                dry_run: {type: boolean}
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
`

func pinProject(t *testing.T, pin string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(intentAPI), 0o600); err != nil {
		t.Fatal(err)
	}
	config := "spec: ./api.yml\nendpoint: http://127.0.0.1:1\nfuzz:\n  pin:\n    " + pin + "\n"
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestATypodPinStopsAPlainRun pins a hole a peer found by changing one letter.
//
// The pin was validated inside the probing phases, so `vertrag run` — the
// command most people type, and the one that runs no probing phase by default —
// accepted a pin naming nothing at all: every transaction ran, no warning,
// normal exit. A configuration that reads exactly like a safety control sat
// unvalidated in the most-used entry point, which is precisely the
// present-inert-and-silent failure this tool argues against elsewhere.
func TestATypodPinStopsAPlainRun(t *testing.T) {
	binary := build(t)

	output, code := runIn(t, pinProject(t, "dry_runn: true"), binary, "run")
	if code == 0 {
		t.Errorf("a pin naming nothing was accepted by a plain run:\n%s", output)
	}
	if !strings.Contains(output, "dry_runn") {
		t.Errorf("the refusal does not name the misspelling:\n%s", output)
	}
}

// TestThePinsReachIsVisibleWithoutSendingAnything is the other half, and the
// reason it matters is an ordering one.
//
// The safety advice is "confirm the pin engaged before you point this at
// anything real" — and that was impossible to satisfy in the order it has to be
// satisfied in, because the only way to observe engagement was to fire the
// requests the pin exists to guard. A peer hit exactly that wall. --dry-run now
// reports what the pin reaches, from a cold start, with nothing sent.
func TestThePinsReachIsVisibleWithoutSendingAnything(t *testing.T) {
	binary := build(t)

	output, code := runIn(t, pinProject(t, "dry_run: true"), binary, "run", "--dry-run")
	if code != 0 {
		t.Fatalf("the dry run did not complete, exit %d:\n%s", code, output)
	}
	if !strings.Contains(output, "dry_run: 1 of 1") {
		t.Errorf("the pin's reach was not reported:\n%s", output)
	}
	// Nothing was sent: the endpoint is a closed port, so any request would
	// have produced a connection error.
	if strings.Contains(output, "connection refused") {
		t.Errorf("the dry run sent something:\n%s", output)
	}
}

// TestAProbingRunWithNothingPinnedSaysSo pins the structural answer to a
// criticism that landed on this tool sideways.
//
// The hazard — generation sending the value that makes a request real — was
// documented, and the control was opt-in. That is the same shape as the bug
// this tool keeps finding elsewhere: a safe path you have to remember is not a
// safe path. The advice was "point it at a sandbox", and advice is exactly what
// an operator can skip, forget, or never read.
func TestAProbingRunWithNothingPinnedSaysSo(t *testing.T) {
	binary := build(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(intentAPI), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"),
		[]byte("spec: ./api.yml\nendpoint: http://127.0.0.1:1\nphases: [examples, coverage]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	output, _ := runIn(t, dir, binary, "run")
	if !strings.Contains(output, "change something") {
		t.Errorf("a probing run over a POST with nothing pinned said nothing:\n%s", output)
	}
	// The state hazard is named as well as the side-effect one: a probing run
	// writes records, and a poisoned journal outlives the run that wrote it.
	if !strings.Contains(output, "journal") {
		t.Errorf("only the side-effect hazard was named, not the state one:\n%s", output)
	}
}

// TestAReadOnlyProbingRunIsNotNagged guards the warning against becoming noise.
// Silence has to mean "nothing generated reaches a mutating operation", which
// is information — a warning on every run would be the same as none.
func TestAReadOnlyProbingRunIsNotNagged(t *testing.T) {
	binary := build(t)

	dir := t.TempDir()
	readOnly := `openapi: 3.0.3
info: {title: Readonly, version: "1.0"}
paths:
  /things:
    get:
      parameters: [{name: limit, in: query, schema: {type: integer, maximum: 10}}]
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
`
	if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(readOnly), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"),
		[]byte("spec: ./api.yml\nendpoint: http://127.0.0.1:1\nphases: [examples, coverage]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	output, _ := runIn(t, dir, binary, "run")
	if strings.Contains(output, "change something") {
		t.Errorf("a read-only description was warned about mutation:\n%s", output)
	}
}
