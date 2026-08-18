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
