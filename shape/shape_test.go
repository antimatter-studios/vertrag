package shape

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// The definition, asserted case by case. Compatible is deliberately narrow, and
// a check that reports too much is worse than one that reports nothing — so
// most of what follows pins what is NOT a difference.

// TestAStringWhereAnArrayWasIsIncompatible is the motivating defect reduced to
// two bodies: FastAPI's handler-raised 422 against its framework-raised one.
func TestAStringWhereAnArrayWasIsIncompatible(t *testing.T) {
	handler := `{"detail":"no item with id 999"}`
	framework := `{"detail":[{"loc":["path","itemId"],"msg":"Input should be a valid integer"}]}`

	if Compatible(handler, framework) {
		t.Error("a string detail and an array detail were called one shape")
	}
}

// TestOptionalFieldsPresentInOneBodyOnlyAreCompatible: every API has optional
// fields, and a check that reported them would report every API.
func TestOptionalFieldsPresentInOneBodyOnlyAreCompatible(t *testing.T) {
	sparse := `{"id":1}`
	full := `{"id":2,"name":"a widget","tags":["blue"],"owner":{"id":7}}`

	if !Compatible(sparse, full) {
		t.Error("a body with more fields present was called a different shape")
	}
}

// TestNullIsReadAsAbsence records the judgement, which could have gone the
// other way. `if x is None` and `if "x" not in body` are one branch in every
// client anyone writes, so a null is treated as the field not being there —
// and the cost is that a genuine null-versus-array confusion is not reported.
func TestNullIsReadAsAbsence(t *testing.T) {
	null := `{"detail":null}`
	array := `{"detail":[{"msg":"nope"}]}`
	absent := `{}`

	if !Compatible(null, array) {
		t.Error("null against an array was reported; null is read as absence")
	}
	if !Compatible(null, absent) {
		t.Error("null against a missing field was reported")
	}
	// The judgement is about null alone. A string against that array is still
	// the finding this exists to make.
	if Compatible(`{"detail":"nope"}`, array) {
		t.Error("treating null as absence has been over-applied to strings")
	}
}

// TestAnArrayLengthIsNotItsShape: a page of ten and a page of none are one
// shape, and an empty array says nothing about what it would have held.
func TestAnArrayLengthIsNotItsShape(t *testing.T) {
	empty := `{"items":[]}`
	one := `{"items":[{"id":1}]}`
	many := `{"items":[{"id":1},{"id":2},{"id":3}]}`

	for _, pair := range [][2]string{{empty, one}, {one, many}, {empty, many}} {
		if !Compatible(pair[0], pair[1]) {
			t.Errorf("%s and %s were called different shapes", pair[0], pair[1])
		}
	}
}

// TestArrayElementsAreComparedAgainstEachOther is the other half: the length is
// not the shape, but the sort of element is. This is the motivating defect one
// level down, and it is the same defect.
func TestArrayElementsAreComparedAgainstEachOther(t *testing.T) {
	objects := `{"errors":[{"msg":"nope"}]}`
	messages := `{"errors":["nope"]}`

	if Compatible(objects, messages) {
		t.Error("an array of objects and an array of strings were called one shape")
	}
}

// TestAValueIsNotAShape: a parser branches on the type, so 1 against 1.5 and
// true against false are nothing at all.
func TestAValueIsNotAShape(t *testing.T) {
	if !Compatible(`{"total":1,"ok":true}`, `{"total":1.5,"ok":false}`) {
		t.Error("two numbers, or two booleans, were called different shapes")
	}
}

// TestAnObjectAndAnArrayAtTheTopAreIncompatible: the disagreement can be the
// whole body rather than a field of it.
func TestAnObjectAndAnArrayAtTheTopAreIncompatible(t *testing.T) {
	if Compatible(`{"items":[]}`, `[{"id":1}]`) {
		t.Error("an object body and an array body were called one shape")
	}
}

// TestAnArrayAlreadyMixedInOneBodyIsNotReported: a server sending `[1,"two"]`
// has declared that path polymorphic itself, and comparing it against another
// response tells the reader nothing they cannot see in the one they have.
func TestAnArrayAlreadyMixedInOneBodyIsNotReported(t *testing.T) {
	mixed := `{"values":[1,"two"]}`
	numbers := `{"values":[3,4]}`

	if !Compatible(mixed, numbers) {
		t.Error("an array that was already mixed was compared against another")
	}
}

