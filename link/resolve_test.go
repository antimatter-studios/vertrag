package link

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
)

// The link check is judged on one question throughout: is the thing it reports
// something the DESCRIPTION says, or something the checker assumed? Every test
// here supplies a document that makes a claim and a response that either honours
// it or does not, and nothing else — no server, no fixture, no lifecycle.

// answered is the source operation having produced its documented response.
func answered(body string) map[int]Observed {
	return map[int]Observed{1: {Exchange: Exchange{StatusCode: "201", ResponseBody: body}}}
}

// linking is a create whose response declares one link, at index 1, so that a
// target may sit at index 0 the way a description usually lists it.
func linking(l compile.Link) []compile.Transaction {
	return []compile.Transaction{{}, {Name: "createItem", OperationID: "createItem", Links: []compile.Link{l}}}
}

// targeted puts a target operation at index 0, with the parameters the link is
// claiming to fill in.
func targeted(transactions []compile.Transaction, operationID string, parameters ...compile.Parameter) []compile.Transaction {
	transactions[0] = compile.Transaction{
		Name: operationID, OperationID: operationID,
		Request: compile.Request{Method: "GET", Parameters: parameters},
	}
	return transactions
}

func itemID(schema string) compile.Parameter {
	return compile.Parameter{In: compile.InPath, Name: "itemId", Schema: schema}
}

func messages(report Report) string {
	var lines []string
	for _, finding := range report.Findings {
		lines = append(lines, finding.Message)
	}
	return strings.Join(lines, "\n")
}

// TestALinkWhoseExpressionResolvesToNothingIsReported is the case that
// prompted the check: a description promises that its response carries the
// identifier the next operation needs, and the response does not.
func TestALinkWhoseExpressionResolvesToNothingIsReported(t *testing.T) {
	transactions := targeted(linking(compile.Link{
		Name: "Read", OperationID: "readItem",
		Parameters: map[string]string{"itemId": "$response.body#/id"},
	}), "readItem", itemID(`{"type":"integer"}`))

	report := Check(transactions, nil, answered(`{"name":"widget"}`))

	if len(report.Findings) != 1 {
		t.Fatalf("findings = %d, want 1:\n%s", len(report.Findings), messages(report))
	}
	for _, want := range []string{"link Read", "$response.body#/id", "carries no such value"} {
		if !strings.Contains(report.Findings[0].Message, want) {
			t.Errorf("the finding does not mention %q:\n%s", want, report.Findings[0].Message)
		}
	}
}

// TestALinkThatResolvesIsNotReported is the soundness half. A check that
// reports a description doing exactly what it said is worth nothing, because
// its findings stop being read.
func TestALinkThatResolvesIsNotReported(t *testing.T) {
	transactions := targeted(linking(compile.Link{
		Name: "Read", OperationID: "readItem",
		Parameters: map[string]string{"itemId": "$response.body#/id"},
	}), "readItem", itemID(`{"type":"integer","minimum":1}`))

	report := Check(transactions, nil, answered(`{"id":7}`))

	if len(report.Findings) != 0 {
		t.Errorf("a link that resolves was reported:\n%s", messages(report))
	}
	if report.Checked != 1 {
		t.Errorf("checked = %d, want 1 — a clean report only means something if something was checked", report.Checked)
	}
}

// TestALinkSupplyingAValueTheTargetRefusesIsReported: the value exists, and
// the two halves of the document disagree about what it is. The link says the
// name in this response is that operation's identifier; the operation says its
// identifier is an integer.
func TestALinkSupplyingAValueTheTargetRefusesIsReported(t *testing.T) {
	transactions := targeted(linking(compile.Link{
		Name: "Read", OperationID: "readItem",
		Parameters: map[string]string{"itemId": "$response.body#/name"},
	}), "readItem", itemID(`{"type":"integer"}`))

	report := Check(transactions, nil, answered(`{"name":"widget"}`))

	if len(report.Findings) != 1 {
		t.Fatalf("findings = %d, want 1:\n%s", len(report.Findings), messages(report))
	}
	for _, want := range []string{"path parameter itemId", `"widget"`, "not an integer"} {
		if !strings.Contains(report.Findings[0].Message, want) {
			t.Errorf("the finding does not mention %q:\n%s", want, report.Findings[0].Message)
		}
	}
}

// TestALinkSupplyingAValueOutsideTheTargetsRangeIsReported is the other half
// of the same question, and the half a type comparison alone would miss: the
// value is the declared type and still not one the target accepts. The two
// come out of different branches of the decoder, so both are pinned.
func TestALinkSupplyingAValueOutsideTheTargetsRangeIsReported(t *testing.T) {
	transactions := targeted(linking(compile.Link{
		Name: "Read", OperationID: "readItem",
		Parameters: map[string]string{"itemId": "$response.body#/id"},
	}), "readItem", itemID(`{"type":"integer","minimum":1}`))

	report := Check(transactions, nil, answered(`{"id":0}`))

	if len(report.Findings) != 1 {
		t.Fatalf("findings = %d, want 1:\n%s", len(report.Findings), messages(report))
	}
	if !strings.Contains(report.Findings[0].Message, `is "0"`) {
		t.Errorf("the finding does not quote the value the link supplied:\n%s", report.Findings[0].Message)
	}
}

