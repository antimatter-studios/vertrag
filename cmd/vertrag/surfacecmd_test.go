package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// surfaceDescription has one operation for each way an operation can go
// unreached, and one it excludes itself.
const surfaceDescription = `openapi: 3.0.3
info: {title: Surface, version: "1.0"}
paths:
  /widgets:
    get:
      summary: List widgets
      operationId: listWidgets
      parameters:
        - {name: limit, in: query, schema: {type: integer, maximum: 10}, example: 5}
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
  /widgets/{id}:
    parameters:
      - {name: id, in: path, required: true, schema: {type: integer}, example: 1}
    get:
      summary: Fetch a widget
      operationId: fetchWidget
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
        "404": {description: missing, content: {application/json: {schema: {type: object}}}}
    delete:
      summary: Delete a widget
      operationId: deleteWidget
      responses:
        "204": {description: gone}
  /admin/reindex:
    post:
      summary: Reindex
      operationId: reindex
      tags: [admin]
      responses:
        "202": {description: started}
  /billing/charge:
    post:
      summary: Charge
      operationId: charge
      x-vertrag-exclude: "charges a real card"
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
  /reports:
    get:
      summary: List reports
      operationId: listReports
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
`

// surfaceSettings leave two operations unreached: one skipped, one filtered.
// The 404 is skipped too, so the examples phase passes and the only thing that
// can fail the run is the gate.
const surfaceSettings = `exclude-tag: [admin]
skip:
  - name: "/widgets/{id} > Delete a widget > 204"
    reason: destroys shared state
  - name: "/widgets/{id} > Fetch a widget > 404 > application/json"
    reason: the example id always exists
`

