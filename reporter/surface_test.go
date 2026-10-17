package reporter

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/runner"
)

// updateSurface rewrites the recorded surface renderings instead of comparing
// against them:
//
//	go test ./reporter/ -run TestTheSurfaceRenderingsAreRecorded -update
var updateSurface = flag.Bool("update", false, "rewrite the recorded surface renderings")

// surfaceFixture has a little of everything a surface report can say.
func surfaceFixture() Surface {
	return Surface{
		Phases: []SurfaceFigures{
			{Phase: "examples", Operations: 5, OperationsReached: 3, Statuses: 9, StatusesReached: 4},
			{Phase: "coverage", Operations: 5, OperationsReached: 2, Statuses: 9, StatusesReached: 3},
		},
		Run: SurfaceFigures{Phase: "run", Operations: 5, OperationsReached: 3, Statuses: 9, StatusesReached: 5},
		Missed: []SurfaceMiss{
			{Operation: "GET /widgets/{id}", Status: "404", Reason: "answered 200"},
			{Operation: "GET /widgets/{id}", Status: "409", Reason: "answered 200"},
			{Operation: "DELETE /widgets/{id}", Reason: "skipped by configuration: destroys shared state"},
			{Operation: "POST /widgets", Status: "422", Reason: "a hook skipped it"},
			{Operation: "POST /admin/reindex", Reason: "left out by exclude-tag admin"},
		},
		Excluded: []SurfaceExclusion{
			{Operation: "POST /billing/charge", Reason: "charges a real card", Source: "x-vertrag-exclude", Reached: true},
			{Operation: "GET /reports", Reason: "needs a reporting backend", Source: "surface.exclude"},
		},
		Required: 100,
	}
}

// TestTheSurfaceRenderingsAreRecorded pins the terminal section and the
// Markdown one byte for byte. They are what a reader sees, and an alignment or
// a heading that moved is a change somebody should have meant.
func TestTheSurfaceRenderingsAreRecorded(t *testing.T) {
	surface := surfaceFixture()
	dry := Surface{
		Run: SurfaceFigures{Phase: "run", Operations: 5, OperationsReached: 4, Statuses: 9},
		Missed: []SurfaceMiss{
			{Operation: "DELETE /widgets/{id}", Reason: "skipped by configuration: destroys shared state"},
		},
		Required: 100, DryRun: true,
	}

	var text bytes.Buffer
	WriteSurface(&text, surface, false)
	WriteSurface(&text, dry, false)

	var markdown bytes.Buffer
	Markdown{Out: &markdown, Surface: &surface}.Report(nil)
	Markdown{Out: &markdown, Surface: &dry}.Report(nil)

	for name, got := range map[string]string{"surface.txt": text.String(), "surface.md": markdown.String()} {
		path := filepath.Join("testdata", name)
		if *updateSurface {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (run with -update to record it)", err)
		}
		if got != string(want) {
			t.Errorf("%s changed.\n--- recorded ---\n%s\n--- now ---\n%s", name, want, got)
		}
	}
}

// TestAShareIsNeverShownFullUnlessItIs is the rounding rule. 1999 of 2000
// rounded to nearest is "100.0%", printed beside a gate of 100 that failed.
func TestAShareIsNeverShownFullUnlessItIs(t *testing.T) {
	figures := SurfaceFigures{Operations: 2000, OperationsReached: 1999}
	if got := Percent(figures.OperationShare()); got != "99.9%" {
		t.Errorf("1999 of 2000 = %s, want 99.9%%", got)
	}
	if got := Percent(SurfaceFigures{}.OperationShare()); got != "100.0%" {
		t.Errorf("nothing to reach = %s, want 100.0%%: there is nothing left to miss", got)
	}
	surface := Surface{Run: figures, Required: 100}
	if surface.Met() {
		t.Error("1999 of 2000 met a gate of 100")
	}
}

