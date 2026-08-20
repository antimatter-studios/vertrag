package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/fuzz"
	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
)

// documents is one operation's response variants, as the compiler produces
// them: one transaction per documented status, all sharing a template.
func documents(method, template string, statuses ...string) []compile.Transaction {
	var transactions []compile.Transaction
	for _, status := range statuses {
		transactions = append(transactions, compile.Transaction{
			Name:     template + " > " + status,
			Request:  compile.Request{Method: method, URI: template, Template: template},
			Response: compile.Response{Status: status},
		})
	}
	return transactions
}

// answered records one response, as the runner's Observe hook would.
func answered(t *testing.T, ledger *statusLedger, transaction compile.Transaction, status string, mode ...generate.Mode) {
	t.Helper()
	ctx := context.Background()
	if len(mode) > 0 {
		ctx = fuzz.WithMode(ctx, mode[0])
	}
	ledger.observe(ctx, transaction, validate.Message{StatusCode: status})
}

func rendered(ledger *statusLedger) string {
	var out strings.Builder
	ledger.report(&out)
	return out.String()
}

// TestAStatusDocumentedOnAnotherVariantIsNotReported is the per-operation
// decision, and the reason this is not a per-transaction check.
//
// The 404 arrives while the run is sending the operation's 200 variant, which
// is the ordinary case: a probing phase sends everything through the success
// variant on purpose. Judged per transaction the 404 is undocumented, because
// THIS transaction promised a 200 — and the description names it two lines
// further down.
func TestAStatusDocumentedOnAnotherVariantIsNotReported(t *testing.T) {
	transactions := documents("GET", "/widgets/{id}", "200", "404")
	ledger := newStatusLedger(transactions)
	ledger.phase(config.PhaseExamples)

	answered(t, ledger, transactions[0], "404")

	if report := rendered(ledger); report != "" {
		t.Errorf("a status the description documents elsewhere on the operation was reported:\n%s", report)
	}
}

// TestARangeDocumentsEveryStatusInItsBand pins that the judgement is
// validate.StatusMatches and not a comparison of its own. A document saying
// `2XX` has documented its 201.
func TestARangeDocumentsEveryStatusInItsBand(t *testing.T) {
	transactions := documents("GET", "/ranged", "2XX")
	ledger := newStatusLedger(transactions)
	ledger.phase(config.PhaseExamples)

	answered(t, ledger, transactions[0], "201")
	if report := rendered(ledger); report != "" {
		t.Errorf("`2XX` should document a 201:\n%s", report)
	}

	answered(t, ledger, transactions[0], "404")
	if report := rendered(ledger); !strings.Contains(report, "404") {
		t.Errorf("`2XX` should not document a 404:\n%s", report)
	}
}

// TestAStatusTheDescriptionNeverMentionsIsReported is the headline: the
// operation, the status, how many times, and which phase saw it.
func TestAStatusTheDescriptionNeverMentionsIsReported(t *testing.T) {
	transactions := documents("GET", "/widgets/{id}", "200")
	ledger := newStatusLedger(transactions)
	ledger.phase(config.PhaseExamples)
	answered(t, ledger, transactions[0], "409")
	ledger.phase(config.PhaseFuzz)
	answered(t, ledger, transactions[0], "409", generate.Valid)

	report := rendered(ledger)
	for _, want := range []string{"GET /widgets/{id}", "409", "x2", "examples, fuzz"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not carry %q:\n%s", want, report)
		}
	}
}

