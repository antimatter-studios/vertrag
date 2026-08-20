package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/corpus"
	"github.com/antimatter-studios/vertrag/shape"
)

// A description of the operation a FastAPI service gets wrong for free.
//
// One documented response, 422, one media type, one schema — `an object with a
// detail`. That is the whole of what the description promises, and it is
// truthful about both bodies the service can send. Which of the two a client
// gets is not written down anywhere, because there is nowhere in the document
// to write it.
const domainErrorAPI = `openapi: 3.0.3
info: {title: Catalogue, version: "1.0"}
paths:
  /items/{itemId}:
    get:
      parameters:
        - name: itemId
          in: path
          required: true
          schema: {type: integer}
          example: 999
      responses:
        "422":
          description: the item could not be processed
          content:
            application/json:
              schema:
                type: object
                required: [detail]
`

// fastAPIShapedServer answers 422 two different ways, exactly as FastAPI does.
//
// An itemId that parses as an integer reaches the handler, which raises its own
// 422 out of a domain ValueError: `detail` is the string the exception carried.
// One that does not parse never reaches the handler at all — the framework's
// request validation refuses it first, with the 422 it generates itself, whose
// `detail` is an array of error objects.
//
// Neither response is a bug. The pair is.
func fastAPIShapedServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		itemID := strings.TrimPrefix(r.URL.Path, "/items/")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)

		if _, err := strconv.Atoi(itemID); err == nil {
			fmt.Fprintf(w, `{"detail":"no item with id %s"}`, itemID)
			return
		}
		_, _ = io.WriteString(w, `{"detail":[{"loc":["path","itemId"],`+
			`"msg":"Input should be a valid integer","type":"int_parsing"}]}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func domainErrorProject(t *testing.T, endpoint string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(domainErrorAPI), 0o600); err != nil {
		t.Fatal(err)
	}
	// `mode: invalid` because the operation is documented as failing: a
	// valid-mode probe of an operation whose only documented answer is 422
	// reports every 422 as the server refusing a value its schema permits,
	// which is a different finding and would bury this one.
	config := fmt.Sprintf(`spec: ./api.yml
endpoint: %s
phases: [examples, fuzz]
fuzz: {cases: 4, seed: 1}
`, endpoint)
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestOneStatusAnsweringWithTwoShapesIsReported is the defect in full, through
// the built binary and against a server that reproduces it.
//
// It takes a whole run to see: the examples phase sends the documented itemId,
// which parses, reaches the handler and comes back with `detail` as a string;
// the fuzz phase sends values the parameter's schema forbids, which the
// framework refuses before the handler runs, with `detail` as an array. Every
// individual response conforms to the description. A client cannot be written
// against them.
func TestOneStatusAnsweringWithTwoShapesIsReported(t *testing.T) {
	binary := build(t)
	server := fastAPIShapedServer(t)

	output, _ := runIn(t, domainErrorProject(t, server.URL), binary,
		"run", "--mode", "invalid", "--no-color")

	if !strings.Contains(output, "two shapes for one status") {
		t.Fatalf("the run met both 422 bodies and said nothing:\n%s", output)
	}
	if !strings.Contains(output, "GET /items/{itemId} · 422 · application/json") {
		t.Errorf("the divergence is not named by operation, status and media type:\n%s", output)
	}
	// Which phase saw which shape is what localises the cause — a framework
	// default against a handler's own error — so its absence is a failure of
	// the report even when the divergence itself is found.
	//
	// The fuzz phase appears against the string too, and that is not a
	// bookkeeping slip: probing sends each operation once exactly as documented
	// before it generates anything, to establish that the operation works at
	// all, and that request goes to the handler like any other. Only the array
	// is unique to a phase, which is the half that points at the cause.
	if !strings.Contains(output, "at detail: string (examples, fuzz), array (fuzz)") {
		t.Errorf("the report does not say which phase saw which shape:\n%s", output)
	}
}

// TestTwoShapesForOneStatusDoNotFailTheRun pins the other half. This is
// information about the description, not a response that broke a promise, and a
// run whose exit code moved because of it would be a merge blocked on a
// document nobody edited.
func TestTwoShapesForOneStatusDoNotFailTheRun(t *testing.T) {
	binary := build(t)
	server := fastAPIShapedServer(t)

	output, code := runIn(t, domainErrorProject(t, server.URL), binary,
		"run", "--mode", "invalid", "--no-color")

	if !strings.Contains(output, "two shapes for one status") {
		t.Fatalf("nothing was reported, so this proves nothing about the exit code:\n%s", output)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0 — two shapes for one status is a summary, not a finding\n%s", code, output)
	}
}

// TestAConformingSuiteReportsNoTwoShapes is the false-report guard, and it is
// the test this check most needs.
//
// The corpus servers answer their own descriptions faithfully, so a divergence
// found here is one this invented — and an invented one is expensive: the
// reader has to fetch two responses and reason about both before they can
// dismiss it. Every description in the corpus, with every probing phase on, and
// they include the features most likely to trip it: several documented statuses
// per operation, several response media types, and bodies of every scalar type
// there is.
func TestAConformingSuiteReportsNoTwoShapes(t *testing.T) {
	binary := build(t)

	for _, name := range corpus.Names() {
		t.Run(name, func(t *testing.T) {
			endpoint, description := serve(t, name)
			output, _ := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color",
				"--phases", "examples,coverage,fuzz", "--cases", "8", "--seed", "1", description)

			// A run that sent nothing proves nothing, and a description this
			// could not read, or a phase that never started, would be exactly
			// that.
			if !strings.Contains(output, " passing, ") || !strings.Contains(output, "seed: 1") {
				t.Fatalf("the run did not reach the probing phases, so it guards nothing:\n%s", output)
			}
			if strings.Contains(output, "two shapes for one status") {
				t.Errorf("a conforming server was reported as answering one status two ways:\n%s", output)
			}
		})
	}
}

// TestTheSummarySaysWhereTheDisagreementIsAndWhoSawIt pins the section itself,
// away from a server, because the whole value of the report is in its wording:
// a reader who cannot tell from it which field disagreed and which phase saw
// which body has to reproduce the run to learn anything.
func TestTheSummarySaysWhereTheDisagreementIsAndWhoSawIt(t *testing.T) {
	var written strings.Builder
	reportDivergences(&written, []shape.Divergence{{
		Operation: "GET /items/{itemId}",
		Status:    "422",
		Media:     "application/json",
		Conflicts: []shape.Conflict{
			{Path: "", Kinds: []shape.Sighting{
				{Kind: shape.Object, Phases: []string{"examples"}},
				{Kind: shape.Array, Phases: []string{"fuzz"}},
			}},
			{Path: "/detail", Kinds: []shape.Sighting{
				{Kind: shape.String, Phases: []string{"examples"}},
				{Kind: shape.Array, Phases: []string{"coverage", "fuzz"}},
			}},
		},
	}}, false)

	for _, want := range []string{
		"two shapes for one status:",
		"  GET /items/{itemId} · 422 · application/json\n",
		"    at the whole body: object (examples), array (fuzz)\n",
		"    at detail: string (examples), array (coverage, fuzz)\n",
		"Nothing above fails the run",
	} {
		if !strings.Contains(written.String(), want) {
			t.Errorf("the summary is missing %q:\n%s", want, written.String())
		}
	}
}

// TestNothingIsPrintedWhenEveryStatusKeptItsShape. A run with nothing to say
// says nothing: a heading over an empty list is a report that trains people to
// ignore the heading.
func TestNothingIsPrintedWhenEveryStatusKeptItsShape(t *testing.T) {
	var written strings.Builder
	reportDivergences(&written, nil, false)
	if written.Len() != 0 {
		t.Errorf("an empty summary printed %q", written.String())
	}
}

// A description whose 200 is offered in two media types, which is what content
// negotiation looks like written down.
const negotiatedAPI = `openapi: 3.0.3
info: {title: Negotiated, version: "1.0"}
paths:
  /report:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {type: object, required: [name]}
              example: {name: monthly}
            application/vnd.example.v2+json:
              schema: {type: array}
              example: [monthly]
`

// negotiatingServer answers in whichever of the two representations was asked
// for, and they are deliberately different shapes: an object one way, an array
// the other.
func negotiatingServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), "vnd.example.v2") {
			w.Header().Set("Content-Type", "application/vnd.example.v2+json")
			_, _ = io.WriteString(w, `["monthly"]`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"monthly"}`)
	}))
	t.Cleanup(server.Close)
	return server
}

// TestContentNegotiationIsNotReportedAsTwoShapes is the constraint that decides
// what this check is keyed on.
//
// One operation, one status, two content types, two bodies — and the second
// body is not a surprise, it is the point of the second content type. Keyed on
// the status alone this fires on every API that offers a representation in more
// than one form, and a check that reports the intended behaviour of ordinary
// APIs is one people learn to scroll past. So the media types must match before
// the shapes are compared at all.
func TestContentNegotiationIsNotReportedAsTwoShapes(t *testing.T) {
	binary := build(t)
	server := negotiatingServer(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(negotiatedAPI), 0o600); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("spec: ./api.yml\nendpoint: %s\n", server.URL)
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	output, code := runIn(t, dir, binary, "run", "--no-color")

	if code != 0 {
		t.Fatalf("exit = %d, want 0 — the server answered both representations as documented\n%s", code, output)
	}
	// Both representations really were fetched; without this the test would
	// pass on a run that never met the second shape.
	if !strings.Contains(output, "2 total, 2 passing") {
		t.Fatalf("both representations were not sent, so nothing here is proved:\n%s", output)
	}
	if strings.Contains(output, "two shapes for one status") {
		t.Errorf("content negotiation was reported as one status with two shapes:\n%s", output)
	}
}
