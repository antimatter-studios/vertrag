package corpus_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/corpus"
	"github.com/antimatter-studios/vertrag/link"
	"github.com/antimatter-studios/vertrag/runner"
)

// runLinked points vertrag at a stateful corpus server, with or without
// sequencing, and returns the results in document order.
func runLinked(t *testing.T, sequenced bool) []runner.Result {
	t.Helper()

	server, err := corpus.NewNamed("linked")
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}
	http := httptest.NewServer(server.Stateful().Handler())
	t.Cleanup(http.Close)

	source, err := corpus.Load("linked")
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	parsed, err := apidesc.Parse(source, "linked")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	compiled := compile.Compile(parsed.MediaType, parsed.Elements, "linked")

	engine := runner.New(http.URL)
	if sequenced {
		engine.Plan = link.NewSequencer(compiled.Transactions)
	}

	results, err := engine.Run(context.Background(), compiled.Transactions)
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	return results
}

func statuses(results []runner.Result) map[string]runner.Status {
	out := map[string]runner.Status{}
	for _, result := range results {
		out[result.Request.Method] = result.Status
	}
	return out
}

// TestWithoutSequencingTheReadFails establishes the problem is real before
// showing it solved.
//
// The description lists the read before the create, so a run in document order
// asks for a widget nothing has made. The 404 that comes back is not a fault of
// the server and says nothing useful about it — it is an artefact of testing
// one request at a time.
func TestWithoutSequencingTheReadFails(t *testing.T) {
	got := statuses(runLinked(t, false))

	if got["GET"] == runner.StatusPass {
		t.Error("the read passed without the create having run, so this proves nothing")
	}
	if got["POST"] != runner.StatusPass {
		t.Errorf("the create should pass on its own, got %s", got["POST"])
	}
}

// TestSequencingMakesTheReadPass is the feature.
//
// The link says the created widget can be read, and where its identifier comes
// from. The run reorders so the create happens first, takes the id out of its
// response, and puts it into the read's path parameter.
func TestSequencingMakesTheReadPass(t *testing.T) {
	results := runLinked(t, true)
	got := statuses(results)

	for method, status := range got {
		if status != runner.StatusPass {
			for _, result := range results {
				if result.Request.Method == method {
					t.Errorf("%s %s: %s\n  errors: %v",
						method, result.Request.URI, status, result.Errors)
				}
			}
		}
	}
}

// TestSequencingRunsEveryTransactionExactlyOnce pins the property the whole
// design rests on.
//
// Sequencing reorders a run; it does not add to it. If the counts moved, every
// CI dashboard using vertrag would shift the day this was switched on, and the
// report would be half noise.
func TestSequencingRunsEveryTransactionExactlyOnce(t *testing.T) {
	flat := runLinked(t, false)
	sequenced := runLinked(t, true)

	if len(flat) != len(sequenced) {
		t.Errorf("flat run had %d result(s), sequenced had %d", len(flat), len(sequenced))
	}

	// Results come back in the document's order however the plan chose to run
	// them, so a report can still be read against the description.
	for i := range flat {
		if flat[i].Name != sequenced[i].Name {
			t.Errorf("result %d: flat is %q, sequenced is %q — the report order should not move",
				i, flat[i].Name, sequenced[i].Name)
		}
	}
}

// TestAFailedStepSkipsWhatDependsOnItRatherThanCascading pins that one root
// cause produces one finding.
//
// The chain is create, read what was created, delete what was read. If the
// create fails, running the read anyway would send the description's own
// example — a 404 against an identifier nothing made — and running the delete
// would do it again. Three failures for one cause, two of them pointing
// nowhere, is how a reader learns to stop reading a report.
func TestAFailedStepSkipsWhatDependsOnItRatherThanCascading(t *testing.T) {
	server, err := corpus.NewNamed("chained", corpus.FaultWrongStatus)
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}
	http := httptest.NewServer(server.Stateful().Handler())
	t.Cleanup(http.Close)

	source, _ := corpus.Load("chained")
	parsed, err := apidesc.Parse(source, "chained")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	compiled := compile.Compile(parsed.MediaType, parsed.Elements, "chained")

	engine := runner.New(http.URL)
	engine.Plan = link.NewSequencer(compiled.Transactions)

	results, err := engine.Run(context.Background(), compiled.Transactions)
	if err != nil {
		t.Fatalf("running: %v", err)
	}

	var failed, skipped int
	for _, result := range results {
		switch result.Status {
		case runner.StatusFail, runner.StatusError:
			failed++
		case runner.StatusSkip:
			skipped++
		}
	}

	if failed != 1 {
		t.Errorf("got %d failure(s) for one root cause, want 1", failed)
		for _, result := range results {
			t.Logf("  %-8s %s %v", result.Status, result.Name, result.Errors)
		}
	}
	if skipped != 2 {
		t.Errorf("got %d skip(s), want the two steps that depended on the failure", skipped)
		for _, result := range results {
			t.Logf("  %-8s %-46s %v", result.Status, result.Name, result.Errors)
		}
	}

	// A skip has to say why, or it is indistinguishable from a transaction
	// somebody excluded on purpose.
	for _, result := range results {
		if result.Status != runner.StatusSkip {
			continue
		}
		if len(result.Errors) == 0 {
			t.Errorf("%s was skipped with no reason given", result.Name)
		}
	}
}
