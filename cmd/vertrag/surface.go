package main

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
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

	// excludedStatus are the documented statuses left out of the figures,
	// per operation, with the reason. An operation whose every documented
	// status is excluded has nothing left to count and is left out whole.
	excludedStatus map[operation]map[string]reporter.SurfaceExclusion

	// transactions are each operation's documented transactions, by name and
	// the status each documents: what a passed operation must have passed.
	transactions map[operation][]surfaceTransaction

	// passed are the examples-phase transactions that passed with an answer
	// below 500, by name; failedWhy says, for the others, why not.
	//
	// Named examples (#85) send one name as several exchanges, so a name can
	// be both: one exchange passed and another did not. failedWhy wins, and
	// keeps the first reason, whatever order the outcomes arrived in (#103).
	passed    map[string]bool
	failedWhy map[string]string

	// plannedTransactions are the transactions a dry run would have sent.
	plannedTransactions map[string]bool

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

// surfaceTransaction is one documented transaction as a passed operation
// counts it.
type surfaceTransaction struct{ name, status string }

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

		excludedStatus:      map[operation]map[string]reporter.SurfaceExclusion{},
		transactions:        map[operation][]surfaceTransaction{},
		passed:              map[string]bool{},
		failedWhy:           map[string]string{},
		plannedTransactions: map[string]bool{},
		planned:             map[operation]bool{},
		why:                 map[operation][]string{},
		whyStatus:           map[operation]map[string][]string{},
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
		ledger.transactions[op] = append(ledger.transactions[op], surfaceTransaction{name: transaction.Name, status: status})
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
			if exclusion.Status == "" {
				matched = true
				ledger.excluded[op] = reporter.SurfaceExclusion{
					Operation: ledger.labels[op], Reason: exclusion.Reason, Source: "surface.exclude",
				}
				continue
			}
			for _, status := range ledger.documented[op] {
				if !statusExcludedBy(exclusion.Status, status) {
					continue
				}
				matched = true
				if ledger.excludedStatus[op] == nil {
					ledger.excludedStatus[op] = map[string]reporter.SurfaceExclusion{}
				}
				// The first entry naming a status gives the reason; a later,
				// broader one does not overwrite what was written for it.
				if _, already := ledger.excludedStatus[op][status]; !already {
					ledger.excludedStatus[op][status] = reporter.SurfaceExclusion{
						Operation: ledger.labels[op], Status: status, Reason: exclusion.Reason, Source: "surface.exclude",
					}
				}
			}
		}
		switch {
		case matched:
		case exclusion.Status != "":
			unmatched = append(unmatched, fmt.Sprintf(
				"surface.exclude names no documented status %q of %s, so it excludes nothing and the "+
					"status it meant still counts against the run; write a status the description documents, "+
					"`403`, a range, `4XX`, or a pattern, `40?`", exclusion.Status, exclusionTarget(exclusion.Operation)))
		default:
			unmatched = append(unmatched, fmt.Sprintf(
				"surface.exclude names no operation %q, so it excludes nothing and the operation it meant "+
					"still counts against the run; name it as the surface report does, `GET /path/{id}`, "+
					"or by its operationId", exclusion.Operation))
		}
	}
	return ledger, unmatched
}

func exclusionTarget(operation string) string {
	if operation == "" {
		return "any operation"
	}
	return fmt.Sprintf("an operation named %q", operation)
}

// names reports whether an exclusion's value names an operation: by the label
// the surface report prints, or by the operationId the description gives it.
// A `*` in the value matches any run of characters, in either; an empty value,
// which only a status exclusion may have, names every operation.
func (l *surfaceLedger) names(op operation, value string, transactions []compile.Transaction) bool {
	if value == "" {
		return true
	}
	match := func(candidate string) bool { return candidate == value }
	if strings.Contains(value, "*") {
		pattern := regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(value), `\*`, ".*") + "$")
		match = pattern.MatchString
	}
	if match(l.labels[op]) {
		return true
	}
	for _, transaction := range transactions {
		if transaction.OperationID != "" && match(transaction.OperationID) && operationOf(transaction) == op {
			return true
		}
	}
	return false
}