// lenient answers everything as documented-looking success, including the
// values a coverage probe sends to be refused — which is what makes the
// probing phase find something.
func lenient(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/admin/reindex":
			w.WriteHeader(http.StatusAccepted)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func surfaceProject(t *testing.T, endpoint, extra string) string {
	t.Helper()
	dir := t.TempDir()
	settings := fmt.Sprintf("spec: ./api.yml\nendpoint: %s\n%s%s", endpoint, surfaceSettings, extra)
	for name, content := range map[string]string{"api.yml": surfaceDescription, "vertrag.yml": settings} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// surfaceSection is the part of a run's output the surface report wrote.
func surfaceSection(output string) string {
	if at := strings.Index(output, "\nAPI surface"); at >= 0 {
		return output[at:]
	}
	return ""
}

func requireAll(t *testing.T, output string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(output, want) {
			t.Errorf("the output does not carry %q:\n%s", want, output)
		}
	}
}

// TestTheSurfaceGateFailsARunThatMissesAnOperationAndNamesIt is the issue's
// second condition, at the command line rather than at the function behind
// it: every documented transaction that was sent passes, and the run still
// fails, because it left two operations untested — and it says which, and why.
func TestTheSurfaceGateFailsARunThatMissesAnOperationAndNamesIt(t *testing.T) {
	binary := build(t)
	dir := surfaceProject(t, lenient(t), "")

	output, code := runIn(t, dir, binary, "run", "--no-color", "--require-surface", "100")
	if code != 3 {
		t.Errorf("exit = %d, want 3 for a run short of its surface\n%s", code, output)
	}
	section := surfaceSection(output)
	requireAll(t, section,
		"examples  3 of 5  60.0%",
		"DELETE /widgets/{id}  skipped by configuration: destroys shared state",
		"POST /admin/reindex",
		"left out by exclude-tag admin",
		"GET /widgets/{id}  404  skipped by configuration: the example id always exists",
		"POST /billing/charge  charges a real card (x-vertrag-exclude)",
		"reached anyway: an exclusion decides what counts, not what is sent",
		"The run reached 3 of 5 operation(s), 60.0%, below the 100.0% it requires.",
	)

	// The same run without a gate passes, and still says what it reached.
	output, code = runIn(t, dir, binary, "run", "--no-color")
	if code != 0 {
		t.Errorf("exit = %d without a gate, want 0\n%s", code, output)
	}
	requireAll(t, surfaceSection(output), "3 of 5  60.0%", "DELETE /widgets/{id}")
	if strings.Contains(output, "it requires") {
		t.Errorf("a run with no gate spoke of one:\n%s", output)
	}

	// Exactly at the gate is meeting it.
	if output, code := runIn(t, dir, binary, "run", "--no-color", "--require-surface", "60"); code != 0 {
		t.Errorf("exit = %d at exactly the share required, want 0\n%s", code, output)
	}
}

// TestASurfaceShortfallIsNotReadAsProbingFindings pins the exit codes against
// each other. Exit 2 says the documented contract held and probing found
// something to file; a run that left part of the contract untested has not
// shown it held, so it exits 3 however much probing found.
func TestASurfaceShortfallIsNotReadAsProbingFindings(t *testing.T) {
	binary := build(t)
	dir := surfaceProject(t, lenient(t), "")

	output, code := runIn(t, dir, binary, "run", "--no-color", "--phases", "coverage", "--require-surface", "60")
	if code != 2 {
		t.Fatalf("exit = %d with the gate met and probing findings, want 2\n%s", code, output)
	}
	requireAll(t, surfaceSection(output), "coverage", "the run")

	output, code = runIn(t, dir, binary, "run", "--no-color", "--phases", "coverage", "--require-surface", "100")
	if code != 3 {
		t.Errorf("exit = %d with the gate missed and probing findings, want 3\n%s", code, output)
	}
}

// TestTheSurfaceSectionOfTheConfigIsReadAndExclusionsAreAccountedFor drives
// the `surface` key and both ways of naming an operation in it.
func TestTheSurfaceSectionOfTheConfigIsReadAndExclusionsAreAccountedFor(t *testing.T) {
	binary := build(t)
	dir := surfaceProject(t, lenient(t), `surface:
  require: 100
  exclude:
    - operation: deleteWidget
      reason: covered by the lifecycle suite
    - operation: POST /admin/reindex
      reason: an operator task, not part of the public contract
`)

	output, code := runIn(t, dir, binary, "run", "--no-color")
	if code != 0 {
		t.Errorf("exit = %d with every unreached operation excluded, want 0\n%s", code, output)
	}
	requireAll(t, surfaceSection(output),
		"examples  3 of 3  100.0%",
		"DELETE /widgets/{id}  covered by the lifecycle suite (surface.exclude)",
		"POST /admin/reindex   an operator task, not part of the public contract (surface.exclude)",
		"which meets the 100.0% it requires",
	)
	if strings.Contains(surfaceSection(output), "Operations no phase reached") {
		t.Errorf("an excluded operation was listed as missed:\n%s", output)
	}
}

// TestADryRunSaysWhatARunCouldReachAndSendsNothing is the preflight: whether a
// configuration could pass its gate is decided by its filters and skips, which
// are known before anything is sent.
func TestADryRunSaysWhatARunCouldReachAndSendsNothing(t *testing.T) {
	binary := build(t)
	// A closed port: a connection error in the output would prove something
	// was sent.
	dir := surfaceProject(t, "http://127.0.0.1:1", "")

	output, code := runIn(t, dir, binary, "run", "--no-color", "--dry-run", "--require-surface", "100")
	if code != 3 {
		t.Errorf("exit = %d for a dry run that cannot meet its gate, want 3\n%s", code, output)
	}
	requireAll(t, surfaceSection(output),
		"dry run: nothing was sent",
		"Operations a run would send nothing to:",
		"DELETE /widgets/{id}  skipped by configuration: destroys shared state",
		"A run would send a request to 3 of 5 operation(s), 60.0%, so it cannot reach the 100.0% it requires.",
	)
	if strings.Contains(output, "refused") || strings.Contains(output, "documented statuses") {
		t.Errorf("a dry run sent something, or counted statuses it cannot have seen:\n%s", output)
	}

	if output, code := runIn(t, dir, binary, "run", "--no-color", "--dry-run", "--require-surface", "60"); code != 0 {
		t.Errorf("exit = %d for a dry run within reach of its gate, want 0\n%s", code, output)
	}
}

// TestAFilterThatSelectsNothingDoesNotPassTheGate is the run that most needs
// it: a misspelled tag sends nothing, and used to leave saying only that there
// was nothing to run.
func TestAFilterThatSelectsNothingDoesNotPassTheGate(t *testing.T) {
	binary := build(t)
	dir := surfaceProject(t, lenient(t), "")

	output, code := runIn(t, dir, binary, "run", "--no-color", "--tag", "ordres", "--require-surface", "100")
	if code != 3 {
		t.Errorf("exit = %d for a run that selected nothing under a gate, want 3\n%s", code, output)
	}
	requireAll(t, output, "No transactions to run.", "not selected by tag", "0 of 5")

	if output, code := runIn(t, dir, binary, "run", "--no-color", "--tag", "ordres"); code != 0 {
		t.Errorf("exit = %d for an empty run with no gate, want 0 as before\n%s", code, output)
	}
}

// TestAnImpossibleGateIsRefusedBeforeAnythingIsSent covers both places a gate
// can be written.
func TestAnImpossibleGateIsRefusedBeforeAnythingIsSent(t *testing.T) {
	binary := build(t)
	dir := surfaceProject(t, "http://127.0.0.1:1", "")
	output, code := runIn(t, dir, binary, "run", "--no-color", "--require-surface", "150")
	if code != 1 || !strings.Contains(output, "from 0 to 100") {
		t.Errorf("exit = %d for --require-surface 150, want a refusal\n%s", code, output)
	}

	dir = surfaceProject(t, "http://127.0.0.1:1", "surface:\n  exclude:\n    - operation: deleteWidget\n")
	output, code = runIn(t, dir, binary, "run", "--no-color")
	if code != 1 || !strings.Contains(output, "gives no reason") {
		t.Errorf("exit = %d for an exclusion without a reason, want a refusal\n%s", code, output)
	}
}

// TestTheSurfaceReachesTheWrittenReports pins the figures into every report a
// pipeline archives, and the gate into the JUnit file as a case of its own.
func TestTheSurfaceReachesTheWrittenReports(t *testing.T) {
	binary := build(t)
	dir := surfaceProject(t, lenient(t), `reporter: [cli, junit, json, markdown, html]
output: ["", report.xml, report.json, report.md, report.html]
surface:
  require: 100
`)
	output, code := runIn(t, dir, binary, "run", "--no-color")
	if code != 3 {
		t.Fatalf("exit = %d, want 3\n%s", code, output)
	}
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s was not written: %v", name, err)
		}
		return string(data)
	}

	requireAll(t, read("report.xml"),
		`<property name="vertrag.surface.operations" value="3/5"></property>`,
		`<property name="vertrag.surface.statuses" value="3/6"></property>`,
		`<property name="vertrag.surface.examples.operations" value="3/5"></property>`,
		`<property name="vertrag.surface.required" value="100.0%"></property>`,
		`<testcase name="API surface" classname="vertrag.surface"`,
		`DELETE /widgets/{id}: skipped by configuration: destroys shared state`,
	)

	var document struct {
		Summary struct{ Passed bool }
		Surface struct {
			Run    struct{ Operations, OperationsReached int }
			Missed []struct{ Operation, Status, Reason string }
		}
	}
	if err := json.Unmarshal([]byte(read("report.json")), &document); err != nil {
		t.Fatalf("the JSON report does not parse: %v", err)
	}
	if document.Summary.Passed || document.Surface.Run.Operations != 5 || document.Surface.Run.OperationsReached != 3 {
		t.Errorf("JSON summary and surface = %+v", document)
	}
	if len(document.Surface.Missed) == 0 || document.Surface.Missed[0].Operation != "GET /widgets/{id}" {
		t.Errorf("JSON misses are not in description order: %+v", document.Surface.Missed)
	}

	requireAll(t, read("report.md"), "## API surface", "| examples | 3 of 5  60.0% | 3 of 6  50.0% |",
		"| DELETE /widgets/{id} | skipped by configuration: destroys shared state |")
	requireAll(t, read("report.html"), "<h2>API surface</h2>", "<td>DELETE /widgets/{id}</td>")
}