// TestABodyThatIsNotJSONIsNotGivenAShape: an unparseable body is a defect, but
// a different one, and "unparseable versus object" is not this report.
func TestABodyThatIsNotJSONIsNotGivenAShape(t *testing.T) {
	if !Compatible(`<html>500</html>`, `{"detail":"nope"}`) {
		t.Error("a body that is not JSON was compared as though it were")
	}
}

// TestAHugeBodyIsBoundedButStillJudged proves the bounds cannot invent a
// difference. A truncated outline holds fewer paths; a difference needs a path
// present in two bodies, so what the bound can cost is a report this failed to
// make, never one it made wrongly.
func TestAHugeBodyIsBoundedButStillJudged(t *testing.T) {
	var wide strings.Builder
	wide.WriteString(`{"detail":"nope"`)
	for i := 0; i < maxNodes*2; i++ {
		fmt.Fprintf(&wide, `,"field%06d":%d`, i, i)
	}
	wide.WriteString("}")

	out, ok := outlineOf(wide.String())
	if !ok {
		t.Fatal("a large but valid body did not parse")
	}
	if len(out) > maxNodes {
		t.Errorf("the outline holds %d paths, above the %d bound", len(out), maxNodes)
	}
	// `detail` sorts before every `fieldNNNNNN`, so the bound cannot lose it,
	// and the finding survives the truncation.
	if Compatible(wide.String(), `{"detail":["nope"]}`) {
		t.Error("truncation lost the difference at a path both bodies have")
	}
}

// jsonHeaders is what a JSON response arrives with. Every recorder test below
// uses it, because the media type is the point of a different test.
var jsonHeaders = map[string]string{"content-type": "application/json; charset=utf-8"}

// TestTheRecorderSaysWhichPhaseSawWhichShape is the second design constraint:
// "the examples phase got a string, the probing phases got an array" points at
// the cause where "two shapes" leaves someone hunting.
func TestTheRecorderSaysWhichPhaseSawWhichShape(t *testing.T) {
	var recorder Recorder
	recorder.Record("examples", "GET /items/{itemId}", "422", jsonHeaders, `{"detail":"no item"}`)
	recorder.Record("fuzz", "GET /items/{itemId}", "422", jsonHeaders, `{"detail":[{"msg":"nope"}]}`)
	recorder.Record("coverage", "GET /items/{itemId}", "422", jsonHeaders, `{"detail":[{"msg":"nope"}]}`)

	divergences := recorder.Divergences()
	if len(divergences) != 1 {
		t.Fatalf("got %d divergences, want 1: %+v", len(divergences), divergences)
	}
	if divergences[0].Operation != "GET /items/{itemId}" ||
		divergences[0].Status != "422" || divergences[0].Media != "application/json" {
		t.Errorf("the divergence is keyed wrongly: %+v", divergences[0])
	}

	conflicts := divergences[0].Conflicts
	if len(conflicts) != 1 || conflicts[0].Path != "/detail" {
		t.Fatalf("got conflicts %+v, want one at /detail", conflicts)
	}
	want := []Sighting{
		{Kind: String, Phases: []string{"examples"}},
		{Kind: Array, Phases: []string{"fuzz", "coverage"}},
	}
	if fmt.Sprint(conflicts[0].Kinds) != fmt.Sprint(want) {
		t.Errorf("got %+v, want %+v — kinds in the order the run met them", conflicts[0].Kinds, want)
	}
}

// TestTwoMediaTypesAreTwoKeys is the first design constraint. Content
// negotiation is one status answering with two shapes on purpose, and a check
// that reports the intended behaviour of ordinary APIs is one people learn to
// scroll past.
func TestTwoMediaTypesAreTwoKeys(t *testing.T) {
	var recorder Recorder
	recorder.Record("examples", "GET /report", "200",
		map[string]string{"content-type": "application/json"}, `{"name":"monthly"}`)
	recorder.Record("examples", "GET /report", "200",
		map[string]string{"content-type": "application/vnd.example.v2+json"}, `["monthly"]`)

	if divergences := recorder.Divergences(); len(divergences) != 0 {
		t.Errorf("content negotiation was reported: %+v", divergences)
	}
}