// TestTheJUnitFileCarriesTheFiguresAndTheGate pins both halves: the figures
// as properties, and a gate that failed as a failing case, so a CI interface
// showing a red job also shows why.
func TestTheJUnitFileCarriesTheFiguresAndTheGate(t *testing.T) {
	surface := surfaceFixture()
	var out bytes.Buffer
	passed := JUnit{Out: &out, Surface: &surface}.Report([]runner.Result{{Name: "a", Status: runner.StatusPass}})
	if passed {
		t.Error("a JUnit file whose gate failed reported a pass")
	}
	for _, want := range []string{
		`tests="2" failures="1"`,
		`<property name="vertrag.surface.operations" value="3/5"></property>`,
		`<property name="vertrag.surface.statuses" value="5/9"></property>`,
		`<property name="vertrag.surface.coverage.operations" value="2/5"></property>`,
		`<property name="vertrag.surface.required" value="100.0%"></property>`,
		`<property name="vertrag.surface.excluded" value="2"></property>`,
		`<testcase name="API surface" classname="vertrag.surface"`,
		`<failure message="The run reached 3 of 5 operation(s), 60.0%, below the 100.0% it requires." type="surface">`,
		`POST /admin/reindex: left out by exclude-tag admin`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the JUnit file does not carry %q:\n%s", want, out.String())
		}
	}

	// No gate, no case: a run that was asked for nothing is not given a test
	// it passed.
	surface.Required = 0
	out.Reset()
	JUnit{Out: &out, Surface: &surface}.Report(nil)
	if strings.Contains(out.String(), "API surface") || !strings.Contains(out.String(), "vertrag.surface.operations") {
		t.Errorf("without a gate the figures belong in properties and nowhere else:\n%s", out.String())
	}
}

// TestTheJSONReportCarriesTheRunAndItsSurface pins the document a pipeline
// parses. It is a contract the moment somebody's script reads it, so the
// names are checked rather than trusted.
func TestTheJSONReportCarriesTheRunAndItsSurface(t *testing.T) {
	surface := surfaceFixture()
	var out bytes.Buffer
	JSON{Out: &out, Run: Provenance{Version: "1.2.3", Spec: "api.yml"}, Surface: &surface}.Report([]runner.Result{
		{Name: "/widgets > List > 200", Status: runner.StatusPass, Request: runner.Request{Method: "GET", URI: "/widgets"}},
		{Name: "/widgets > Create > 201", Status: runner.StatusFail, Errors: []string{"body: missing id"}},
	})

	var document map[string]any
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	summary := document["summary"].(map[string]any)
	if summary["total"] != 2.0 || summary["failing"] != 1.0 || summary["passed"] != false {
		t.Errorf("summary = %v", summary)
	}
	run := document["surface"].(map[string]any)["run"].(map[string]any)
	if run["operations"] != 5.0 || run["operationsReached"] != 3.0 {
		t.Errorf("surface.run = %v", run)
	}
	if !strings.Contains(out.String(), `"/widgets > List > 200"`) {
		t.Errorf("transaction names were escaped:\n%s", out.String())
	}
	if document["vertrag"].(map[string]any)["version"] != "1.2.3" {
		t.Errorf("provenance = %v", document["vertrag"])
	}

	// Outside a run there may be no surface, and then there is no key.
	out.Reset()
	JSON{Out: &out}.Report(nil)
	if strings.Contains(out.String(), "surface") || !strings.Contains(out.String(), `"results": []`) {
		t.Errorf("a report with no surface:\n%s", out.String())
	}
}

// TestTheHTMLPageEscapesTheSurface is the same protection the rest of the page
// has: an operation name is the description's text, and a description is not
// trusted to be harmless.
func TestTheHTMLPageEscapesTheSurface(t *testing.T) {
	surface := surfaceFixture()
	surface.Missed = append(surface.Missed, SurfaceMiss{Operation: "GET /<script>alert(1)</script>", Reason: "a hook skipped it"})
	var out bytes.Buffer
	HTML{Out: &out, Surface: &surface}.Report(nil)
	page := out.String()
	if strings.Contains(page, "<script>alert") {
		t.Error("an operation name reached the page unescaped")
	}
	for _, want := range []string{"<h2>API surface</h2>", "<td>examples</td>", "<td>404, 409</td>",
		"below the 100.0% it requires", "`skip` is what keeps a request back"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page does not carry %q", want)
		}
	}
}

// TestWithSurfaceReachesEveryReporterThatHasAPlaceForIt includes the ones
// inside a Multi, which is how a run with a terminal log and a file is built.
func TestWithSurfaceReachesEveryReporterThatHasAPlaceForIt(t *testing.T) {
	surface := surfaceFixture()
	var junit, markdown, cli bytes.Buffer
	WithSurface(Multi{JUnit{Out: &junit}, Markdown{Out: &markdown}, CLI{Out: &cli}}, &surface).Report(nil)
	if !strings.Contains(junit.String(), "vertrag.surface") || !strings.Contains(markdown.String(), "## API surface") {
		t.Errorf("the surface did not reach a reporter inside Multi:\n%s\n%s", junit.String(), markdown.String())
	}
	if strings.Contains(cli.String(), "API surface") {
		t.Error("the CLI reporter printed the surface; the run prints it once, after every reporter")
	}
}
