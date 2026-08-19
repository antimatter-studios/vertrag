package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A description with enough operations for concurrency to be visible, each
// carrying a bounded integer so the coverage phase has boundaries to send.
func manyOperations(count int) string {
	var b strings.Builder
	b.WriteString("openapi: 3.0.3\ninfo: {title: Many, version: \"1.0\"}\npaths:\n")
	for i := range count {
		fmt.Fprintf(&b, `  /thing%d:
    post:
      operationId: makeThing%d
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [size]
              properties:
                size: {type: integer, minimum: 1, maximum: 50}
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
`, i, i)
	}
	return b.String()
}

func coverageProject(t *testing.T, endpoint string, operations int, extra string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(manyOperations(operations)), 0o600); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("spec: ./api.yml\nendpoint: %s\n%s", endpoint, extra)
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// slowServer answers after a delay and records the highest number of requests
// it was ever handling at once.
type slowServer struct {
	mu       sync.Mutex
	inFlight int
	peak     int
	total    atomic.Int64
	delay    time.Duration
}

func (s *slowServer) start(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.inFlight++
		if s.inFlight > s.peak {
			s.peak = s.inFlight
		}
		s.mu.Unlock()

		s.total.Add(1)
		time.Sleep(s.delay)

		s.mu.Lock()
		s.inFlight--
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func (s *slowServer) peakInFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

// TestTheCoveragePhaseProbesSeveralOperationsAtOnce pins the capability. The
// fuzz phase cannot do this — rapid takes its case count and seed from
// process-global flags, so two probes at once would overwrite each other's seed
// — but coverage draws nothing at random and so shares nothing.
func TestTheCoveragePhaseProbesSeveralOperationsAtOnce(t *testing.T) {
	binary := build(t)

	server := &slowServer{delay: 25 * time.Millisecond}
	dir := coverageProject(t, server.start(t), 8, "")

	if _, code := runIn(t, dir, binary, "run", "--phases", "coverage", "--workers", "4"); code > 2 {
		t.Fatalf("the run did not complete, exit %d", code)
	}
	if peak := server.peakInFlight(); peak < 2 {
		t.Errorf("peak concurrent requests = %d; --workers 4 probed one at a time", peak)
	}
}

// TestOneWorkerIsStillOneRequestAtATime is the guard that keeps the test above
// from passing on a server that miscounts: with one worker the peak must be
// one, so the measurement is known to mean what it says.
func TestOneWorkerIsStillOneRequestAtATime(t *testing.T) {
	binary := build(t)

	server := &slowServer{delay: 10 * time.Millisecond}
	dir := coverageProject(t, server.start(t), 5, "")

	if _, code := runIn(t, dir, binary, "run", "--phases", "coverage"); code > 2 {
		t.Fatalf("the run did not complete, exit %d", code)
	}
	if peak := server.peakInFlight(); peak != 1 {
		t.Errorf("peak concurrent requests = %d with no --workers; want 1", peak)
	}
}

// TestTheCoverageReportIsTheSameWhateverTheWorkerCount is the property that
// makes turning workers up safe, and the reason each operation accumulates into
// its own buffer and is merged in description order afterwards.
//
// A report whose findings arrive in a different sequence each run cannot be
// diffed between runs, and interleaved output from four workers is unreadable.
// So concurrency is allowed to change how long the phase takes and nothing
// else.
func TestTheCoverageReportIsTheSameWhateverTheWorkerCount(t *testing.T) {
	binary := build(t)

	// A server that fails one specific operation, so there is a finding whose
	// position in the report can move.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/thing3") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	// Durations are stripped before comparing, and only durations.
	//
	// The claim is that concurrency changes how long the phase takes and
	// nothing else — so the one thing it is allowed to change is exactly the
	// measured time, and leaving it in would make this test assert something
	// stronger than the property and fail on `(2ms)` against `(1ms)`. Every
	// other byte still has to match: the findings, their order, the counts, the
	// summary.
	timings := regexp.MustCompile(`\(\d+(?:\.\d+)?(?:ns|µs|ms|s)\)`)

	// And the clock in the response headers, for the same reason and with the
	// same care. CI caught this one: two runs of the eight straddled a second
	// boundary, so the reports differed by `date: … 05:03:32 GMT` against
	// `… 05:03:33 GMT` and nothing else at all. That is the wall clock, not
	// the worker count — a byte-identical assertion over output containing a
	// timestamp asserts something stronger than the property and eventually
	// fails on the property being true.
	dates := regexp.MustCompile(`(?i)date: [A-Za-z]{3}, [^\n]*GMT`)

	var reports []string
	for _, workers := range []string{"1", "2", "4", "8"} {
		dir := coverageProject(t, server.URL, 8, "")
		output, _ := runIn(t, dir, binary, "run", "--phases", "coverage", "--workers", workers)
		normalised := timings.ReplaceAllString(output, "(elapsed)")
		reports = append(reports, dates.ReplaceAllString(normalised, "date: (when)"))
	}

	for i := 1; i < len(reports); i++ {
		if reports[i] != reports[0] {
			t.Errorf("the report changed with the worker count.\n--- 1 worker ---\n%s\n--- run %d ---\n%s",
				reports[0], i, reports[i])
			break
		}
	}
}

// TestThePinStillHoldsWithWorkers guards the interlock against the
// parallelisation: each worker gets its own copy of the mutable options, and a
// copy that dropped the pin would be the worst possible bug to introduce here.
func TestThePinStillHoldsWithWorkers(t *testing.T) {
	binary := build(t)

	server := &orderServer{}
	dir := project(t, server.start(t), "fuzz:\n  pin:\n    dry_run: true\n")
	if _, code := runIn(t, dir, binary, "run", "--phases", "coverage", "--workers", "4"); code > 2 {
		t.Fatal("the run did not complete")
	}
	live, all := server.counts()
	if all == 0 {
		t.Fatal("nothing was sent, so the pin proved nothing")
	}
	if live != 0 {
		t.Errorf("%d of %d requests reached the server unpinned with workers on", live, all)
	}
}
