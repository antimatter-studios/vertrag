package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// A report lists what ran. It cannot list what did not, and what did not run
// is where a suite stops testing without anybody deciding it should: a filter
// written for one afternoon and never removed, a skip whose reason went stale,
// a hook that returns early, a link that stopped resolving when a field was
// renamed. Each of those leaves a run that is green and smaller, and nothing in
// it says so — the passing lines are all still true.
//
// So this keeps the other account: of every operation the description
// defines, and every status those operations document, what did each phase
// reach, and for what nothing reached, why. Like the status ledger beside it,
// it is bookkeeping and sends nothing. Unlike that one it can decide the exit
// code, because a run asked to reach a share of its description and falling
// short is not a fact about the document — it is the run failing to do what it
// was told to.

// surfaceLedger is the run-wide account of what each phase reached.
//
// Keyed per operation, as the status ledger is, and for the same reason: a
// transaction is one documented response, and "was this operation tested"
// is a question about all of them at once.
type surfaceLedger struct {
	mu sync.Mutex

	order  []operation
	labels map[operation]string

	// documented is every status the description gives an operation, without
	// repeats — a status documented in two media types is one status to
	// reach — in the order the description gives them.
	documented map[operation][]string

	// excluded are the operations left out of the figures, with the reason.
	excluded map[operation]reporter.SurfaceExclusion

	// answered is every status each operation answered with, per phase.
	// Recorded raw and judged at the end, so the judgement is made once and
	// by the rule that decides pass and fail.
	answered map[string]map[operation]map[string]bool

	// planned are the operations a dry run would have sent a request to.
	planned map[operation]bool

	// why holds, per operation and per documented status, the reasons the
	// run gave for not getting the answer: what filtered it out, what skipped
	// it, what came back instead. Distinct, in the order they arose.
	why       map[operation][]string
	whyStatus map[operation]map[string][]string

	current string
	phases  []string
}

// surfaceLabel is how the surface report names an operation: the method and
// the path template, as somebody would write it in `surface.exclude`.
//
// It is operationLabel without the query expression the parser appends to a
// template. That expression is how URI expansion learns which query parameters
// exist, and it is not part of any name a person uses for the operation —
// nobody excluding a search endpoint writes `{?q*,limit*,cursor*}` after it.
func surfaceLabel(transaction compile.Transaction) string {
	label := operationLabel(transaction)
	if transaction.GraphQL != nil {
		return label
	}
	if at := strings.Index(label, "{?"); at >= 0 {
		label = label[:at]
	}
	return label
}

// newSurfaceLedger reads the operations a description defines and the statuses
// they document, and which of them are excluded.
//
// It takes every compiled transaction, including the ones this run will not
// send: what the description defines is the whole of what the run is measured
// against, and a filter must narrow the run without narrowing the yardstick —
// otherwise `--tag one` would reach a hundred percent of everything it did.
//
// It returns, with the ledger, every configured exclusion that names no
// operation. Such an entry excludes nothing, so the operation its author
// meant still counts against the run while the file reads as though it does
// not — the shape of every silent setting this tool has had.
func newSurfaceLedger(transactions []compile.Transaction, exclusions []config.SurfaceExclusion) (*surfaceLedger, []string) {
	ledger := &surfaceLedger{
		labels:     map[operation]string{},
		documented: map[operation][]string{},
		excluded:   map[operation]reporter.SurfaceExclusion{},
		answered:   map[string]map[operation]map[string]bool{},
		planned:    map[operation]bool{},
		why:        map[operation][]string{},
		whyStatus:  map[operation]map[string][]string{},
	}

	for _, transaction := range transactions {
		op := operationOf(transaction)
		if _, known := ledger.documented[op]; !known {
			ledger.order = append(ledger.order, op)
			ledger.labels[op] = surfaceLabel(transaction)
			ledger.documented[op] = nil
		}
		status := strings.TrimSpace(transaction.Response.Status)
		if !slices.Contains(ledger.documented[op], status) {
			ledger.documented[op] = append(ledger.documented[op], status)
		}
		// The description's own exclusion. Written on the operation, so any
		// of its transactions carries it; the first is as good as any.
		if transaction.Exclude != "" {
			if _, already := ledger.excluded[op]; !already {
				ledger.excluded[op] = reporter.SurfaceExclusion{
					Operation: ledger.labels[op], Reason: transaction.Exclude, Source: "x-vertrag-exclude",
				}
			}
		}
	}

	// The configuration's exclusions, which win over the description's when
	// both name an operation: the file is what this project decided, and the
	// description may be shared by several.
	var unmatched []string
	for _, exclusion := range exclusions {
		matched := false
		for _, op := range ledger.order {
			if !ledger.names(op, exclusion.Operation, transactions) {
				continue
			}
			matched = true
			ledger.excluded[op] = reporter.SurfaceExclusion{
				Operation: ledger.labels[op], Reason: exclusion.Reason, Source: "surface.exclude",
			}
		}
		if !matched {
			unmatched = append(unmatched, fmt.Sprintf(
				"surface.exclude names no operation %q, so it excludes nothing and the operation it meant "+
					"still counts against the run; name it as the surface report does, `GET /path/{id}`, "+
					"or by its operationId", exclusion.Operation))
		}
	}
	return ledger, unmatched
}

