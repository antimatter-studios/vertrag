package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// offByOne bounds limit with `<` where the schema says `<=` — the classic
// boundary bug: every value in the middle works, and only the maximum itself
// is refused. Random probing meets it one case in a few; boundary probing
// asks it by name, every run.
func offByOne() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/things/"))
		if err != nil || id < 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if limit := r.URL.Query().Get("limit"); limit != "" {
			n, err := strconv.Atoi(limit)
			if err != nil || n < 1 || n >= 100 { // schema says maximum: 100 — the bug is `>=`
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":1}`))
	})
}

func coverageOutput(t *testing.T, endpoint string, extra ...string) (string, error) {
	t.Helper()
	description := filepath.Join(t.TempDir(), "api.yml")
	if err := os.WriteFile(description, []byte(parameterisedAPI), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--endpoint", endpoint, "--no-color"}, extra...)
	args = append(args, description)

	real := os.Stdout
	read, write, _ := os.Pipe()
	os.Stdout = write
	defer func() { os.Stdout = real }()
	captured := make(chan string, 1)
	go func() {
		text, _ := io.ReadAll(read)
		captured <- string(text)
	}()
	runErr := runCoverage(args)
	write.Close()
	return <-captured, runErr
}

// TestCoverageFindsTheOffByOne: the maximum itself is a valid probe, asked
// by name; a handler that refuses it is caught and the finding says which
// boundary.
func TestCoverageFindsTheOffByOne(t *testing.T) {
	server := httptest.NewServer(offByOne())
	defer server.Close()

	output, err := coverageOutput(t, server.URL)
	if !errors.Is(err, errFailed) {
		t.Fatalf("err = %v, want the off-by-one found; output:\n%s", err, output)
	}
	if !strings.Contains(output, "probe:   maximum (100)") {
		t.Errorf("the finding does not name the boundary it probed:\n%s", output)
	}
	if !strings.Contains(output, `query parameter "limit"`) {
		t.Errorf("the finding does not name the parameter:\n%s", output)
	}
	if !strings.Contains(output, "its own schema permits") {
		t.Errorf("a refused valid value should read as a disagreement with the description:\n%s", output)
	}
}

// TestCoverageIsCleanAgainstACarefulServer: the careful server from the fuzz
// tests enforces every bound exactly, and must produce no findings — with
// probes actually sent, so a clean run means something.
func TestCoverageIsCleanAgainstACarefulServer(t *testing.T) {
	server := httptest.NewServer(careful())
	defer server.Close()

	output, err := coverageOutput(t, server.URL)
	if err != nil {
		t.Fatalf("a careful server produced findings:\n%s", output)
	}
	if !strings.Contains(output, "0 finding(s)") || strings.Contains(output, " 0 probe(s) sent") {
		t.Errorf("not a clean, non-empty run:\n%s", output)
	}
	// Every parameter has bounds, so every one must have been probed.
	if !strings.Contains(output, "3 body and parameter target(s)") {
		t.Errorf("all three parameters should be covered:\n%s", output)
	}
}

// TestCoverageIsDeterministic pins the phase's reason to exist: two runs
// against the same server produce byte-identical output — same probes, same
// order, same findings — with no seed to pin, because there is nothing
// random to pin. That is what makes it a CI gate rather than a lottery.
func TestCoverageIsDeterministic(t *testing.T) {
	server := httptest.NewServer(offByOne())
	defer server.Close()

	first, _ := coverageOutput(t, server.URL)
	second, _ := coverageOutput(t, server.URL)

	// The signature line carries the temp path, which differs per call.
	strip := regexp.MustCompile(`vertrag [^\n]*\n`)
	if strip.ReplaceAllString(first, "") != strip.ReplaceAllString(second, "") {
		t.Errorf("two coverage runs differ:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// TestCoverageWritesAJUnitReport: one testcase per probe, named by the
// boundary it asked, so a pipeline can see which bound broke.
func TestCoverageWritesAJUnitReport(t *testing.T) {
	server := httptest.NewServer(offByOne())
	defer server.Close()

	junitPath := filepath.Join(t.TempDir(), "coverage.xml")
	output, err := coverageOutput(t, server.URL, "--reporter", "junit", "--output", junitPath)
	if !errors.Is(err, errFailed) {
		t.Fatalf("err = %v; output:\n%s", err, output)
	}
	report, readErr := os.ReadFile(junitPath)
	if readErr != nil {
		t.Fatalf("no junit file: %v", readErr)
	}
	xml := string(report)
	if !strings.Contains(xml, "maximum (100)") {
		t.Errorf("junit does not name the boundary:\n%s", xml)
	}
	if !strings.Contains(xml, "<failure") {
		t.Errorf("junit carries no failure for the off-by-one:\n%s", xml)
	}
}

// stagedAPI documents an operation's success AND its failure responses,
// the way a real description does — and the way a suite reaches the failure
// variants is a header telling the mock which failure to stage.
const stagedAPI = `openapi: 3.0.3
info:
  title: Staged
  version: 1.0.0
paths:
  /things/{id}:
    get:
      summary: Read
      parameters:
        - name: id
          in: path
          required: true
          example: 7
          schema: {type: integer, minimum: 1}
      responses:
        '200':
          description: a thing
          content:
            application/json:
              schema: {type: object}
        '404':
          description: no such thing
        '500':
          description: the server broke
`

// stagingMock breaks when — and only when — told to, exactly like a mock
// hub driven by an X-Mock-Scenario header. Otherwise it is careful.
func stagingMock() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Mock-Scenario") {
		case "broken":
			w.WriteHeader(http.StatusInternalServerError)
			return
		case "absent":
			w.WriteHeader(http.StatusNotFound)
			return
		}
		id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/things/"))
		if err != nil || id < 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	})
}

