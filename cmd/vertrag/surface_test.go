package main

import (
	"context"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// reach records one response, as the runner's observer would.
func reach(ledger *surfaceLedger, transaction compile.Transaction, status string) {
	ledger.observe(context.Background(), transaction, validate.Message{StatusCode: status})
}

func missFor(surface reporter.Surface, operation, status string) (reporter.SurfaceMiss, bool) {
	for _, miss := range surface.Missed {
		if miss.Operation == operation && miss.Status == status {
			return miss, true
		}
	}
	return reporter.SurfaceMiss{}, false
}

// TestAFilterNarrowsTheRunAndNotTheYardstick is the reason the ledger is built
// from every compiled transaction. A run narrowed to one tag that measured
// itself against that tag would reach a hundred percent of whatever it chose
// to test, which is the figure this exists to stop being misleading.
func TestAFilterNarrowsTheRunAndNotTheYardstick(t *testing.T) {
	widgets := documents("GET", "/widgets", "200")
	admin := documents("POST", "/admin/reindex", "202")
	ledger, _ := newSurfaceLedger(append(widgets, admin...), nil)
	ledger.leftOut(admin[0], "left out by exclude-tag admin")

	ledger.phase(config.PhaseExamples)
	reach(ledger, widgets[0], "200")

	surface := ledger.summary(config.SurfaceSettings{}, false)
	if surface.Run.Operations != 2 || surface.Run.OperationsReached != 1 {
		t.Fatalf("run = %+v, want 1 of 2 operations", surface.Run)
	}
	miss, found := missFor(surface, "POST /admin/reindex", "")
	if !found || miss.Reason != "left out by exclude-tag admin" {
		t.Errorf("the filtered operation is not listed with the filter that dropped it: %+v", surface.Missed)
	}
}

// TestAnyAnswerReachesAnOperation pins what "reached" means. A 500 is a
// failure, and it is reported as one elsewhere — but the request was answered,
// so the operation was exercised, and the status it documents was not.
func TestAnyAnswerReachesAnOperation(t *testing.T) {
	transactions := documents("GET", "/widgets/{id}", "200", "404")
	ledger, _ := newSurfaceLedger(transactions, nil)
	ledger.phase(config.PhaseExamples)

	reach(ledger, transactions[0], "500")
	ledger.outcome(transactions[0], runner.Result{Status: runner.StatusFail, Actual: validate.Message{StatusCode: "500"}})
	ledger.outcome(transactions[1], runner.Result{Status: runner.StatusFail, Actual: validate.Message{StatusCode: "500"}})

	surface := ledger.summary(config.SurfaceSettings{}, false)
	if surface.Run.OperationsReached != 1 {
		t.Errorf("an operation that answered was not counted as reached: %+v", surface.Run)
	}
	if surface.Run.StatusesReached != 0 || surface.Run.Statuses != 2 {
		t.Errorf("statuses = %d of %d, want 0 of 2", surface.Run.StatusesReached, surface.Run.Statuses)
	}
	for _, status := range []string{"200", "404"} {
		miss, found := missFor(surface, "GET /widgets/{id}", status)
		if !found || miss.Reason != "answered 500" {
			t.Errorf("%s: want it listed as answered 500, got %+v (found %v)", status, miss, found)
		}
	}
}

// TestEachPhaseHasItsOwnFiguresAndTheRunHasTheirUnion is the issue's first
// condition. The examples phase reaches the documented 200; the coverage phase
// reaches the 400 a refused probe gets. Neither phase alone reached both, and
// the run did.
func TestEachPhaseHasItsOwnFiguresAndTheRunHasTheirUnion(t *testing.T) {
	transactions := documents("POST", "/widgets", "201", "400")
	ledger, _ := newSurfaceLedger(transactions, nil)

	ledger.phase(config.PhaseExamples)
	reach(ledger, transactions[0], "201")
	ledger.phase(config.PhaseCoverage)
	reach(ledger, transactions[0], "400")

	surface := ledger.summary(config.SurfaceSettings{}, false)
	if len(surface.Phases) != 2 {
		t.Fatalf("phases = %+v, want examples and coverage", surface.Phases)
	}
	for i, want := range []string{config.PhaseExamples, config.PhaseCoverage} {
		got := surface.Phases[i]
		if got.Phase != want || got.StatusesReached != 1 || got.Statuses != 2 || got.OperationsReached != 1 {
			t.Errorf("phase %d = %+v, want %s reaching 1 of 2 statuses", i, got, want)
		}
	}
	if surface.Run.StatusesReached != 2 {
		t.Errorf("the run reached %d of its statuses, want both", surface.Run.StatusesReached)
	}
	if len(surface.Missed) != 0 {
		t.Errorf("nothing was missed by the run, yet: %+v", surface.Missed)
	}
}

// TestARangeIsReachedByAStatusInItsBand pins that the judgement is the one
// pass and fail use, so `2XX` is reached exactly when a 201 would satisfy it.
func TestARangeIsReachedByAStatusInItsBand(t *testing.T) {
	transactions := documents("GET", "/ranged", "2XX", "404")
	ledger, _ := newSurfaceLedger(transactions, nil)
	ledger.phase(config.PhaseExamples)
	reach(ledger, transactions[0], "201")

	surface := ledger.summary(config.SurfaceSettings{}, false)
	if surface.Run.StatusesReached != 1 {
		t.Errorf("a 201 should reach `2XX` and nothing else: %+v", surface.Run)
	}
	if _, found := missFor(surface, "GET /ranged", "404"); !found {
		t.Errorf("the 404 was reached by a 201: %+v", surface.Missed)
	}
}

// TestAStatusDocumentedTwiceIsOneStatusToReach is the media-type case: one
// status documented as JSON and as XML is two transactions and one thing a
// server can answer with.
func TestAStatusDocumentedTwiceIsOneStatusToReach(t *testing.T) {
	transactions := documents("GET", "/widgets", "200", "200")
	ledger, _ := newSurfaceLedger(transactions, nil)
	if surface := ledger.summary(config.SurfaceSettings{}, false); surface.Run.Statuses != 1 {
		t.Errorf("statuses = %d, want 1", surface.Run.Statuses)
	}
}

// TestAnExclusionIsListedApartAndNotCounted covers both places an exclusion
// can be written, which wins, and the two ways one can stop meaning anything:
// naming nothing, and being reached anyway.
func TestAnExclusionIsListedApartAndNotCounted(t *testing.T) {
	charge := documents("POST", "/billing/charge", "200")
	charge[0].Exclude = "charges a real card"
	reports := documents("GET", "/reports", "200")
	reports[0].OperationID = "listReports"
	widgets := documents("GET", "/widgets", "200")
	transactions := append(append(append([]compile.Transaction{}, charge...), reports...), widgets...)

	ledger, unmatched := newSurfaceLedger(transactions, []config.SurfaceExclusion{
		{Operation: "listReports", Reason: "needs a reporting backend"},
		{Operation: "GET /nowhere", Reason: "renamed since"},
	})
	if len(unmatched) != 1 || !strings.Contains(unmatched[0], `"GET /nowhere"`) {
		t.Errorf("an exclusion naming no operation was not reported: %v", unmatched)
	}

	ledger.phase(config.PhaseExamples)
	reach(ledger, charge[0], "200")
	reach(ledger, widgets[0], "200")

	surface := ledger.summary(config.SurfaceSettings{Require: 100}, false)
	if surface.Run.Operations != 1 || surface.Run.OperationsReached != 1 || !surface.Met() {
		t.Errorf("excluded operations were counted: %+v", surface.Run)
	}
	want := []reporter.SurfaceExclusion{
		{Operation: "POST /billing/charge", Reason: "charges a real card", Source: "x-vertrag-exclude", Reached: true},
		{Operation: "GET /reports", Reason: "needs a reporting backend", Source: "surface.exclude"},
	}
	if !reflect.DeepEqual(surface.Excluded, want) {
		t.Errorf("excluded =\n  %+v\nwant\n  %+v", surface.Excluded, want)
	}

	// The configuration wins over the description, because the file is what
	// this project decided and a description may be shared.
	both, _ := newSurfaceLedger(charge, []config.SurfaceExclusion{{Operation: "POST /billing/charge", Reason: "ours"}})
	if got := both.summary(config.SurfaceSettings{}, false).Excluded; len(got) != 1 || got[0].Source != "surface.exclude" {
		t.Errorf("the configuration's exclusion should win: %+v", got)
	}
}

// TestEveryWayATransactionIsNotAnsweredHasItsOwnReason is the list the issue
// asked for, each in the words the report prints.
func TestEveryWayATransactionIsNotAnsweredHasItsOwnReason(t *testing.T) {
	for _, test := range []struct {
		name   string
		result runner.Result
		want   string
	}{
		{"a hook", runner.Result{Status: runner.StatusSkip}, "a hook skipped it"},
		{"the configuration", runner.Result{Status: runner.StatusSkip,
			Errors: []string{"skipped by configuration: destroys shared state"}},
			"skipped by configuration: destroys shared state"},
		{"a link", runner.Result{Status: runner.StatusSkip,
			Errors: []string{"link GetWidget could not resolve id from the response of /widgets > Create"}},
			"link GetWidget could not resolve id from the response of /widgets > Create"},
		{"no answer", runner.Result{Status: runner.StatusError,
			Errors: []string{"GET http://127.0.0.1:1/widgets: connection refused\nmore"}},
			"no answer: GET http://127.0.0.1:1/widgets: connection refused"},
		{"a hook's failure", runner.Result{Status: runner.StatusFail, Errors: []string{"not today"}},
			"a hook failed it before it was sent: not today"},
		{"a different answer", runner.Result{Status: runner.StatusFail,
			Actual: validate.Message{StatusCode: "200"}}, "answered 200"},
	} {
		if got := outcomeReason(test.result); got != test.want {
			t.Errorf("%s: reason = %q, want %q", test.name, got, test.want)
		}
	}
}

// TestADryRunCountsWhatItWouldSend is the preflight: no status can be counted,
// and what the configuration takes out is known without a server.
func TestADryRunCountsWhatItWouldSend(t *testing.T) {
	widgets := documents("GET", "/widgets", "200")
	doomed := documents("DELETE", "/widgets/{id}", "204")
	ledger, _ := newSurfaceLedger(append(widgets, doomed...), nil)
	ledger.plan(widgets[0])
	ledger.leftOut(doomed[0], skipReasonText("destroys shared state"))

	surface := ledger.summary(config.SurfaceSettings{Require: 100}, true)
	if surface.Met() || surface.Run.OperationsReached != 1 || len(surface.Phases) != 0 {
		t.Errorf("dry run = %+v", surface)
	}
	if miss, found := missFor(surface, "DELETE /widgets/{id}", ""); !found ||
		miss.Reason != "skipped by configuration: destroys shared state" {
		t.Errorf("the skip is not the reason given: %+v", surface.Missed)
	}
}

// TestTheSummaryDoesNotDependOnTheOrderAnswersArrived is the determinism check.
//
// The coverage phase probes operations concurrently, so the order answers
// reach the ledger changes from run to run. The summary is walked in
// description order and must come out identical however they arrived — a
// report that reordered itself between two runs of one suite could not be
// diffed, and the diff is how anybody notices it shrinking.
func TestTheSummaryDoesNotDependOnTheOrderAnswersArrived(t *testing.T) {
	var transactions []compile.Transaction
	for _, path := range []string{"/a", "/b", "/c", "/d", "/e", "/f", "/g", "/h"} {
		transactions = append(transactions, documents("GET", path, "200", "400", "404")...)
	}

	summarise := func(seed uint64) reporter.Surface {
		ledger, _ := newSurfaceLedger(transactions, nil)
		ledger.phase(config.PhaseExamples)
		order := rand.New(rand.NewPCG(seed, seed)).Perm(len(transactions))
		var wg sync.WaitGroup
		for _, index := range order {
			// Every other operation answers, and only with some of its
			// statuses, so there are misses whose order could move.
			if index%2 == 0 || index%3 == 0 {
				continue
			}
			wg.Add(1)
			go func(transaction compile.Transaction) {
				defer wg.Done()
				reach(ledger, transaction, transaction.Response.Status)
			}(transactions[index])
		}
		wg.Wait()
		for _, index := range order {
			ledger.outcome(transactions[index], runner.Result{
				Status: runner.StatusFail, Actual: validate.Message{StatusCode: "200"},
			})
		}
		return ledger.summary(config.SurfaceSettings{}, false)
	}

	first := summarise(1)
	if len(first.Missed) == 0 {
		t.Fatal("the fixture produced no misses, so it proves nothing about their order")
	}
	for seed := uint64(2); seed < 20; seed++ {
		if got := summarise(seed); !reflect.DeepEqual(got, first) {
			t.Fatalf("the summary changed with the order answers arrived in:\n%+v\nvs\n%+v", first, got)
		}
	}
}

// TestTheReportNamesAnOperationWithoutItsQueryExpression is how the label is
// written in `surface.exclude`. The parser appends `{?limit*}` to a template so
// expansion knows the parameter exists, and nobody types that.
func TestTheReportNamesAnOperationWithoutItsQueryExpression(t *testing.T) {
	transaction := compile.Transaction{Request: compile.Request{Method: "GET", Template: "/search{?q*,limit*}"}}
	if got := surfaceLabel(transaction); got != "GET /search" {
		t.Errorf("label = %q, want GET /search", got)
	}
}

// TestADroppedTransactionIsBlamedOnTheFilterThatDroppedIt pins the wording of
// the two kinds of filter, which send a reader to different places.
func TestADroppedTransactionIsBlamedOnTheFilterThatDroppedIt(t *testing.T) {
	transactions := []compile.Transaction{
		{Name: "a", Request: compile.Request{Method: "GET"}, Tags: []string{"orders"}},
		{Name: "b", Request: compile.Request{Method: "DELETE"}, Tags: []string{"orders"}},
		{Name: "c", Request: compile.Request{Method: "GET"}, Tags: []string{"admin"}},
	}
	_, dropped, _, err := narrowTransactions(transactions, config.Config{
		Tag: []string{"orders"}, ExcludeMethod: []string{"DELETE"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, each := range dropped {
		got[each.transaction.Name] = each.reason
	}
	want := map[string]string{"b": "left out by exclude-method DELETE", "c": "not selected by tag"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reasons = %v, want %v", got, want)
	}
}

// TestAnOperationCanExcludeItselfAndOnlyWithAReason drives the parser, the
// compiler and the ledger together, from the description's own text.
func TestAnOperationCanExcludeItselfAndOnlyWithAReason(t *testing.T) {
	source := []byte(`openapi: 3.1.0
info: {title: Self, version: "1"}
paths:
  /charge:
    post:
      x-vertrag-exclude: "charges a real card"
      responses: {"200": {description: ok}}
  /vague:
    post:
      x-vertrag-exclude: true
      responses: {"200": {description: ok}}
`)
	result, _, err := transactionsFor(source, "api.yml", config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, transaction := range result.Transactions {
		reasons[transaction.Request.URI] = transaction.Exclude
	}
	if reasons["/charge"] != "charges a real card" || reasons["/vague"] != "" {
		t.Errorf("exclusion reasons = %v", reasons)
	}

	warned := false
	for _, annotation := range result.Annotations {
		if annotation.Type == "warning" && strings.Contains(annotation.Message, "x-vertrag-exclude") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("an exclusion with no reason was dropped without a warning: %+v", result.Annotations)
	}

	ledger, _ := newSurfaceLedger(stripAPIName(result.Transactions), nil)
	surface := ledger.summary(config.SurfaceSettings{}, false)
	if len(surface.Excluded) != 1 || surface.Excluded[0].Operation != "POST /charge" || surface.Run.Operations != 1 {
		t.Errorf("surface = %+v", surface)
	}
}

// TestAStatusPassesOnlyWhenEveryExchangeSentForItPassed is #103. Named
// examples turn one documented status into several exchanges that share one
// transaction name, so the ledger sees that name more than once. Whatever order
// their outcomes arrive in, one failed or left-out exchange keeps the status
// from passing, and its reason stays in the report.
func TestAStatusPassesOnlyWhenEveryExchangeSentForItPassed(t *testing.T) {
	pass := runner.Result{Status: runner.StatusPass, Actual: validate.Message{StatusCode: "200"}}
	fail := runner.Result{Status: runner.StatusFail, Actual: validate.Message{StatusCode: "404"}}
	for _, test := range []struct {
		name   string
		record func(*surfaceLedger, compile.Transaction)
		why    string
	}{
		{"a failure, then a pass", func(l *surfaceLedger, transaction compile.Transaction) {
			l.outcome(transaction, fail)
			l.outcome(transaction, pass)
		}, "200 answered 404 and failed"},
		{"a pass, then a failure", func(l *surfaceLedger, transaction compile.Transaction) {
			l.outcome(transaction, pass)
			l.outcome(transaction, fail)
		}, "200 answered 404 and failed"},
		{"left out, then a pass", func(l *surfaceLedger, transaction compile.Transaction) {
			l.leftOut(transaction, "left out by exclude-example missing")
			l.outcome(transaction, pass)
		}, "200 left out by exclude-example missing"},
	} {
		widgets := documents("GET", "/widgets", "200")
		ledger, _ := newSurfaceLedger(widgets, nil)
		ledger.phase(config.PhaseExamples)
		reach(ledger, widgets[0], "200")
		test.record(ledger, widgets[0])

		surface := ledger.summary(config.SurfaceSettings{}, false)
		if surface.Run.OperationsPassed != 0 {
			t.Errorf("%s: the status counts as passed: %+v", test.name, surface.Run)
		}
		if passed, why := ledger.operationPassed(operationOf(widgets[0]), []string{"200"}, false); passed || why != test.why {
			t.Errorf("%s: passed = %v, why = %q, want %q", test.name, passed, why, test.why)
		}
	}

	// A dry run is held to the same rule: an exchange the configuration takes
	// out is not planned because another exchange on its status is.
	widgets := documents("GET", "/widgets", "200")
	ledger, _ := newSurfaceLedger(widgets, nil)
	ledger.leftOut(widgets[0], skipReasonText("destroys shared state"))
	ledger.plan(widgets[0])
	if passed, _ := ledger.operationPassed(operationOf(widgets[0]), []string{"200"}, true); passed {
		t.Error("dry run: a skipped exchange counts as planned because another on its status is")
	}
}
