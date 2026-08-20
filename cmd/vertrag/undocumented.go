package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/fuzz"
	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
)

// The defect this exists for is the one the two APIs vertrag is used against
// have in common, and it is not a broken handler: the document does not
// describe reality. One of them documents `200` and nothing else on 27
// operations whose handlers answer 401, 404 and 409 — so a client reading the
// description cannot know those exist, let alone what shape they arrive in.
//
// A run already provokes every one of them. The examples phase sends the
// documented request, the probing phases send generated ones, the stateful
// phase follows a chain through — and hundreds of responses arrive, are judged
// one at a time against the single response variant that transaction stood
// for, and are then thrown away. Nothing ever asked the run-wide question:
// across everything this operation answered, what did the description never
// mention?
//
// So this is bookkeeping, not a probe. It sends nothing, and it changes no
// verdict: what it reports is a defect in the DOCUMENT, and a document defect
// must not turn a green pipeline red — that is how a team learns to pass
// --no-verify. It prints, and the exit code is decided elsewhere.

// statusLedger is the run-wide account of what each operation answered, and
// what its description said it would.
//
// Keyed per OPERATION rather than per transaction, and that is the whole
// design. A transaction is one documented response — a description yielding a
// 200, a 404 and a 500 for one endpoint yields three of them — so "was this
// status expected" asked of a transaction can only mean "was it THIS
// transaction's status", which is the question validation already answers and
// fails the run over. The question here is different and larger: of everything
// the operation returned, what does its description not admit to anywhere?
type statusLedger struct {
	mu sync.Mutex

	// documented is every status the description gives an operation, over all
	// of its response variants. Built from the compiled transactions before
	// filtering, so `--only` narrowing a run cannot make the description look
	// less complete than it is.
	documented map[operation][]string

	labels map[operation]string
	order  []operation

	seen map[operation]map[string]*sighting

	// serverErrors counts the 5xx answers deliberately left out — see observe.
	serverErrors int

	current string
	phases  []string
}

// operation identifies an operation independently of which response variant a
// transaction is: the method, and whatever operationKey says names the
// operation. Both halves are needed — one path serves GET and DELETE, and
// their documented responses have nothing to do with each other.
type operation struct{ method, key string }

func operationOf(transaction compile.Transaction) operation {
	return operation{transaction.Request.Method, operationKey(transaction)}
}

// operationLabel is how the report names an operation. A GraphQL operation's
// key is already its root field, and prefixing every one of them with the POST
// and the single path they all share would say nothing and hide the field.
func operationLabel(transaction compile.Transaction) string {
	if transaction.GraphQL != nil {
		return operationKey(transaction)
	}
	return transaction.Request.Method + " " + operationKey(transaction)
}

// sighting is one status one operation returned, and everything the report
// needs to say about it.
type sighting struct {
	count  int
	phases map[string]bool

	// How the request that provoked it was composed. The three are kept apart
	// rather than reduced to a flag because they are not equally good
	// evidence — see report.
	fromDocumented bool
	fromValid      bool
	fromInvalid    bool
}

// newStatusLedger reads what the description documents, per operation.
//
// It takes every compiled transaction, including the ones this run will not
// send. What an operation documents is a property of the document; a run
// narrowed by `--only` or `--tag` still reads it whole, or excluding the 404
// variant from a run would make its 404 look undocumented.
func newStatusLedger(transactions []compile.Transaction) *statusLedger {
	ledger := &statusLedger{
		documented: map[operation][]string{},
		labels:     map[operation]string{},
		seen:       map[operation]map[string]*sighting{},
	}
	for _, transaction := range transactions {
		op := operationOf(transaction)
		if _, known := ledger.documented[op]; !known {
			ledger.order = append(ledger.order, op)
			ledger.labels[op] = operationLabel(transaction)
		}
		ledger.documented[op] = append(ledger.documented[op],
			strings.TrimSpace(transaction.Response.Status))
	}
	return ledger
}

