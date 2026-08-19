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

// A description with a documented success and a documented refusal, which is
// what makes a status-conditional header worth writing in the first place.
const stagedLoginAPI = `openapi: 3.0.3
info: {title: Staged, version: "1.0"}
paths:
  /login:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [username, password]
              properties:
                username: {type: string}
                password: {type: string}
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
        "401": {description: refused, content: {application/json: {schema: {type: object}}}}
`

// stagedServer refuses everything unless the staging header is present, which
// is how a mock that serves a chosen scenario behaves.
func stagedServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := http.StatusUnauthorized
		if r.Header.Get("X-Mock-Scenario") != "" {
			code = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func stagedProject(t *testing.T, endpoint string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(stagedLoginAPI), 0o600); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`spec: ./api.yml
endpoint: %s
header:
  - name: X-Mock-Scenario
    value: success
    when: {status: 200}
phases: [examples, fuzz]
fuzz: {cases: 4, seed: 1}
`, endpoint)
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestAStagingHeaderDoesNotDecideWhatAProbeIsJudgedBy pins the worst finding
// this tool has produced.
//
// A header conditioned on the documented status exists to make a mock serve
// that documented answer. Probing goes through the success variant, so the
// header rode along on every generated request — telling the server to answer
// 200 and then reporting the server for answering 200. Against a real project
// it manufactured "the server returned 200 for a login body with no password",
// on an API whose login endpoint answers 401 to exactly that request. They
// escalated it as a security finding, and it was ours.
func TestAStagingHeaderDoesNotDecideWhatAProbeIsJudgedBy(t *testing.T) {
	binary := build(t)
	server := stagedServer(t)

	output, _ := runIn(t, stagedProject(t, server.URL), binary, "run")

	if strings.Contains(output, "schema forbids") {
		t.Errorf("a staged 200 was reported as the server accepting a body its schema forbids:\n%s", output)
	}
}

// TestTheExamplesPhaseStillStagesItsDocumentedResponses is the other half. The
// header is not wrong — it is how a mock is asked for a documented answer — so
// removing it from the documented requests would break the suites that need it.
// Only the generated requests lose it.
func TestTheExamplesPhaseStillStagesItsDocumentedResponses(t *testing.T) {
	binary := build(t)
	server := stagedServer(t)

	// --no-color, because the status word carries ANSI codes between "pass"
	// and its colon and an assertion on the coloured form silently never
	// matches.
	output, _ := runIn(t, stagedProject(t, server.URL), binary, "run", "--phases", "examples", "--no-color")

	// Both documented transactions pass only if the header went with the one
	// that needs it; without it the 200 case gets a 401 and fails.
	if !strings.Contains(output, "2 passing, 0 failing") {
		t.Errorf("the staging header did not reach the documented request:\n%s", output)
	}
}

// TestAProbesReproLineCarriesEveryHeaderItSent pins the reporting half, which
// is what made the finding above unfalsifiable rather than merely wrong.
//
// The repro line was rebuilt by hand from the transaction's headers and the
// run-wide --header list, so it omitted the credential and the conditional
// headers — exactly the ones the RUN adds and the reader does not know about. A
// project ran it character for character, got a different status, and
// reasonably concluded the tool had invented the finding.
func TestAProbesReproLineCarriesEveryHeaderItSent(t *testing.T) {
	binary := build(t)
	server := stagedServer(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(stagedLoginAPI), 0o600); err != nil {
		t.Fatal(err)
	}
	// A plain run-wide header, which the runner adds and the description does
	// not mention: it must appear in the repro line of a probe finding.
	config := fmt.Sprintf("spec: ./api.yml\nendpoint: %s\nheader: ['X-Tenant: acme']\nphases: [examples, fuzz]\nfuzz: {cases: 4, seed: 1}\n", server.URL)
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	output, _ := runIn(t, dir, binary, "run")
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "curl -X") && !strings.Contains(line, "X-Tenant: acme") {
			t.Errorf("a repro line omits a header the run sent, so it does not reproduce:\n%s", line)
		}
	}
}