// TestInputTheSchemaForbidsIsReportedApartFromTheRest is the noise decision.
//
// A probing run provokes hundreds of correct refusals and a handful of the
// statuses somebody should act on. Printed as one list the first buries the
// second, so a 400 seen ONLY from input the schema forbids is grouped apart —
// and the same status seen once from input the schema permits is not.
func TestInputTheSchemaForbidsIsReportedApartFromTheRest(t *testing.T) {
	transactions := documents("GET", "/widgets/{id}", "200")

	refusalOnly := newStatusLedger(transactions)
	refusalOnly.phase(config.PhaseFuzz)
	answered(t, refusalOnly, transactions[0], "400", generate.Invalid)

	report := rendered(refusalOnly)
	weak := strings.Index(report, "Reached only by deliberately invalid input")
	strong := strings.Index(report, "The document is wrong about its own operation")
	if weak < 0 {
		t.Fatalf("a refusal of invalid input is not reported apart:\n%s", report)
	}
	if strong >= 0 {
		t.Errorf("a refusal of invalid input was reported as the stronger kind:\n%s", report)
	}

	// One sighting from permitted input moves the same status across.
	both := newStatusLedger(transactions)
	both.phase(config.PhaseFuzz)
	answered(t, both, transactions[0], "400", generate.Invalid)
	answered(t, both, transactions[0], "400", generate.Valid)

	report = rendered(both)
	if !strings.Contains(report, "The document is wrong about its own operation") {
		t.Errorf("a status also seen from permitted input stayed in the weaker group:\n%s", report)
	}
	if strings.Contains(report, "Reached only by deliberately invalid input") {
		t.Errorf("one status was reported in both groups:\n%s", report)
	}
}

// TestAServerErrorIsNotReportedAsMerelyUndocumented pins that one event does
// not get two entries at two severities. A 5xx is a finding; the section says
// how many it left out and nothing more.
func TestAServerErrorIsNotReportedAsMerelyUndocumented(t *testing.T) {
	transactions := documents("GET", "/widgets/{id}", "200")
	ledger := newStatusLedger(transactions)
	ledger.phase(config.PhaseExamples)

	answered(t, ledger, transactions[0], "503")
	if report := rendered(ledger); report != "" {
		t.Errorf("a 5xx alone produced a section; it is already a finding:\n%s", report)
	}

	answered(t, ledger, transactions[0], "409")
	report := rendered(ledger)
	if strings.Contains(report, "503") {
		t.Errorf("a 5xx was listed as merely undocumented:\n%s", report)
	}
	if !strings.Contains(report, "1 undocumented 5xx answer(s) are not listed") {
		t.Errorf("the section does not say what it left out:\n%s", report)
	}
}

// TestOnePathTwoMethodsAreTwoOperations. A path serving GET and DELETE
// documents each separately, and a key made of the path alone would let one
// method's statuses excuse the other's.
func TestOnePathTwoMethodsAreTwoOperations(t *testing.T) {
	transactions := append(
		documents("GET", "/widgets/{id}", "200", "404"),
		documents("DELETE", "/widgets/{id}", "204")...)
	ledger := newStatusLedger(transactions)
	ledger.phase(config.PhaseExamples)

	answered(t, ledger, transactions[2], "404")

	report := rendered(ledger)
	if !strings.Contains(report, "DELETE /widgets/{id}") {
		t.Errorf("the DELETE's 404 was excused by the GET's:\n%s", report)
	}
}

// TestAGeneratedPathValueDoesNotInventAnOperation.
//
// A probe replaces the value in the URI, so the request that arrives at the
// observation point reads `/widgets/8231` where the description said
// `/widgets/{id}`. Keyed on the expanded URI, every drawn value would be an
// operation of its own — each documenting nothing, each reporting every status
// it saw. operationKey answers with the template, and this is why.
func TestAGeneratedPathValueDoesNotInventAnOperation(t *testing.T) {
	transactions := documents("GET", "/widgets/{id}", "200")
	ledger := newStatusLedger(transactions)
	ledger.phase(config.PhaseFuzz)

	for _, id := range []string{"8231", "17", "4"} {
		probed := transactions[0]
		probed.Request.URI = "/widgets/" + id
		answered(t, ledger, probed, "404", generate.Valid)
	}

	report := rendered(ledger)
	if !strings.Contains(report, "x3") {
		t.Errorf("three drawn values were counted as separate operations:\n%s", report)
	}
	if strings.Contains(report, "/widgets/8231") {
		t.Errorf("the report names an expanded URI rather than the operation:\n%s", report)
	}
}

// TestNothingIsSaidWhenTheDescriptionIsRight. Silence is the report on a
// document that describes what its API does, and a line saying so on every
// clean run is a line readers learn to skip past on the run where it matters.
func TestNothingIsSaidWhenTheDescriptionIsRight(t *testing.T) {
	transactions := documents("GET", "/widgets/{id}", "200", "404")
	ledger := newStatusLedger(transactions)
	ledger.phase(config.PhaseExamples)

	answered(t, ledger, transactions[0], "200")
	answered(t, ledger, transactions[1], "404", generate.Valid)

	if report := rendered(ledger); report != "" {
		t.Errorf("a description that was right still produced a section:\n%s", report)
	}
}