// phase records which phase the run is now in, so a sighting can say where it
// came from. Phases run one after another, but a phase's own workers do not,
// so this takes the same lock the recording does.
func (l *statusLedger) phase(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.current = name
	for _, seen := range l.phases {
		if seen == name {
			return
		}
	}
	l.phases = append(l.phases, name)
}

// observe records one response. It is installed as the runner's Observe hook,
// which is called from the single point in the runner that touches the wire —
// see Runner.Observe for why it is there and not at each phase's call site.
func (l *statusLedger) observe(ctx context.Context, source compile.Transaction, reply validate.Message) {
	status := strings.TrimSpace(reply.StatusCode)
	if status == "" {
		return
	}
	op := operationOf(source)

	l.mu.Lock()
	defer l.mu.Unlock()

	// An operation the ledger does not know cannot be judged against a
	// description it has no entry for. It should not happen — the ledger is
	// built from every compiled transaction, and a generated request keeps the
	// template its operation was keyed on — so treating it as "documents
	// nothing" would report every status of it, turning a bug of ours into a
	// page of findings about somebody's API.
	if _, known := l.documented[op]; !known {
		return
	}

	// A 5xx is already a finding wherever it lands: the runner's server-error
	// check fails the documented transaction, and both probing phases report
	// one as a finding in its own right. Listing it here as well would give
	// one event two entries at two severities, and the weaker of the two — a
	// documentation note that changes no exit code — is the one a reader
	// reaches first. Counted, so the section can say what it left out.
	if code, err := strconv.Atoi(status); err == nil && code >= 500 {
		if !l.documents(op, status) {
			l.serverErrors++
		}
		return
	}

	if l.documents(op, status) {
		return
	}

	byStatus := l.seen[op]
	if byStatus == nil {
		byStatus = map[string]*sighting{}
		l.seen[op] = byStatus
	}
	entry := byStatus[status]
	if entry == nil {
		entry = &sighting{phases: map[string]bool{}}
		byStatus[status] = entry
	}
	entry.count++
	entry.phases[l.current] = true

	// Absent means the request carried content the description itself
	// supplied: an example, a probing phase's baseline, a step of a chain.
	// Only generation marks the context — see fuzz.ModeOf.
	switch mode, generated := fuzz.ModeOf(ctx); {
	case !generated:
		entry.fromDocumented = true
	case mode == generate.Valid:
		entry.fromValid = true
	default:
		entry.fromInvalid = true
	}
}

// documents reports whether the description admits this status anywhere among
// an operation's response variants.
//
// The judgement is validate.StatusMatches and nothing else, so that "does the
// description cover this" has one answer in the whole tool. It already knows
// that `2XX` documents a 201, and it is what decides pass or fail, so a second
// rule here would eventually disagree with it — and the disagreement would
// show as a status reported undocumented while the transaction expecting it
// passed.
//
// What it cannot answer is `default`, and that is not a limitation of
// StatusMatches. An OpenAPI `default` response is dropped at the parse stage —
// see parseResponses in apidesc/openapi3, which says why — and the compiler
// substitutes 200, so by the time anything downstream can ask, the catch-all
// is gone and indistinguishable from an explicit 200. An operation whose only
// error entry is `default` will therefore have its errors reported here as
// undocumented. Carrying the catch-all through would change the compiled
// shape, which is the oracle's comparison surface, and that is a decision of
// its own rather than a detail of this one.
func (l *statusLedger) documents(op operation, status string) bool {
	for _, expected := range l.documented[op] {
		if validate.StatusMatches(expected, status) {
			return true
		}
	}
	return false
}

// undocumented is one line of the report.
type undocumented struct {
	label  string
	status string
	count  int
	phases string
}