// names reports whether an exclusion's value names an operation: by the label
// the surface report prints, or by the operationId the description gives it.
func (l *surfaceLedger) names(op operation, value string, transactions []compile.Transaction) bool {
	if value == l.labels[op] {
		return true
	}
	for _, transaction := range transactions {
		if transaction.OperationID != "" && transaction.OperationID == value && operationOf(transaction) == op {
			return true
		}
	}
	return false
}

// exclusionReach counts the operations an exclusion value names, for doctor.
func exclusionReach(value string, transactions []compile.Transaction) int {
	ledger, _ := newSurfaceLedger(transactions, nil)
	reached := 0
	for _, op := range ledger.order {
		if ledger.names(op, value, transactions) {
			reached++
		}
	}
	return reached
}

// phase records which phase the run is now in. Phases follow one another, but
// a phase's own workers do not, so this takes the lock the recording does.
func (l *surfaceLedger) phase(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.current = name
	if !slices.Contains(l.phases, name) {
		l.phases = append(l.phases, name)
	}
}

// observe records that an operation answered. It is installed as one of the
// runner's observers, which are called from the single point that touches the
// wire, so every phase's requests arrive here through whichever door they took
// — and only requests that were answered, which is the definition of reached.
func (l *surfaceLedger) observe(_ context.Context, source compile.Transaction, reply validate.Message) {
	status := strings.TrimSpace(reply.StatusCode)
	if status == "" {
		return
	}
	op := operationOf(source)

	l.mu.Lock()
	defer l.mu.Unlock()
	// An operation the ledger was not built with cannot be counted against a
	// description it has no entry for; see statusLedger.observe for why that
	// should not happen and why reporting it would be wrong if it did.
	if _, known := l.documented[op]; !known {
		return
	}
	byOperation := l.answered[l.current]
	if byOperation == nil {
		byOperation = map[operation]map[string]bool{}
		l.answered[l.current] = byOperation
	}
	if byOperation[op] == nil {
		byOperation[op] = map[string]bool{}
	}
	byOperation[op][status] = true
}

// leftOut records why a transaction was not going to be sent at all.
func (l *surfaceLedger) leftOut(transaction compile.Transaction, reason string) {
	l.note(transaction, reason)
}

// plan records that a dry run would have sent this transaction.
func (l *surfaceLedger) plan(transaction compile.Transaction) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.planned[operationOf(transaction)] = true
}

// outcome records why the examples phase did, or did not, get the answer one
// transaction documents. Only the examples phase is asked, because it is the
// one that sends every selected transaction as the description wrote it; the
// probing phases send what they can generate for, and an operation they had
// nothing to probe in is not one they failed to reach.
func (l *surfaceLedger) outcome(transaction compile.Transaction, result runner.Result) {
	l.note(transaction, outcomeReason(result))
}

// outcomeReason says what became of a transaction, in the words the surface
// report lists it with.
func outcomeReason(result runner.Result) string {
	first := func() string {
		if len(result.Errors) == 0 {
			return ""
		}
		return strings.SplitN(result.Errors[0], "\n", 2)[0]
	}
	switch {
	case result.Actual.StatusCode != "":
		return "answered " + strings.TrimSpace(result.Actual.StatusCode)
	case result.Status == runner.StatusSkip && first() == "":
		return "a hook skipped it"
	case result.Status == runner.StatusSkip:
		// Whatever the runner recorded: a skip from the configuration with its
		// reason, a sequenced step whose link could not resolve, the failure
		// budget spent, the run cancelled.
		return first()
	case result.Status == runner.StatusError:
		return "no answer: " + first()
	case result.Status == runner.StatusFail:
		return "a hook failed it before it was sent: " + first()
	}
	return "not sent"
}