// statusExcludedBy reports whether an exclusion's status pattern matches a
// documented status. `default` matches only itself. Otherwise the pattern is
// read a character at a time, without regard to case: a digit is itself, `X`
// and `?` are any one character, and `*` any run of them — so `4XX` excludes
// a documented 403 and a documented 4XX alike, and `403` excludes only 403.
func statusExcludedBy(pattern, documented string) bool {
	if strings.EqualFold(pattern, "default") || strings.EqualFold(documented, "default") {
		return strings.EqualFold(pattern, documented)
	}
	var expression strings.Builder
	expression.WriteString("(?i)^")
	for _, r := range pattern {
		switch r {
		case 'X', 'x', '?':
			expression.WriteString(".")
		case '*':
			expression.WriteString(".*")
		default:
			expression.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	expression.WriteString("$")
	matched, err := regexp.MatchString(expression.String(), documented)
	return err == nil && matched
}

// exclusionReach counts what an exclusion names, for doctor: the operations,
// or for a status exclusion the documented statuses.
func exclusionReach(exclusion config.SurfaceExclusion, transactions []compile.Transaction) int {
	ledger, _ := newSurfaceLedger(transactions, nil)
	reached := 0
	for _, op := range ledger.order {
		if !ledger.names(op, exclusion.Operation, transactions) {
			continue
		}
		if exclusion.Status == "" {
			reached++
			continue
		}
		for _, status := range ledger.documented[op] {
			if statusExcludedBy(exclusion.Status, status) {
				reached++
			}
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
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, already := l.failedWhy[transaction.Name]; !already && reason != "" {
		l.failedWhy[transaction.Name] = reason
	}
}

// plan records that a dry run would have sent this transaction.
func (l *surfaceLedger) plan(transaction compile.Transaction) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.planned[operationOf(transaction)] = true
	l.plannedTransactions[transaction.Name] = true
}

// outcome records why the examples phase did, or did not, get the answer one
// transaction documents. Only the examples phase is asked, because it is the
// one that sends every selected transaction as the description wrote it; the
// probing phases send what they can generate for, and an operation they had
// nothing to probe in is not one they failed to reach.
func (l *surfaceLedger) outcome(transaction compile.Transaction, result runner.Result) {
	l.note(transaction, outcomeReason(result))

	l.mu.Lock()
	defer l.mu.Unlock()
	if passing, why := passedOutright(result); passing {
		l.passed[transaction.Name] = true
	} else if _, already := l.failedWhy[transaction.Name]; !already {
		l.failedWhy[transaction.Name] = why
	}
}

// passedOutright reports whether a transaction counts towards a passed
// operation, or says why not. It passed, and it was answered, and the answer
// was not a 5xx: a server error is the server failing whatever the
// description says it may do, so a 500 never counts as passed — not even one
// a documented 500 matched. Exclude that status if it is not meant to count.
func passedOutright(result runner.Result) (bool, string) {
	answer := strings.TrimSpace(result.Actual.StatusCode)
	code, _ := strconv.Atoi(answer)
	switch {
	case result.Status == runner.StatusPass && answer != "" && code < 500:
		return true, ""
	case result.Status == runner.StatusPass && answer != "":
		return false, "answered " + answer + ", which never counts as passed"
	case result.Status == runner.StatusPass:
		return false, "passed without an answer, which never counts as passed"
	case result.Status == runner.StatusFail && answer != "":
		return false, "answered " + answer + " and failed"
	}
	return false, outcomeReason(result)
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
func (l *surfaceLedger) summary(settings config.SurfaceSettings, dryRun bool) reporter.Surface {
	l.mu.Lock()
	defer l.mu.Unlock()

	count := settings.Count
	if count == "" {
		count = config.CountReached
	}
	surface := reporter.Surface{
		Required: settings.Require, Count: count, DryRun: dryRun,
		Phases: []reporter.SurfaceFigures{}, Missed: []reporter.SurfaceMiss{},
		Excluded: []reporter.SurfaceExclusion{}, NotPassed: []reporter.SurfaceMiss{},
	}

	// Each counted operation, with the documented statuses of it that count.
	// Both sides of every figure are taken from these, so an exclusion takes
	// the same thing out of what was reached and out of what there was.
	var counted []operation
	statusesOf := map[operation][]string{}
	for _, op := range l.order {
		if exclusion, excluded := l.excluded[op]; excluded {
			exclusion.Reached = l.reachedByRun(op, dryRun)
			surface.Excluded = append(surface.Excluded, exclusion)
			continue
		}
		var statuses []string
		for _, status := range l.documented[op] {
			if exclusion, excluded := l.excludedStatus[op][status]; excluded {
				exclusion.Reached = !dryRun && l.statusReachedByRun(op, status)
				surface.Excluded = append(surface.Excluded, exclusion)
				continue
			}
			statuses = append(statuses, status)
		}
		// Every documented status excluded leaves nothing to count, and an
		// operation with nothing to count is neither reached nor passed; it
		// is listed above, status by status, and left out of the figures.
		if len(statuses) == 0 {
			continue
		}
		counted = append(counted, op)
		statusesOf[op] = statuses
	}

	if !dryRun {
		for _, phase := range l.phases {
			figures := reporter.SurfaceFigures{Phase: phase}
			for _, op := range counted {
				answered := l.answered[phase][op]
				figures.Operations++
				figures.Statuses += len(statusesOf[op])
				if len(answered) > 0 {
					figures.OperationsReached++
				}
				for _, status := range statusesOf[op] {
					if reachedStatus(status, answered) {
						figures.StatusesReached++
					}
				}
				if phase == config.PhaseExamples {
					if passed, _ := l.operationPassed(op, statusesOf[op], false); passed {
						figures.OperationsPassed++
					}
				}
			}
			surface.Phases = append(surface.Phases, figures)
		}
	}

	surface.Run = reporter.SurfaceFigures{Phase: "run"}
	for _, op := range counted {
		surface.Run.Operations++
		surface.Run.Statuses += len(statusesOf[op])
		passed, why := l.operationPassed(op, statusesOf[op], dryRun)
		if passed {
			surface.Run.OperationsPassed++
		}
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
		if !passed {
			surface.NotPassed = append(surface.NotPassed, reporter.SurfaceMiss{Operation: l.labels[op], Reason: why})
		}
		for _, status := range statusesOf[op] {
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

// operationPassed reports whether every documented transaction of an
// operation whose status counts passed in the examples phase — or, for a dry
// run, would be sent — and when not, what kept it from passing.
func (l *surfaceLedger) operationPassed(op operation, statuses []string, dryRun bool) (bool, string) {
	var why []string
	for _, transaction := range l.transactions[op] {
		if !slices.Contains(statuses, transaction.status) {
			continue
		}
		reason, failed := l.failedWhy[transaction.name]
		if dryRun {
			if failed || !l.plannedTransactions[transaction.name] {
				why = append(why, "not sent")
			}
			continue
		}
		if !failed && l.passed[transaction.name] {
			continue
		}
		if reason == "" {
			reason = "not sent"
		}
		reason = transaction.status + " " + reason
		if !slices.Contains(why, reason) {
			why = append(why, reason)
		}
	}
	return len(why) == 0, strings.Join(why, "; ")
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
