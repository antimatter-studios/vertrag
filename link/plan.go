// Package link orders a run by the sequences its description declares.
//
// Every test vertrag runs is otherwise a single request in isolation: each
// transaction is compiled, sent and judged on its own. That leaves a whole
// class of behaviour untestable — anything needing a sequence, where a later
// request depends on something an earlier one returned.
//
//	POST /users        -> 201 {"id": 42}
//	GET  /users/42     -> 200          # needs the id the POST returned
//	DELETE /users/42   -> 204
//
// Run flat, the second and third send whatever identifier the description used
// as an example, get 404s, and report two failures that say nothing about the
// server. OpenAPI describes exactly this with the Link Object, and Dredd
// supports none of it — its own documentation says it "probably isn't the best
// tool" for scenarios and points at hand-written hooks with a global variable.
//
// # The decision that keeps this small
//
// Links do not create tests. They reorder the tests that already exist and fill
// in the values the description could not have known.
//
// So a plan has exactly as many steps as the flat list has transactions, and
// each appears exactly once. Everything follows from that. Termination is
// trivial. Cost is unchanged. Result counts and hook names are unchanged, so a
// CI dashboard does not shift. A cycle in the link graph cannot cause a problem
// because the second visit to a transaction is simply not an edge.
//
// The alternative — appending a step per link — makes `GET /users/{id}` run
// twice, once flat and uselessly 404ing and once in a chain, and turns half the
// report into noise. Schemathesis appends because Schemathesis is exploring;
// `vertrag run` is a regression gate and must not.
package link

import (
	"sort"
	"strings"

	"github.com/antimatter-studios/vertrag/compile"
)

// Step is one transaction in the order a plan runs it.
type Step struct {
	// Index is the transaction's position in the original flat list, which is
	// what the runner addresses it by.
	Index int

	// After is the step this one depends on, or -1 when it stands alone. A step
	// whose dependency failed cannot run: its parameters were to be filled from
	// a response that never arrived.
	After int

	// Via names the link that created the dependency, for a report that has to
	// explain why a step was skipped.
	Via string
}

// Plan is a run's order, with the dependencies between its steps.
type Plan struct {
	Steps []Step

	// Notes records what the plan could not do: a link naming an operation the
	// description does not define, or one vertrag cannot follow. They are
	// diagnostics about the document, not failures of the server, and belong in
	// the same place as a parser warning.
	Notes []string
}

// Build orders transactions by the links between them.
//
// Order within the plan is the document's order, broken only where a link
// requires it. That matters for reading a report: a run that reorders more than
// it must is one nobody can follow back to the description.
func Build(transactions []compile.Transaction) Plan {
	plan := Plan{Steps: make([]Step, 0, len(transactions))}

	byOperation := map[string][]int{}
	for i, transaction := range transactions {
		if transaction.OperationID != "" {
			byOperation[transaction.OperationID] = append(byOperation[transaction.OperationID], i)
		}
	}

	// The edges a link asks for: target index -> the step it must follow.
	after := make([]int, len(transactions))
	via := make([]string, len(transactions))
	for i := range after {
		after[i] = -1
	}

	for source, transaction := range transactions {
		for _, l := range transaction.Links {
			targets, note := resolveTarget(l, byOperation)
			if note != "" {
				plan.Notes = append(plan.Notes, note)
				continue
			}

			for _, target := range targets {
				switch {
				case target == source:
					// An operation linking to itself would depend on its own
					// response. There is no order that satisfies that.
					continue
				case after[target] != -1:
					// Already claimed. The first link in document order wins,
					// so the plan does not depend on map iteration and two runs
					// of the same description order the same way.
					continue
				case wouldCycle(after, source, target):
					plan.Notes = append(plan.Notes, "link "+l.Name+
						" would make the order circular, so it was not followed")
					continue
				}
				after[target] = source
				via[target] = l.Name
			}
		}
	}

	for i := range transactions {
		plan.Steps = append(plan.Steps, Step{Index: i, After: after[i], Via: via[i]})
	}

	plan.Steps = order(plan.Steps)
	return plan
}

// resolveTarget finds the transactions a link points at.
//
// One operation can compile to several transactions — a document offering two
// response content types describes two exchanges of the same operation — and a
// link naming it means all of them.
func resolveTarget(l compile.Link, byOperation map[string][]int) ([]int, string) {
	switch {
	case l.OperationID != "":
		targets, found := byOperation[l.OperationID]
		if !found {
			return nil, "link " + l.Name + " names operation " + l.OperationID +
				", which this description does not define"
		}
		return targets, ""

	case l.OperationRef != "":
		// An operationRef is a JSON Reference to the operation. Following one
		// means resolving a pointer into the document that produced these
		// transactions, which the compiled form no longer carries.
		return nil, "link " + l.Name + " uses operationRef, which vertrag does not follow yet; " +
			"give the target an operationId to sequence it"

	default:
		return nil, "link " + l.Name + " names no target operation"
	}
}

// wouldCycle reports whether making target depend on source closes a loop.
//
// Walking the existing chain upward from source is enough: each step has at
// most one dependency, so the graph is a forest and a cycle can only be closed
// by reaching the target again.
func wouldCycle(after []int, source, target int) bool {
	for at := source; at != -1; at = after[at] {
		if at == target {
			return true
		}
	}
	return false
}

// order sorts steps so a dependency always precedes what depends on it, and
// otherwise leaves document order alone.
func order(steps []Step) []Step {
	depth := make(map[int]int, len(steps))
	after := make(map[int]int, len(steps))
	for _, step := range steps {
		after[step.Index] = step.After
	}

	// A step's depth is how many dependencies stand between it and a root. A
	// forest with no cycles — which wouldCycle guarantees — always terminates.
	var depthOf func(index int) int
	depthOf = func(index int) int {
		if known, seen := depth[index]; seen {
			return known
		}
		parent, exists := after[index]
		if !exists || parent == -1 {
			depth[index] = 0
			return 0
		}
		// Marked before recursing so a graph that somehow still contains a loop
		// terminates rather than overflowing the stack.
		depth[index] = 0
		value := depthOf(parent) + 1
		depth[index] = value
		return value
	}

	sorted := make([]Step, len(steps))
	copy(sorted, steps)
	sort.SliceStable(sorted, func(i, j int) bool {
		return depthOf(sorted[i].Index) < depthOf(sorted[j].Index)
	})
	return sorted
}

// Values resolves a link's parameters against a completed exchange.
//
// The returned map is keyed as the description wrote it — bare or qualified —
// and the caller decides which of the target's parameters each one names.
//
// A parameter that cannot be resolved is left out rather than blanked. A link
// that cannot be followed must leave its target alone: sending an empty string
// where an identifier belongs makes the request go somewhere the description
// never described, and the resulting failure points nowhere near the cause.
func Values(l compile.Link, exchange Exchange) (map[string]any, []string) {
	if len(l.Parameters) == 0 {
		return nil, nil
	}

	values := map[string]any{}
	var unresolved []string

	// Sorted so two runs of the same description resolve in the same order and
	// a report reads the same way twice.
	names := make([]string, 0, len(l.Parameters))
	for name := range l.Parameters {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		value, ok := Evaluate(l.Parameters[name], exchange)
		if !ok {
			unresolved = append(unresolved, name)
			continue
		}
		values[name] = value
	}
	return values, unresolved
}

// Match reports whether a link parameter names a given parameter of the target.
//
// A key may be bare or qualified with the location the value is for, because
// one operation can have a query and a path parameter of the same name.
func Match(key, in, name string) bool {
	if key == name {
		return true
	}
	return strings.EqualFold(key, in+"."+name)
}
