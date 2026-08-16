package link

import (
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
)

// transaction is a compiled transaction reduced to what a plan reads.
func transaction(operationID string, links ...compile.Link) compile.Transaction {
	return compile.Transaction{Name: operationID, OperationID: operationID, Links: links}
}

func linkTo(name, target string) compile.Link {
	return compile.Link{
		Name:        name,
		OperationID: target,
		Parameters:  map[string]string{"id": "$response.body#/id"},
	}
}

// indexes renders a plan's order as the operation names it runs, which is what
// a test actually wants to assert.
func indexes(plan Plan, transactions []compile.Transaction) []string {
	var out []string
	for _, step := range plan.Steps {
		out = append(out, transactions[step.Index].OperationID)
	}
	return out
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestALinkPutsItsTargetAfterItsSource is the whole point: a description that
// lists the read before the create has them run the other way round.
func TestALinkPutsItsTargetAfterItsSource(t *testing.T) {
	transactions := []compile.Transaction{
		transaction("readUser"),
		transaction("createUser", linkTo("ReadUser", "readUser")),
	}

	plan := Build(transactions)
	if got := indexes(plan, transactions); !equal(got, []string{"createUser", "readUser"}) {
		t.Errorf("order = %v, want [createUser readUser]", got)
	}
}

// TestEveryTransactionRunsExactlyOnce is the property the whole design rests
// on, and the reason this is not a state machine.
//
// A plan has as many steps as the flat list has transactions. Cost, result
// counts and hook names are therefore unchanged by sequencing, so turning it on
// does not shift a CI dashboard — and there is nothing to budget, because
// nothing repeats.
func TestEveryTransactionRunsExactlyOnce(t *testing.T) {
	transactions := []compile.Transaction{
		transaction("createUser",
			linkTo("ReadUser", "readUser"),
			linkTo("DeleteUser", "deleteUser")),
		transaction("readUser", linkTo("DeleteUser", "deleteUser")),
		transaction("deleteUser"),
		transaction("unrelated"),
	}

	plan := Build(transactions)
	if len(plan.Steps) != len(transactions) {
		t.Fatalf("got %d step(s) for %d transaction(s)", len(plan.Steps), len(transactions))
	}

	seen := map[int]int{}
	for _, step := range plan.Steps {
		seen[step.Index]++
	}
	for i := range transactions {
		if seen[i] != 1 {
			t.Errorf("transaction %d ran %d time(s), want exactly 1", i, seen[i])
		}
	}
}

// TestFirstLinkInDocumentOrderWins pins that two links claiming one target do
// not depend on map iteration. Two runs of the same description must order the
// same way, or a failure cannot be reproduced from the report.
func TestFirstLinkInDocumentOrderWins(t *testing.T) {
	transactions := []compile.Transaction{
		transaction("first", linkTo("A", "target")),
		transaction("second", linkTo("B", "target")),
		transaction("target"),
	}

	for i := 0; i < 20; i++ {
		plan := Build(transactions)
		for _, step := range plan.Steps {
			if step.Index == 2 && step.Via != "A" {
				t.Fatalf("target followed %q, want A — the first link in document order", step.Via)
			}
		}
	}
}

// TestACycleIsBrokenRatherThanFollowed pins that a description whose links form
// a loop still produces a runnable plan.
//
// Every transaction still runs exactly once; one edge is dropped and recorded.
func TestACycleIsBrokenRatherThanFollowed(t *testing.T) {
	transactions := []compile.Transaction{
		transaction("a", linkTo("ToB", "b")),
		transaction("b", linkTo("ToA", "a")),
	}

	plan := Build(transactions)
	if len(plan.Steps) != 2 {
		t.Fatalf("got %d step(s), want 2", len(plan.Steps))
	}
	if len(plan.Notes) == 0 {
		t.Error("breaking a cycle should be recorded, not silent")
	}
}

// TestSelfLinkIsIgnored pins the degenerate case: an operation linking to
// itself would depend on its own response, and no order satisfies that.
func TestSelfLinkIsIgnored(t *testing.T) {
	transactions := []compile.Transaction{transaction("a", linkTo("ToSelf", "a"))}

	plan := Build(transactions)
	if len(plan.Steps) != 1 || plan.Steps[0].After != -1 {
		t.Errorf("a self-link should leave the step standing alone, got %+v", plan.Steps)
	}
}

// TestAnUnknownTargetIsANoteNotAFailure pins that a link naming an operation
// the description does not define is reported as a problem with the document.
// It is not a fault of the server, and failing a transaction over it would send
// the reader looking in entirely the wrong place.
func TestAnUnknownTargetIsANoteNotAFailure(t *testing.T) {
	transactions := []compile.Transaction{
		transaction("a", linkTo("Missing", "nowhere")),
	}

	plan := Build(transactions)
	if len(plan.Notes) != 1 {
		t.Fatalf("got %d note(s), want 1: %v", len(plan.Notes), plan.Notes)
	}
	if len(plan.Steps) != 1 {
		t.Errorf("the transaction should still run, got %d step(s)", len(plan.Steps))
	}
}

// TestOperationRefIsReportedRatherThanGuessed pins that the unsupported target
// form says so, and says what to do instead.
func TestOperationRefIsReportedRatherThanGuessed(t *testing.T) {
	transactions := []compile.Transaction{
		{OperationID: "a", Links: []compile.Link{{Name: "Ref", OperationRef: "#/paths/~1users/get"}}},
	}

	plan := Build(transactions)
	if len(plan.Notes) != 1 {
		t.Fatalf("got %d note(s), want 1", len(plan.Notes))
	}
	if !containsText(plan.Notes[0], "operationId") {
		t.Errorf("the note should say what to do instead: %q", plan.Notes[0])
	}
}

// TestOrderIsOtherwiseTheDocumentsOwn pins that a plan reorders only what a
// link requires. A run that shuffles more than it must cannot be read back
// against the description.
func TestOrderIsOtherwiseTheDocumentsOwn(t *testing.T) {
	transactions := []compile.Transaction{
		transaction("one"),
		transaction("two"),
		transaction("three"),
	}

	if got := indexes(Build(transactions), transactions); !equal(got, []string{"one", "two", "three"}) {
		t.Errorf("order = %v, want the document's own", got)
	}
}

// TestAChainOfThreeOrdersEndToEnd pins that depth is respected rather than only
// single edges: create must precede read, which must precede delete.
func TestAChainOfThreeOrdersEndToEnd(t *testing.T) {
	transactions := []compile.Transaction{
		transaction("delete"),
		transaction("read", linkTo("Delete", "delete")),
		transaction("create", linkTo("Read", "read")),
	}

	if got := indexes(Build(transactions), transactions); !equal(got, []string{"create", "read", "delete"}) {
		t.Errorf("order = %v, want [create read delete]", got)
	}
}

// TestValuesLeavesUnresolvedParametersOut pins that a value which cannot be
// resolved is omitted rather than blanked, so the target keeps its own example
// instead of being sent an empty identifier.
func TestValuesLeavesUnresolvedParametersOut(t *testing.T) {
	l := compile.Link{
		Name: "ReadUser",
		Parameters: map[string]string{
			"good":    "$response.body#/id",
			"missing": "$response.body#/absent",
		},
	}

	values, unresolved := Values(l, Exchange{ResponseBody: `{"id":42}`})
	if values["good"] != float64(42) {
		t.Errorf("good = %#v, want 42", values["good"])
	}
	if _, present := values["missing"]; present {
		t.Error("an unresolvable parameter should be absent, not blank")
	}
	if len(unresolved) != 1 || unresolved[0] != "missing" {
		t.Errorf("unresolved = %v, want [missing]", unresolved)
	}
}

// TestMatchAcceptsBothSpellings pins that a link parameter names its target
// bare or qualified by location.
func TestMatchAcceptsBothSpellings(t *testing.T) {
	if !Match("userId", "path", "userId") {
		t.Error("the bare form should match")
	}
	if !Match("path.userId", "path", "userId") {
		t.Error("the qualified form should match")
	}
	if Match("query.userId", "path", "userId") {
		t.Error("a different location should not match")
	}
}

func containsText(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