// TestALinkNamingAParameterTheTargetDoesNotDeclareIsReported. The sequencer
// already refuses to run a step on this, and said so only to whoever ran with
// --sequence; it is a contradiction between two parts of one document and is
// worth saying whether or not a run is ordered by links.
func TestALinkNamingAParameterTheTargetDoesNotDeclareIsReported(t *testing.T) {
	transactions := targeted(linking(compile.Link{
		Name: "Read", OperationID: "readItem",
		Parameters: map[string]string{"id": "$response.body#/id"},
	}), "readItem", itemID(`{"type":"integer"}`))

	report := Check(transactions, nil, answered(`{"id":7}`))

	if len(report.Findings) != 1 {
		t.Fatalf("findings = %d, want 1:\n%s", len(report.Findings), messages(report))
	}
	if !strings.Contains(report.Findings[0].Message, "declares no parameter of that name") {
		t.Errorf("the finding does not say the parameter is not declared:\n%s", report.Findings[0].Message)
	}
}

// TestALinkNamingAnOperationTheDescriptionDoesNotDefineNeedsNoResponse: the
// claim is false on the document alone, so no exchange is required to say so.
func TestALinkNamingAnOperationTheDescriptionDoesNotDefineNeedsNoResponse(t *testing.T) {
	transactions := linking(compile.Link{
		Name: "Read", OperationID: "readItemThatDoesNotExist",
		Parameters: map[string]string{"itemId": "$response.body#/id"},
	})

	report := Check(transactions, nil, nil)

	if len(report.Findings) != 1 {
		t.Fatalf("findings = %d, want 1:\n%s", len(report.Findings), messages(report))
	}
	if !strings.Contains(report.Findings[0].Message, "readItemThatDoesNotExist") {
		t.Errorf("the finding does not name the operation nothing defines:\n%s", report.Findings[0].Message)
	}
}

// TestALinkNamingAnOperationTheRunLeftOutIsNotCalledUndefined. `--only`,
// `--tag` and their neighbours narrow what a run holds; they say nothing about
// what the document defines. A check that read the two as the same thing would
// answer every narrowed run with a page of findings about a description that
// is perfectly consistent — the loudest possible false alarm, on the option
// people reach for when a suite is already failing.
func TestALinkNamingAnOperationTheRunLeftOutIsNotCalledUndefined(t *testing.T) {
	defined := targeted(linking(compile.Link{
		Name: "Read", OperationID: "readItem",
		Parameters: map[string]string{"itemId": "$response.body#/id"},
	}), "readItem", itemID(`{"type":"integer"}`))

	// The run holds the create alone, as `--exclude-method GET` would leave it.
	running := []compile.Transaction{defined[1]}
	report := Check(running, defined, map[int]Observed{
		0: {Exchange: Exchange{StatusCode: "201", ResponseBody: `{"id":7}`}}})

	if len(report.Findings) != 0 {
		t.Errorf("a narrowed run was told its description is inconsistent:\n%s", messages(report))
	}
	if report.Checked != 1 {
		t.Errorf("checked = %d, want 1: the link is still resolvable and still worth checking", report.Checked)
	}
}

// TestALinkWhoseSourceNeverAnsweredIsUncheckedRatherThanFalse is the whole
// distinction between the two lists. The project that prompted this check had
// exactly this: a create answering 400 where its description promises success,
// so the link below it could not resolve. The link was not wrong, and blaming
// it would have sent the reader to the wrong half of the document.
func TestALinkWhoseSourceNeverAnsweredIsUncheckedRatherThanFalse(t *testing.T) {
	transactions := targeted(linking(compile.Link{
		Name: "Read", OperationID: "readItem",
		Parameters: map[string]string{"itemId": "$response.body#/id"},
	}), "readItem", itemID(`{"type":"integer"}`))

	report := Check(transactions, nil, map[int]Observed{
		1: {Missing: "it answered 400 where the description promises 201"}})

	if len(report.Findings) != 0 {
		t.Errorf("a link nothing could be resolved against was reported as false:\n%s", messages(report))
	}
	if len(report.Unchecked) != 1 {
		t.Fatalf("unchecked = %d, want 1 — silence would read as a link that held", len(report.Unchecked))
	}
	if !strings.Contains(report.Unchecked[0].Reason, "answered 400") {
		t.Errorf("the reason does not say what the source did:\n%s", report.Unchecked[0].Reason)
	}
	if report.Checked != 0 {
		t.Errorf("checked = %d, want 0: nothing was resolved", report.Checked)
	}
}

// TestALinkBodyThatResolvesToNothingIsReported. requestBody is the other half
// of a Link Object's claim and reads from the same expressions, so a document
// that names a field its response does not carry is wrong in the same way.
func TestALinkBodyThatResolvesToNothingIsReported(t *testing.T) {
	transactions := targeted(linking(compile.Link{
		Name: "Rename", OperationID: "renameItem",
		RequestBody: "$response.body#/item",
	}), "renameItem")

	report := Check(transactions, nil, answered(`{"id":7}`))

	if len(report.Findings) != 1 {
		t.Fatalf("findings = %d, want 1:\n%s", len(report.Findings), messages(report))
	}
	if !strings.Contains(report.Findings[0].Message, "the body of renameItem") {
		t.Errorf("the finding does not say it is the body that is claimed:\n%s", report.Findings[0].Message)
	}
}

// TestTwoRunsOfTheSameDescriptionReportTheSameOrder: the findings come out of
// map iteration, and a report that shuffled between runs cannot be diffed
// against yesterday's.
func TestTwoRunsOfTheSameDescriptionReportTheSameOrder(t *testing.T) {
	transactions := targeted(linking(compile.Link{
		Name: "Read", OperationID: "readItem",
		Parameters: map[string]string{
			"alpha": "$response.body#/a", "beta": "$response.body#/b", "gamma": "$response.body#/c"},
	}), "readItem", itemID(`{"type":"integer"}`))

	first := messages(Check(transactions, nil, answered(`{}`)))
	for i := 0; i < 20; i++ {
		if again := messages(Check(transactions, nil, answered(`{}`))); again != first {
			t.Fatalf("two runs reported different orders:\n%s\n---\n%s", first, again)
		}
	}
}