// TestOneMediaTypeSpeltTwoWaysIsOneKey is the reverse. `application/json` and
// `application/json; charset=utf-8` are one media type, and splitting them
// would hide the finding behind a header nobody chose.
func TestOneMediaTypeSpeltTwoWaysIsOneKey(t *testing.T) {
	var recorder Recorder
	recorder.Record("examples", "GET /items/{itemId}", "422",
		map[string]string{"Content-Type": "application/json"}, `{"detail":"no item"}`)
	recorder.Record("fuzz", "GET /items/{itemId}", "422",
		map[string]string{"content-type": "APPLICATION/JSON; charset=UTF-8"}, `{"detail":[]}`)

	if divergences := recorder.Divergences(); len(divergences) != 1 {
		t.Errorf("got %d divergences, want 1 — the charset parameter split one key in two", len(divergences))
	}
}

// TestTwoStatusesAreTwoKeys: a 200 shaped unlike a 404 is not a defect, it is
// what a status code is for.
func TestTwoStatusesAreTwoKeys(t *testing.T) {
	var recorder Recorder
	recorder.Record("examples", "GET /items/{itemId}", "200", jsonHeaders, `{"item":{"id":1}}`)
	recorder.Record("examples", "GET /items/{itemId}", "404", jsonHeaders, `{"item":"not found"}`)

	if divergences := recorder.Divergences(); len(divergences) != 0 {
		t.Errorf("two statuses were compared against each other: %+v", divergences)
	}
}

// TestTwoOperationsAreTwoKeys: the same status from different operations says
// nothing, and there is no client that reads both with one parser anyway.
func TestTwoOperationsAreTwoKeys(t *testing.T) {
	var recorder Recorder
	recorder.Record("examples", "GET /items/{itemId}", "422", jsonHeaders, `{"detail":"no item"}`)
	recorder.Record("examples", "POST /orders", "422", jsonHeaders, `{"detail":[]}`)

	if divergences := recorder.Divergences(); len(divergences) != 0 {
		t.Errorf("two operations were compared against each other: %+v", divergences)
	}
}

// TestAResponseThatIsNotJSONIsNotRecorded: a CSV export and an image have no
// shape this could compare, and guessing at one would report the difference
// between two pictures.
func TestAResponseThatIsNotJSONIsNotRecorded(t *testing.T) {
	var recorder Recorder
	recorder.Record("examples", "GET /report", "200",
		map[string]string{"content-type": "text/csv"}, "name,total\na,1\n")
	recorder.Record("fuzz", "GET /report", "200",
		map[string]string{"content-type": "text/csv"}, "1,2,3\n")

	if divergences := recorder.Divergences(); len(divergences) != 0 {
		t.Errorf("a non-JSON body was given a shape: %+v", divergences)
	}
}

// TestDivergencesComeOutInAFixedOrder. A summary whose lines move between two
// runs of one suite cannot be diffed, and a report nobody can diff is one
// nobody checks.
func TestDivergencesComeOutInAFixedOrder(t *testing.T) {
	var recorder Recorder
	for _, operation := range []string{"POST /orders", "GET /items/{itemId}", "GET /report"} {
		for _, status := range []string{"500", "422"} {
			recorder.Record("examples", operation, status, jsonHeaders, `{"detail":"a"}`)
			recorder.Record("fuzz", operation, status, jsonHeaders, `{"detail":["a"]}`)
		}
	}

	var order []string
	for _, divergence := range recorder.Divergences() {
		order = append(order, divergence.Operation+" "+divergence.Status)
	}
	want := []string{
		"GET /items/{itemId} 422", "GET /items/{itemId} 500",
		"GET /report 422", "GET /report 500",
		"POST /orders 422", "POST /orders 500",
	}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", order, want)
	}
}

// TestTheRecorderTakesResponsesFromEveryWorkerAtOnce. A run with --workers
// sends from several goroutines, and the recorder is called from whichever one
// sent the request. Worth its own test because the failure is a crash in
// somebody's CI rather than a wrong answer, and only -race finds it early.
func TestTheRecorderTakesResponsesFromEveryWorkerAtOnce(t *testing.T) {
	var recorder Recorder
	var waiting sync.WaitGroup

	for worker := 0; worker < 8; worker++ {
		waiting.Add(1)
		go func(worker int) {
			defer waiting.Done()
			for i := 0; i < 50; i++ {
				body := `{"detail":"no item"}`
				if i%2 == 0 {
					body = `{"detail":[{"msg":"nope"}]}`
				}
				recorder.Record("fuzz", fmt.Sprintf("GET /items/%d", worker), "422", jsonHeaders, body)
			}
		}(worker)
	}
	waiting.Wait()

	if divergences := recorder.Divergences(); len(divergences) != 8 {
		t.Errorf("got %d divergences, want one per operation", len(divergences))
	}
}
