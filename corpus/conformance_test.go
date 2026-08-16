package corpus_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/corpus"
	"github.com/antimatter-studios/vertrag/runner"
)

// run points vertrag at a corpus server and returns what it found.
func run(t *testing.T, name string, checks runner.Checks, faults ...corpus.Fault) []runner.Result {
	t.Helper()

	server, err := corpus.NewNamed(name, faults...)
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}
	http := httptest.NewServer(server.Handler())
	t.Cleanup(http.Close)

	source, err := corpus.Load(name)
	if err != nil {
		t.Fatalf("loading %s: %v", name, err)
	}
	parsed, err := apidesc.Parse(source, name)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	compiled := compile.Compile(parsed.MediaType, parsed.Elements, name)

	engine := runner.New(http.URL)
	engine.Checks = checks
	results, err := engine.Run(context.Background(), compiled.Transactions)
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	return results
}

func failures(results []runner.Result) []runner.Result {
	var out []runner.Result
	for _, result := range results {
		if result.Status != "pass" {
			out = append(out, result)
		}
	}
	return out
}

// TestAConformingServerProducesNoFindings is the baseline the whole corpus
// rests on.
//
// The server answers exactly what the description promises, so every failure
// here is vertrag reporting a violation that did not happen. That is the half
// of correctness which is otherwise almost impossible to measure: a tester can
// be made to catch any particular bug by adding a check for it, and the cost of
// doing so — false findings on correct servers — only shows up in someone
// else's suite weeks later.
func TestAConformingServerProducesNoFindings(t *testing.T) {
	for _, name := range corpus.Names() {
		t.Run(name, func(t *testing.T) {
			results := run(t, name, runner.Checks{ServerError: true, ContentType: true, HeaderSchema: true})

			for _, failed := range failures(results) {
				// Beyond holds the checks Dredd does not make, and a finding
				// there fails a transaction while leaving Errors empty. An
				// earlier version of this printed only Errors and reported a
				// failure with no reason at all.
				reasons := append(append([]string{}, failed.Errors...), failed.Beyond...)
				t.Errorf("false finding on %s:\n  status: %s\n  %s",
					failed.Name, failed.Status, strings.Join(reasons, "\n  "))
			}
			if len(results) == 0 {
				t.Error("nothing ran, so nothing was established")
			}
		})
	}
}

// TestEachFaultIsFound is the other half: a violation deliberately committed
// must be reported.
//
// Each fault is switched on alone. A fault that produces no finding is a
// violation vertrag would miss in the wild; the assertion is deliberately only
// that something was found, because which transaction carries it depends on the
// description and pinning that would make the corpus painful to extend.
func TestEachFaultIsFound(t *testing.T) {
	// The faults a run of a conforming description can exhibit. The
	// input-validation faults are not here: they concern what the server does
	// with input it should refuse, and a normal run only ever sends the
	// documented example, which is valid. Those are the business of the
	// generated-input tests, and their absence here is the point — it is
	// exactly the gap `vertrag fuzz` exists to close.
	responseFaults := []corpus.Fault{
		corpus.FaultWrongStatus,
		corpus.FaultWrongContentType,
		corpus.FaultBodyViolatesSchema,
		corpus.FaultMissingProperty,
		corpus.FaultMissingHeader,
		corpus.FaultHeaderViolatesSchema,
		corpus.FaultRejectsValidInput,
	}

	checks := runner.Checks{ServerError: true, ContentType: true, HeaderSchema: true}

	for _, fault := range responseFaults {
		t.Run(string(fault), func(t *testing.T) {
			// Every description, not one. A fault that only ever fires against
			// a single document proves less than it appears to: the descriptions
			// differ in what they declare — headers, several outcomes, non-JSON
			// bodies — and a check that works on one shape can be blind on
			// another.
			var found []string
			for _, name := range corpus.Names() {
				if len(failures(run(t, name, checks, fault))) > 0 {
					found = append(found, name)
				}
			}

			if len(found) == 0 {
				t.Errorf("%s was committed against every description in the corpus and nothing was reported", fault)
			}
			t.Logf("found in %d of %d description(s): %s",
				len(found), len(corpus.Names()), strings.Join(found, ", "))
		})
	}
}

// TestNoFaultGoesUnnoticedInADescriptionThatCarriesItsFeature is the sharper
// question: not "is this fault findable somewhere" but "is it findable wherever
// it applies".
//
// A description declaring response headers should report a header fault. One
// declaring none cannot, and saying so is the difference between coverage and
// the appearance of it.
func TestNoFaultGoesUnnoticedInADescriptionThatCarriesItsFeature(t *testing.T) {
	checks := runner.Checks{ServerError: true, ContentType: true, HeaderSchema: true}

	// A fault that alters every response should be found in every description,
	// because every description has at least one response.
	universal := []corpus.Fault{
		corpus.FaultWrongStatus,
		corpus.FaultRejectsValidInput,
	}

	for _, fault := range universal {
		t.Run(string(fault), func(t *testing.T) {
			for _, name := range corpus.Names() {
				if len(failures(run(t, name, checks, fault))) == 0 {
					t.Errorf("%s went unnoticed in %s, which has responses to alter", fault, name)
				}
			}
		})
	}
}

// TestFaultsNotExercisedByARunAreListed keeps the catalogue honest.
//
// Some faults concern how a server treats input it should refuse, and a
// deterministic run cannot reach them because it only ever sends the documented
// example. Naming them here means the gap is recorded rather than silently
// uncovered, and the test fails if a fault is added to the catalogue and
// assigned to neither list.
func TestFaultsNotExercisedByARunAreListed(t *testing.T) {
	reachedByRun := map[corpus.Fault]bool{
		corpus.FaultWrongStatus:          true,
		corpus.FaultWrongContentType:     true,
		corpus.FaultBodyViolatesSchema:   true,
		corpus.FaultMissingProperty:      true,
		corpus.FaultMissingHeader:        true,
		corpus.FaultHeaderViolatesSchema: true,
		corpus.FaultRejectsValidInput:    true,
	}
	// Reachable only by sending input the description forbids, which is
	// generation's job.
	reachedByGeneration := map[corpus.Fault]bool{
		corpus.FaultAcceptsAnyParameter: true,
		corpus.FaultAcceptsAnyBody:      true,
		corpus.FaultCrashesOnBadInput:   true,
	}

	for _, fault := range corpus.Faults() {
		if !reachedByRun[fault] && !reachedByGeneration[fault] {
			t.Errorf("%s is in the catalogue but no test claims to reach it", fault)
		}
	}
}