// report prints what the description never mentioned, or nothing at all.
//
// Two groups, because an undocumented status is not one kind of thing:
//
//   - A status reached by a request the description describes, or by a value
//     its own schema permits, is a straightforward defect in the document. The
//     API does this on the ordinary path and never says so.
//   - A status reached only by deliberately invalid input is weaker. The
//     server is very likely right to refuse, and vertrag went looking for the
//     refusal. It is still worth a line — a client that cannot learn from the
//     document whether a bad field yields 400 or 422 discovers it in
//     production — but it must not be printed beside the first kind. A probing
//     run produces hundreds of these and two or three of the others, and a
//     reader given one undifferentiated list reads the top of it and stops.
//
// The split is drawn on how the request was composed rather than on the phase,
// because the phase is the wrong axis: the probing phases send the documented
// example too, as their baseline, and a 409 from THAT is as strong as anything
// the examples phase saw. The phases are named beside each entry anyway, since
// "only the stateful phase ever saw this" tells a reader where to look.
//
// One caveat worth stating rather than hiding, because it decides how much a
// line in the first group is worth: generation can produce anything a caller
// must SHAPE and nothing a caller must POSSESS — the same rule refusals.isLogin
// turns on. A valid drawn identifier names no real row, so a 404 provoked by
// one is real behaviour of the API but not behaviour of its happy path, and it
// is grouped with the strong evidence only because the input satisfied the
// schema the description published.
//
// Nothing is printed when nothing was found. A line on every clean run saying
// a clean run happened is a line readers learn to skip, and this section has
// to be worth reading on the run where it is not empty.
func (l *statusLedger) report(w io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var confirmed, provoked []undocumented
	for _, op := range l.order {
		byStatus := l.seen[op]
		if len(byStatus) == 0 {
			continue
		}
		statuses := make([]string, 0, len(byStatus))
		for status := range byStatus {
			statuses = append(statuses, status)
		}
		sort.Strings(statuses)

		for _, status := range statuses {
			entry := byStatus[status]
			line := undocumented{
				label: l.labels[op], status: status, count: entry.count,
				phases: strings.Join(l.phasesOf(entry), ", "),
			}
			if entry.fromDocumented || entry.fromValid {
				confirmed = append(confirmed, line)
				continue
			}
			provoked = append(provoked, line)
		}
	}
	if len(confirmed) == 0 && len(provoked) == 0 {
		return
	}

	fmt.Fprint(w, "\nStatuses returned that the description does not document\n")
	if len(confirmed) > 0 {
		fmt.Fprint(w, "\n  Reached by a request the description describes, or by input its own schema\n"+
			"  permits. The document is wrong about its own operation:\n")
		writeUndocumented(w, confirmed)
	}
	if len(provoked) > 0 {
		fmt.Fprint(w, "\n  Reached only by deliberately invalid input. Refusing it may well be correct,\n"+
			"  but the description never says what a refusal looks like:\n")
		writeUndocumented(w, provoked)
	}
	if l.serverErrors > 0 {
		fmt.Fprintf(w, "\n  %d undocumented 5xx answer(s) are not listed: a server error is reported as a\n"+
			"  finding in its own right, and one event with two severities gets acted on at\n"+
			"  the lower one.\n", l.serverErrors)
	}
	fmt.Fprint(w, "\n  This is a report about the description and does not affect the exit code.\n")
}

// phasesOf names the phases that saw a status, in the order the run ran them
// rather than alphabetically — the coverage phase probes operations
// concurrently, so first-seen order within a phase is stable but across a set
// it is not, and a section that reordered itself between two runs of one suite
// could not be diffed.
func (l *statusLedger) phasesOf(entry *sighting) []string {
	var names []string
	for _, phase := range l.phases {
		if entry.phases[phase] {
			names = append(names, phase)
		}
	}
	return names
}

// writeUndocumented lines the operations up so the statuses read as a column.
// A report whose second field starts somewhere different on every line is one
// nobody can scan for the status they are chasing.
func writeUndocumented(w io.Writer, lines []undocumented) {
	width := 0
	for _, line := range lines {
		if len(line.label) > width {
			width = len(line.label)
		}
	}
	for _, line := range lines {
		fmt.Fprintf(w, "    %-*s  %s  x%-4d %s\n", width, line.label, line.status, line.count, line.phases)
	}
}
