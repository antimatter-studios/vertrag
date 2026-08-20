package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The link check against a whole run, which is where the two things that
// matter about it are visible: that it needs neither the stateful phase nor a
// server that can create and delete, and that it distinguishes a link that is
// wrong from one that could not be tested.
//
// The description below is the shape the motivating project had: a create
// declaring a link to the operation that follows it. Everything the tests vary
// is what the server answers.

const linkedDescription = `openapi: 3.0.3
info: {title: Linked, version: "1.0"}
paths:
  /items:
    post:
      operationId: createItem
      responses:
        "201":
          description: made
          content:
            application/json:
              schema: {type: object}
          links:
            Read:
              operationId: readItem
              parameters: {itemId: "$response.body#/id"}
  /items/{itemId}:
    get:
      operationId: readItem
      parameters:
        - {name: itemId, in: path, required: true, example: 1, schema: {type: integer}}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {type: object}
`

// linkedProject stands up a server that answers the create with the given body
// and status, and writes a project that points at it.
func linkedProject(t *testing.T, created string, status int, extraConfig string) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(status)
			fmt.Fprint(w, created)
			return
		}
		fmt.Fprint(w, `{"id":1}`)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(linkedDescription), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := fmt.Sprintf("spec: ./api.yml\nendpoint: %s\n%s", server.URL, extraConfig)
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestALinkThatResolvesToNothingIsReportedWithoutTheStatefulPhase is the whole
// point of taking this out of that phase. No `--phases`, no chain, no create
// and delete: an ordinary run, and a description whose own words did not hold.
//
// Exit 2 rather than 1 because every documented transaction passed. The create
// answered 201 exactly as promised; what it did not carry is the identifier
// the link says it carries.
func TestALinkThatResolvesToNothingIsReportedWithoutTheStatefulPhase(t *testing.T) {
	binary := build(t)
	dir := linkedProject(t, `{"name":"widget"}`, http.StatusCreated, "")

	output, code := runIn(t, dir, binary, "run", "--no-color")

	if code != 2 {
		t.Errorf("exit = %d, want 2 — the documented transactions all passed\n%s", code, output)
	}
	for _, want := range []string{"link Read", "$response.body#/id", "carries no such value"} {
		if !strings.Contains(output, want) {
			t.Errorf("the run does not mention %q:\n%s", want, output)
		}
	}
	if !strings.Contains(output, "1 link(s) checked, 1 finding(s)") {
		t.Errorf("the check did not say what it checked:\n%s", output)
	}
	// The phase it used to live in must not have been started to get here.
	if strings.Contains(output, "chain(s) run") {
		t.Errorf("the stateful phase ran; the check is meant to stand alone:\n%s", output)
	}
}

// TestALinkThatResolvesLeavesTheRunGreen is the soundness half: the same
// description, a server that carries what the link claims, and nothing said
// beyond the count. A check that cried wolf on a conforming pair would be
// switched off within a day.
func TestALinkThatResolvesLeavesTheRunGreen(t *testing.T) {
	binary := build(t)
	dir := linkedProject(t, `{"id":7}`, http.StatusCreated, "")

	output, code := runIn(t, dir, binary, "run", "--no-color")

	if code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, output)
	}
	if !strings.Contains(output, "1 link(s) checked, 0 finding(s)") {
		t.Errorf("a clean check said nothing about having run:\n%s", output)
	}
}

// TestALinkWhoseSourceFailedIsReportedAsNotCheckedRatherThanFalse is the
// motivating run itself. Their `POST /roles` answered 400 where the
// description promises success — the schema said `name: string`, the handler
// demanded alphanumeric — and the link below it could not resolve.
//
// The link was not the defect and must not be reported as one: the reader has
// to be sent to the create. Saying nothing at all would be the other failure,
// since a link nobody could test reads exactly like a link that held.
func TestALinkWhoseSourceFailedIsReportedAsNotCheckedRatherThanFalse(t *testing.T) {
	binary := build(t)
	dir := linkedProject(t, `{"error":"name must be alphanumeric"}`, http.StatusBadRequest, "")

	output, code := runIn(t, dir, binary, "run", "--no-color")

	if code != 1 {
		t.Errorf("exit = %d, want 1 — a documented transaction failed\n%s", code, output)
	}
	if strings.Contains(output, "carries no such value") {
		t.Errorf("a link nothing could be resolved against was reported as false:\n%s", output)
	}
	for _, want := range []string{"not checked", "it answered 400 where the description promises 201"} {
		if !strings.Contains(output, want) {
			t.Errorf("the run does not say why the link went untested (%q):\n%s", want, output)
		}
	}
}

// TestANarrowedRunIsNotToldItsDescriptionIsInconsistent. `--exclude-method
// GET` leaves the run without the operation the link points at, and that says
// nothing whatever about the document. The check has to be handed the whole
// description alongside the run's own list, or every narrowed run over a
// linked description opens with findings that are not true.
func TestANarrowedRunIsNotToldItsDescriptionIsInconsistent(t *testing.T) {
	binary := build(t)
	dir := linkedProject(t, `{"id":7}`, http.StatusCreated, "")

	output, code := runIn(t, dir, binary, "run", "--no-color", "--exclude-method", "GET")

	if code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, output)
	}
	if strings.Contains(output, "does not define") {
		t.Errorf("an operation the run excluded was called undefined:\n%s", output)
	}
	if !strings.Contains(output, "1 link(s) checked") {
		t.Errorf("the link went unchecked although its source ran:\n%s", output)
	}
}

// TestADescriptionWithNoLinksSaysNothingAboutLinks. The check runs on every
// run, so its silence has to be worth something: most descriptions declare no
// links at all, and a line per run reporting that would train the reader to
// skip the place the findings appear.
func TestADescriptionWithNoLinksSaysNothingAboutLinks(t *testing.T) {
	binary := build(t)
	endpoint, description := serve(t, "widgets")

	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color", description)
	if code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, output)
	}
	if strings.Contains(output, "link(s) checked") {
		t.Errorf("a description with no links was told about link checking:\n%s", output)
	}
}

// TestLinkResolutionCanBeTurnedOff. It is on by default because it costs no
// request, but a description whose links are aspirational should not have to
// choose between fixing them today and a red pipeline.
func TestLinkResolutionCanBeTurnedOff(t *testing.T) {
	binary := build(t)
	dir := linkedProject(t, `{"name":"widget"}`, http.StatusCreated, "checks:\n  link-resolution: false\n")

	output, code := runIn(t, dir, binary, "run", "--no-color")

	if code != 0 {
		t.Errorf("exit = %d, want 0 with the check off\n%s", code, output)
	}
	if strings.Contains(output, "link(s) checked") {
		t.Errorf("the check ran although it was turned off:\n%s", output)
	}
}