func (l *surfaceLedger) note(transaction compile.Transaction, reason string) {
	if reason == "" {
		return
	}
	op := operationOf(transaction)
	status := strings.TrimSpace(transaction.Response.Status)

	l.mu.Lock()
	defer l.mu.Unlock()
	if !slices.Contains(l.why[op], reason) {
		l.why[op] = append(l.why[op], reason)
	}
	if l.whyStatus[op] == nil {
		l.whyStatus[op] = map[string][]string{}
	}
	if !slices.Contains(l.whyStatus[op][status], reason) {
		l.whyStatus[op][status] = append(l.whyStatus[op][status], reason)
	}
}

// summary is the account the reporters render and the gate reads.
//
// Everything is walked in description order — operations as the description
// lists them, statuses as each operation lists them, phases as the run ran
// them — and nothing is read out of a map in the map's order. The coverage
// phase probes operations concurrently, and a report that reordered itself
// between two runs of one suite could not be diffed.
func (l *surfaceLedger) summary(required float64, dryRun bool) reporter.Surface {
	l.mu.Lock()
	defer l.mu.Unlock()

	surface := reporter.Surface{
		Required: required, DryRun: dryRun,
		Phases: []reporter.SurfaceFigures{}, Missed: []reporter.SurfaceMiss{},
		Excluded: []reporter.SurfaceExclusion{},
	}

	var counted []operation
	for _, op := range l.order {
		if exclusion, excluded := l.excluded[op]; excluded {
			exclusion.Reached = l.reachedByRun(op, dryRun)
			surface.Excluded = append(surface.Excluded, exclusion)
			continue
		}
		counted = append(counted, op)
	}

	if !dryRun {
		for _, phase := range l.phases {
			figures := reporter.SurfaceFigures{Phase: phase}
			for _, op := range counted {
				answered := l.answered[phase][op]
				figures.Operations++
				figures.Statuses += len(l.documented[op])
				if len(answered) > 0 {
					figures.OperationsReached++
				}
				for _, status := range l.documented[op] {
					if reachedStatus(status, answered) {
						figures.StatusesReached++
					}
				}
			}
			surface.Phases = append(surface.Phases, figures)
		}
	}

	surface.Run = reporter.SurfaceFigures{Phase: "run"}
	for _, op := range counted {
		surface.Run.Operations++
		surface.Run.Statuses += len(l.documented[op])
		if !l.reachedByRun(op, dryRun) {
			surface.Missed = append(surface.Missed, reporter.SurfaceMiss{
				Operation: l.labels[op], Reason: reasons(l.why[op], "no request to it was answered"),
			})
			continue
		}
		surface.Run.OperationsReached++
		if dryRun {
			continue
		}
		for _, status := range l.documented[op] {
			if l.statusReachedByRun(op, status) {
				surface.Run.StatusesReached++
				continue
			}
			surface.Missed = append(surface.Missed, reporter.SurfaceMiss{
				Operation: l.labels[op], Status: status,
				Reason: reasons(l.whyStatus[op][status], "no answer carried it"),
			})
		}
	}
	return surface
}

// reachedByRun reports whether any phase reached an operation, or for a dry
// run whether a run would have sent it anything.
func (l *surfaceLedger) reachedByRun(op operation, dryRun bool) bool {
	if dryRun {
		return l.planned[op]
	}
	for _, phase := range l.phases {
		if len(l.answered[phase][op]) > 0 {
			return true
		}
	}
	return false
}

func (l *surfaceLedger) statusReachedByRun(op operation, status string) bool {
	for _, phase := range l.phases {
		if reachedStatus(status, l.answered[phase][op]) {
			return true
		}
	}
	return false
}

// reachedStatus reports whether a documented status was among the answers.
//
// The judgement is validate.StatusMatches, the rule that decides whether a
// transaction passed, so that `2XX` is reached by a 201 here exactly when a
// 201 would satisfy it there. A second rule would eventually disagree with the
// first, and the disagreement would show as a status the run passed on and
// still reported unreached.
func reachedStatus(documented string, answered map[string]bool) bool {
	for status := range answered {
		if validate.StatusMatches(documented, status) {
			return true
		}
	}
	return false
}

// reasons joins what the run recorded about a miss, or says the fallback when
// it recorded nothing.
func reasons(recorded []string, fallback string) string {
	if len(recorded) == 0 {
		return fallback
	}
	return strings.Join(recorded, "; ")
}