// TestProbesGoThroughTheSuccessVariantOnly reproduces the report from a real
// suite: its mock stages a failure when a conditional header — keyed on the
// transaction's EXPECTED status — asks it to. Probes sent through the 500
// variant carried "please break", the mock broke, and 94 of 105 findings
// said the server returned 500 for a generated value: true, and meaningless.
// Probing asks how an operation handles unexpected input, which is only a
// question against the request meant to succeed — so each operation is
// probed once, through its lowest 2xx variant, and a careful mock produces
// no findings however many failure variants the description documents.
func TestProbesGoThroughTheSuccessVariantOnly(t *testing.T) {
	server := httptest.NewServer(stagingMock())
	defer server.Close()

	dir := t.TempDir()
	description := filepath.Join(dir, "staged.yml")
	os.WriteFile(description, []byte(stagedAPI), 0o600)
	cfg := filepath.Join(dir, "vertrag.yml")
	os.WriteFile(cfg, []byte("spec: "+description+"\nendpoint: "+server.URL+"\nheader:\n  - {name: X-Mock-Scenario, value: absent, when: {status: 404}}\n  - {name: X-Mock-Scenario, value: broken, when: {status: 500}}\n"), 0o600)

	real := os.Stdout
	read, write, _ := os.Pipe()
	os.Stdout = write
	captured := make(chan string, 1)
	go func() { text, _ := io.ReadAll(read); captured <- string(text) }()
	err := runCoverage([]string{"--config", cfg, "--no-color"})
	write.Close()
	os.Stdout = real
	output := <-captured

	if err != nil {
		t.Fatalf("a careful mock produced findings — probes went through a failure variant:\n%s", output)
	}
	// One operation, probed once: not once per documented response.
	if !strings.Contains(output, "1 operation(s) covered") {
		t.Errorf("the operation should be covered once, not per response variant:\n%s", output)
	}
	if strings.Contains(output, "returned 500") {
		t.Errorf("a staged 500 was reported as a finding:\n%s", output)
	}
}
