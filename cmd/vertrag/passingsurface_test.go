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

	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// passingDescription has four operations. GET /half documents a 404 the
// settings below skip, so it is reached and can never pass; the others pass
// when the server answers them as documented.
const passingDescription = `openapi: 3.0.3
info: {title: Passing, version: "1.0"}
paths:
  /ok:
    get:
      summary: Ok
      operationId: getOk
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
  /other:
    get:
      summary: Other
      operationId: getOther
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
  /half:
    get:
      summary: Half
      operationId: getHalf
      parameters:
        - {name: limit, in: query, schema: {type: integer, maximum: 10}, example: 5}
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
        "404": {description: missing, content: {application/json: {schema: {type: object}}}}
  /broken:
    get:
      summary: Broken
      operationId: getBroken
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
`

const passingSettings = `skip:
  - name: "/half > Half > 404 > application/json"
    reason: the example always exists
`

// passingServer answers every operation 200, or /broken with a 500 when
// broken is set.
func passingServer(t *testing.T, broken bool) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if broken && r.URL.Path == "/broken" {
			w.WriteHeader(http.StatusInternalServerError)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func passingProject(t *testing.T, endpoint, extra string) string {
	t.Helper()
	dir := t.TempDir()
	settings := fmt.Sprintf("spec: ./api.yml\nendpoint: %s\n%s%s", endpoint, passingSettings, extra)
	for name, content := range map[string]string{"api.yml": passingDescription, "vertrag.yml": settings} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestAPassedGateDoesNotCountAnOperationWithASkippedTransaction is the
// issue's first gap: GET /half answered its 200 and passed it, and its 404
// was skipped, so it was reached and did not pass. Nothing failed, so the
// shortfall is the run's only fault and exits 3.
func TestAPassedGateDoesNotCountAnOperationWithASkippedTransaction(t *testing.T) {
	binary := build(t)
	dir := passingProject(t, passingServer(t, false), "")

	output, code := runIn(t, dir, binary, "run", "--no-color", "--surface-count", "passed", "--require-surface", "100")
	if code != 3 {
		t.Errorf("exit = %d, want 3 for a run that passed less than it requires\n%s", code, output)
	}
	requireAll(t, surfaceSection(output),
		"operations passed",
		"examples  4 of 4  100.0%",
		"3 of 4  75.0%",
		"Operations reached that did not pass:",
		"GET /half  404 skipped by configuration: the example always exists",
		"The run passed 3 of 4 operation(s), 75.0%, below the 100.0% it requires.",
	)

	// The same run counting what it reached meets the same gate: the
	// default, and the gate as v0.28.0 shipped it, is unchanged.
	output, code = runIn(t, dir, binary, "run", "--no-color", "--require-surface", "100")
	if code != 0 {
		t.Errorf("exit = %d counting reached operations, want 0\n%s", code, output)
	}
	requireAll(t, surfaceSection(output), "The run reached 4 of 4 operation(s), 100.0%")
	if strings.Contains(output, "operations passed") {
		t.Errorf("a reached gate showed the passed column:\n%s", output)
	}
}

// TestThePassedGateBoundary is met exactly at the share required and not a
// hair above it, as the reached gate is.
func TestThePassedGateBoundary(t *testing.T) {
	binary := build(t)
	dir := passingProject(t, passingServer(t, false), "surface:\n  count: passed\n")

	if output, code := runIn(t, dir, binary, "run", "--no-color", "--require-surface", "75"); code != 0 {
		t.Errorf("exit = %d at exactly the share passed, want 0\n%s", code, output)
	}
	if output, code := runIn(t, dir, binary, "run", "--no-color", "--require-surface", "75.1"); code != 3 {
		t.Errorf("exit = %d just above the share passed, want 3\n%s", code, output)
	}

	// A dry run knows the skipped 404 keeps GET /half from passing, so it
	// says the most that could pass and fails a gate above it.
	if output, code := runIn(t, dir, binary, "run", "--no-color", "--dry-run", "--require-surface", "75"); code != 0 {
		t.Errorf("exit = %d for a dry run within reach of its passed gate, want 0\n%s", code, output)
	}
	output, code := runIn(t, dir, binary, "run", "--no-color", "--dry-run", "--require-surface", "100")
	if code != 3 {
		t.Errorf("exit = %d for a dry run that cannot pass its gate, want 3\n%s", code, output)
	}
	requireAll(t, surfaceSection(output),
		"A run would send every counted transaction of 3 of 4 operation(s), 75.0%, so it cannot pass the 100.0% it requires.")
}

// TestAnAnswered500IsReachedAndNeverPassed: the 500 reached GET /broken and
// failed its transaction. The failure outranks the shortfall, so the run exits
// 1 — a response broke the contract — and the surface still says the
// operation was reached and did not pass.
func TestAnAnswered500IsReachedAndNeverPassed(t *testing.T) {
	binary := build(t)
	dir := passingProject(t, passingServer(t, true), "")

	output, code := runIn(t, dir, binary, "run", "--no-color", "--surface-count", "passed", "--require-surface", "100")
	if code != 1 {
		t.Errorf("exit = %d with a failed transaction and a shortfall, want 1\n%s", code, output)
	}
	requireAll(t, surfaceSection(output),
		"examples  4 of 4  100.0%",
		"2 of 4  50.0%",
		"GET /broken  200 answered 500 and failed",
		"The run passed 2 of 4 operation(s), 50.0%, below the 100.0% it requires.",
	)
}

// TestAPassedShortfallOutranksProbingFindings: probing found GET /half
// accepting a limit above its maximum, and the run also passed less than it
// requires. 3, not 2: exit 2 says the documented contract held.
func TestAPassedShortfallOutranksProbingFindings(t *testing.T) {
	binary := build(t)
	dir := passingProject(t, passingServer(t, false), "")

	output, code := runIn(t, dir, binary, "run", "--no-color", "--phases", "coverage",
		"--surface-count", "passed", "--require-surface", "100")
	if code != 3 {
		t.Errorf("exit = %d with probing findings and a shortfall, want 3\n%s", code, output)
	}
	output, code = runIn(t, dir, binary, "run", "--no-color", "--phases", "coverage",
		"--surface-count", "passed", "--require-surface", "75")
	if code != 2 {
		t.Errorf("exit = %d with probing findings and the gate met, want 2\n%s", code, output)
	}
}

// TestAStatusExclusionTakesTheStatusOutOfBothSides covers the forms a status
// exclusion takes: exact on one operation, a range on every operation, and a
// pattern on a pattern of operations. Each takes the 404 out of what GET
// /half has to pass, so it passes, and each lists what it excluded.
func TestAStatusExclusionTakesTheStatusOutOfBothSides(t *testing.T) {
	binary := build(t)
	for _, test := range []struct{ name, exclusion, listed string }{
		{"exact", "{operation: getHalf, status: '404', reason: 'unreachable until #71'}", "GET /half 404"},
		{"a range on every operation", "{status: 4XX, reason: 'error responses, until #71'}", "GET /half 404"},
		{"a pattern", "{operation: 'GET /ha*', status: '40?', reason: 'error responses, until #71'}", "GET /half 404"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := passingProject(t, passingServer(t, false),
				"surface:\n  count: passed\n  require: 100\n  exclude:\n    - "+test.exclusion+"\n")
			output, code := runIn(t, dir, binary, "run", "--no-color")
			if code != 0 {
				t.Errorf("exit = %d with the skipped status excluded, want 0\n%s", code, output)
			}
			requireAll(t, surfaceSection(output),
				"examples  4 of 4  100.0%   4 of 4  100.0%        4 of 4  100.0%",
				"Excluded, and not counted:",
				test.listed+"  ",
				"(surface.exclude)",
				"The run passed 4 of 4 operation(s), 100.0%, which meets the 100.0% it requires.",
			)
			if strings.Contains(output, "names no documented status") {
				t.Errorf("a matching exclusion was reported as matching nothing:\n%s", output)
			}
		})
	}
}

// TestAStatusExclusionThatMatchesNothingIsSaidAndOneThatCannotIsRefused: a
// status no operation documents excludes nothing and the run says so; a
// status no response could have, a misspelt count, or an entry naming
// neither operation nor status is refused before anything is sent.
func TestAStatusExclusionThatMatchesNothingIsSaidAndOneThatCannotIsRefused(t *testing.T) {
	binary := build(t)

	dir := passingProject(t, passingServer(t, false),
		"surface:\n  exclude:\n    - {status: '418', reason: teapots}\n")
	output, code := runIn(t, dir, binary, "run", "--no-color")
	if code != 0 || !strings.Contains(output, `surface.exclude names no documented status "418" of any operation`) {
		t.Errorf("exit = %d; an unmatched status exclusion was not reported\n%s", code, output)
	}
	output, _ = runIn(t, dir, binary, "doctor")
	if !strings.Contains(output, "surface.exclude") || !strings.Contains(output, "418") {
		t.Errorf("doctor did not report the unmatched status exclusion:\n%s", output)
	}

	for _, test := range []struct{ name, section, flag, want string }{
		{"a status no response has", "surface:\n  exclude:\n    - {status: '600', reason: x}\n", "",
			"is not a documented status"},
		{"a word for a status", "surface:\n  exclude:\n    - {status: forbidden, reason: x}\n", "",
			"is not a documented status"},
		{"no operation and no status", "surface:\n  exclude:\n    - {reason: x}\n", "", "names no operation"},
		{"a status without a reason", "surface:\n  exclude:\n    - {status: '403'}\n", "", "gives no reason"},
		{"a misspelt count", "surface:\n  count: passd\n", "", `count is "reached" or "passed"`},
		{"a misspelt count flag", "", "--surface-count=pass", `--surface-count: count is "reached" or "passed"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := passingProject(t, "http://127.0.0.1:1", test.section)
			args := []string{"run", "--no-color"}
			if test.flag != "" {
				args = append(args, test.flag)
			}
			output, code := runIn(t, dir, binary, args...)
			if code != 1 || !strings.Contains(output, test.want) {
				t.Errorf("exit = %d, want 1 and a refusal saying %q\n%s", code, test.want, output)
			}
			if strings.Contains(output, "refused") {
				t.Errorf("something was sent before the refusal:\n%s", output)
			}
		})
	}
}

// TestNothingLeftToCountIsNotAPass: with every status excluded no operation
// is left, and a passed gate does not read a hundred percent of nothing as a
// pass. The reached gate keeps reading it as all of it reached.
func TestNothingLeftToCountIsNotAPass(t *testing.T) {
	binary := build(t)
	dir := passingProject(t, passingServer(t, false),
		"surface:\n  require: 100\n  exclude:\n    - {status: '*', reason: nothing is ready}\n")

	output, code := runIn(t, dir, binary, "run", "--no-color", "--surface-count", "passed")
	if code != 3 {
		t.Errorf("exit = %d with nothing left to count under a passed gate, want 3\n%s", code, output)
	}
	requireAll(t, surfaceSection(output),
		"GET /half 404",
		"GET /ok 200",
		"No operation is left to count once the exclusions apply, so none passed and the 100.0% it requires is not met.",
	)

	if output, code := runIn(t, dir, binary, "run", "--no-color"); code != 0 {
		t.Errorf("exit = %d with nothing left to count under a reached gate, want 0 as before\n%s", code, output)
	}
}

// TestThePassedFiguresReachTheWrittenReports pins the count, the passed
// figures and what did not pass into the JSON and JUnit files.
func TestThePassedFiguresReachTheWrittenReports(t *testing.T) {
	binary := build(t)
	dir := passingProject(t, passingServer(t, false), `reporter: [cli, junit, json]
output: ["", report.xml, report.json]
surface:
  count: passed
  require: 100
  exclude:
    - {operation: getOther, reason: covered elsewhere}
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
		`<property name="vertrag.surface.operations" value="3/3"></property>`,
		`<property name="vertrag.surface.passed" value="2/3"></property>`,
		`<property name="vertrag.surface.examples.passed" value="2/3"></property>`,
		`<property name="vertrag.surface.count" value="passed"></property>`,
		`<testcase name="API surface" classname="vertrag.surface"`,
		`GET /half: did not pass: 404 skipped by configuration: the example always exists`,
	)

	var document struct {
		Summary struct{ Passed bool }
		Surface struct {
			Count     string
			Run       struct{ Operations, OperationsReached, OperationsPassed int }
			NotPassed []struct{ Operation, Reason string }
			Excluded  []struct{ Operation, Status, Reason string }
		}
	}
	if err := json.Unmarshal([]byte(read("report.json")), &document); err != nil {
		t.Fatalf("the JSON report does not parse: %v", err)
	}
	surface := document.Surface
	if document.Summary.Passed || surface.Count != "passed" || surface.Run.Operations != 3 ||
		surface.Run.OperationsReached != 3 || surface.Run.OperationsPassed != 2 {
		t.Errorf("JSON summary and surface = %+v", document)
	}
	if len(surface.NotPassed) != 1 || surface.NotPassed[0].Operation != "GET /half" {
		t.Errorf("JSON notPassed = %+v, want GET /half alone", surface.NotPassed)
	}
	if len(surface.Excluded) != 1 || surface.Excluded[0].Operation != "GET /other" || surface.Excluded[0].Reason == "" {
		t.Errorf("JSON excluded = %+v, want GET /other with its reason", surface.Excluded)
	}
}

// TestA500OrNoAnswerNeverCountsAsPassed is the rule beneath the gate, for the
// results a CLI run cannot easily produce: a 500 a documented 5xx matched,
// and a pass with no answer at all.
func TestA500OrNoAnswerNeverCountsAsPassed(t *testing.T) {
	for _, test := range []struct {
		name   string
		result runner.Result
		passes bool
	}{
		{"a 200 that passed", runner.Result{Status: runner.StatusPass, Actual: validate.Message{StatusCode: "200"}}, true},
		{"a documented 500 that passed", runner.Result{Status: runner.StatusPass, Actual: validate.Message{StatusCode: "500"}}, false},
		{"a pass without an answer", runner.Result{Status: runner.StatusPass}, false},
		{"a 200 that failed", runner.Result{Status: runner.StatusFail, Actual: validate.Message{StatusCode: "200"}}, false},
		{"a skip", runner.Result{Status: runner.StatusSkip, Errors: []string{"skipped by configuration"}}, false},
		{"an error", runner.Result{Status: runner.StatusError, Errors: []string{"connection refused"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			passes, why := passedOutright(test.result)
			if passes != test.passes || (!passes && why == "") {
				t.Errorf("passedOutright = %v, %q; want %v and a reason when not", passes, why, test.passes)
			}
		})
	}
}