// widgetsByID answers the way an ordinary handler does and the way its
// description does not admit to: the documented example works, an id that
// names nothing is a 404, and an id that is not one is a 400. Neither of the
// last two is written down.
func widgetsByID(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/widgets/"))
		switch {
		case err != nil || id < 1 || id > 9999:
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"that is not an id"}`)
		case id != 1:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"no such widget"}`)
		default:
			fmt.Fprint(w, `{"id":1}`)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// describedAs writes a description of that operation, documenting the statuses
// named and no others.
func describedAs(t *testing.T, statuses ...string) string {
	t.Helper()
	document := `openapi: 3.0.0
info:
  title: Undocumented
  version: "1.0.0"
paths:
  /widgets/{id}:
    get:
      summary: Read a widget
      parameters:
        - name: id
          in: path
          required: true
          schema: {type: integer, minimum: 1, maximum: 9999}
          example: 1
      responses:
`
	for _, status := range statuses {
		document += `        "` + status + `":
          description: An answer
          content:
            application/json:
              schema: {type: object}
`
	}

	path := filepath.Join(t.TempDir(), "widgets.yml")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("writing the description: %v", err)
	}
	return path
}

// TestAnUndocumentedStatusIsReportedByTheCommand runs the binary a user runs,
// against a server whose handler does more than its document says.
//
// Through the built command deliberately: the ledger can be entirely correct
// while nothing installs it, and a hook that is never wired up produces the
// same empty section as an API that is perfectly documented. The exit code is
// asserted at the same time, because the section must not change it — a
// defect in a document is not a reason to fail somebody's pipeline, and a
// pipeline that fails over one teaches its owners to stop reading the output.
func TestAnUndocumentedStatusIsReportedByTheCommand(t *testing.T) {
	binary := build(t)
	endpoint := widgetsByID(t)
	description := describedAs(t, "200")

	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color",
		"--phases", "examples,coverage,fuzz", "--cases", "8", "--seed", "7", description)

	if code != 0 {
		t.Errorf("exit = %d, want 0 — an undocumented status is a report, not a verdict\n%s", code, output)
	}
	if !strings.Contains(output, "Statuses returned that the description does not document") {
		t.Fatalf("no section about the description:\n%s", output)
	}

	// The 404 arrives from an id the published schema permits; the 400 only
	// from one it forbids. They must not be in the same list.
	permitted := strings.Index(output, "The document is wrong about its own operation")
	forbidden := strings.Index(output, "Reached only by deliberately invalid input")
	if permitted < 0 || forbidden < 0 {
		t.Fatalf("both kinds of evidence should be present and apart:\n%s", output)
	}
	if !strings.Contains(output[permitted:forbidden], "404") {
		t.Errorf("the 404 an accepted id provoked is not in the stronger group:\n%s", output)
	}
	if !strings.Contains(output[forbidden:], "400") {
		t.Errorf("the 400 only a rejected id provoked is not in the weaker group:\n%s", output)
	}
	if !strings.Contains(output, "GET /widgets/{id}") {
		t.Errorf("the section does not name the operation:\n%s", output)
	}
}

// TestADocumentedStatusIsNotReportedByTheCommand is the other half, and the
// one that would catch a section that simply prints everything: the same
// server, the same probes, a description that admits to all three answers.
//
// Only the 200 variant is run, which is a second thing pinned rather than a
// convenience. The error variants are reached in a real suite by staging them
// at a mock, and here there is nothing to stage — sending them at the id the
// description gives would just fail. Narrowing the RUN must not narrow what
// the DESCRIPTION is taken to document, or every suite that filters would
// start reporting its own excluded responses as undocumented.
func TestADocumentedStatusIsNotReportedByTheCommand(t *testing.T) {
	binary := build(t)
	endpoint := widgetsByID(t)
	description := describedAs(t, "200", "400", "404")

	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color",
		"--only-matching", "> 200 >",
		"--phases", "examples,coverage,fuzz", "--cases", "8", "--seed", "7", description)

	if code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, output)
	}
	if strings.Contains(output, "Statuses returned that the description does not document") {
		t.Errorf("a description that documents all three answers still got a section:\n%s", output)
	}
}
