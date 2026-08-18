package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The description a trading API would write: a flag that decides whether the
// request does something irreversible, which the schema is equally happy to see
// either way. Nothing in OpenAPI marks `dry_run: false` as the dangerous one.
const orderDescription = `
openapi: 3.0.3
info: {title: Orders, version: "1.0"}
paths:
  /orders:
    post:
      operationId: placeOrder
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [symbol, quantity, dry_run]
              properties:
                symbol: {type: string, minLength: 1, maxLength: 8}
                quantity: {type: integer, minimum: 1, maximum: 100}
                dry_run: {type: boolean}
      responses:
        "200": {description: accepted, content: {application/json: {schema: {type: object}}}}
`

// orderServer counts the requests that would have placed a real order.
type orderServer struct {
	mu   sync.Mutex
	live int
	all  int
}

func (s *orderServer) start(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		s.mu.Lock()
		s.all++
		if body["dry_run"] != true {
			s.live++
		}
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func (s *orderServer) counts() (live, all int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live, s.all
}

// runIn is runCommand with a working directory, since these runs are driven by
// a vertrag.yml that has to be discovered rather than named.
func runIn(t *testing.T, dir, binary string, args ...string) (output string, code int) {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Dir = dir
	out, err := command.CombinedOutput()
	if exit, isExit := err.(*exec.ExitError); isExit {
		return string(out), exit.ExitCode()
	}
	if err != nil {
		t.Fatalf("running vertrag: %v\n%s", err, out)
	}
	return string(out), 0
}

// project writes a description and a vertrag.yml into a temporary directory and
// returns it.
func project(t *testing.T, endpoint, fuzzSection string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(orderDescription), 0o600); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("spec: ./api.yml\nendpoint: %s\n%s", endpoint, fuzzSection)
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestAPinnedFieldNeverReachesTheServerUnpinned is the test the whole feature
// answers to. Without it, a probing phase pointed at an ordering endpoint
// places real orders — which is the reason a real project could not use these
// phases at all.
func TestAPinnedFieldNeverReachesTheServerUnpinned(t *testing.T) {
	binary := build(t)

	// First, unpinned, to establish that the danger is real and this test is
	// not passing for want of anything to catch.
	loose := &orderServer{}
	dir := project(t, loose.start(t), "fuzz:\n  cases: 20\n  seed: 7\n")
	if _, code := runIn(t, dir, binary, "fuzz"); code > 2 {
		t.Fatalf("the unpinned run did not complete, exit %d", code)
	}
	liveBefore, allBefore := loose.counts()
	if allBefore == 0 {
		t.Fatal("nothing was sent, so neither run proves anything")
	}
	if liveBefore == 0 {
		t.Fatalf("generation never drew dry_run:false in %d requests; the pin test below would prove nothing", allBefore)
	}
	t.Logf("unpinned: %d of %d requests would have placed a real order", liveBefore, allBefore)

	// Now the same run with the pin.
	held := &orderServer{}
	dir = project(t, held.start(t), "fuzz:\n  cases: 20\n  seed: 7\n  pin:\n    dry_run: true\n")
	output, code := runIn(t, dir, binary, "fuzz")
	if code > 2 {
		t.Fatalf("the pinned run did not complete, exit %d:\n%s", code, output)
	}
	liveAfter, allAfter := held.counts()
	if allAfter == 0 {
		t.Fatal("the pinned run sent nothing, so the pin proved nothing")
	}
	if liveAfter != 0 {
		t.Errorf("%d of %d requests reached the server unpinned", liveAfter, allAfter)
	}

	// And the run says what it held, because a pin nobody can see is one
	// nobody can verify.
	if !strings.Contains(output, "dry_run") {
		t.Errorf("the run does not say what it pinned:\n%s", output)
	}
}

// TestAPinNamingAFieldNoBodyDeclaresStopsTheRun pins the refusal, end to end.
// A typo here is the difference between a safety control and the appearance of
// one, and the run must not start.
func TestAPinNamingAFieldNoBodyDeclaresStopsTheRun(t *testing.T) {
	binary := build(t)
	server := &orderServer{}
	dir := project(t, server.start(t), "fuzz:\n  cases: 5\n  pin:\n    dryrun: true\n")

	output, code := runIn(t, dir, binary, "fuzz")
	if code == 0 {
		t.Fatalf("a pin naming nothing should stop the run:\n%s", output)
	}
	if !strings.Contains(output, "dryrun") {
		t.Errorf("the refusal does not name the field:\n%s", output)
	}
	if _, all := server.counts(); all != 0 {
		t.Errorf("%d requests were sent before the pin was checked; it must be checked first", all)
	}
}

// TestThePinHoldsInTheCoveragePhaseToo: both probing phases generate bodies, so
// a pin that held on one and not the other would not be a pin.
func TestThePinHoldsInTheCoveragePhaseToo(t *testing.T) {
	binary := build(t)
	server := &orderServer{}
	dir := project(t, server.start(t), "fuzz:\n  pin:\n    dry_run: true\n")

	output, code := runIn(t, dir, binary, "coverage")
	if code > 2 {
		t.Fatalf("the coverage run did not complete, exit %d:\n%s", code, output)
	}
	live, all := server.counts()
	if all == 0 {
		t.Fatal("coverage sent nothing, so the pin proved nothing")
	}
	if live != 0 {
		t.Errorf("%d of %d coverage requests reached the server unpinned", live, all)
	}
}

// TestAnExcusedAnswerIsCountedInTheOutput pins the condition on which accepting
// is worth offering: the run says how much it did not report. A suite that
// quietly stopped testing anything shows up as a suppression count somebody can
// read, where a hidden finding is a number nobody can see.
func TestAnExcusedAnswerIsCountedInTheOutput(t *testing.T) {
	binary := build(t)

	// A server that refuses everything for a business reason the description
	// does not carry — the case `accept` exists for, and also exactly what a
	// broken endpoint looks like.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()

	dir := project(t, server.URL, "fuzz:\n  cases: 8\n  seed: 3\n  accept: [409]\n")
	output, _ := runIn(t, dir, binary, "fuzz")

	if !strings.Contains(output, "excused by fuzz.accept") {
		t.Errorf("the run did not report what it excused:\n%s", output)
	}
	if strings.Contains(output, "0 answer(s) excused") {
		t.Errorf("answers were excused and the count says none:\n%s", output)
	}
}

// TestTheFuzzCommandHonoursTheConfigsSeedAndCases pins a gap found while
// testing the pin: `vertrag run --phases fuzz` read `seed`, `cases` and
// `whole-request` from the file and `vertrag fuzz` — the command named after
// that section — silently did not. A pinned seed that works through one of two
// entry points is worse than none, because the run ignoring it still prints a
// seed and still looks reproducible.
func TestTheFuzzCommandHonoursTheConfigsSeedAndCases(t *testing.T) {
	binary := build(t)

	first := &orderServer{}
	dir := project(t, first.start(t), "fuzz:\n  cases: 4\n  seed: 99\n  pin:\n    dry_run: true\n")
	output, _ := runIn(t, dir, binary, "fuzz")
	if !strings.Contains(output, "seed: 99") {
		t.Errorf("the config's seed was ignored:\n%s", output)
	}
	_, firstCount := first.counts()

	// The same configuration twice sends the same number of requests, which is
	// what a pinned seed is for.
	second := &orderServer{}
	dir = project(t, second.start(t), "fuzz:\n  cases: 4\n  seed: 99\n  pin:\n    dry_run: true\n")
	runIn(t, dir, binary, "fuzz")
	if _, secondCount := second.counts(); secondCount != firstCount {
		t.Errorf("the same seed sent %d requests then %d", firstCount, secondCount)
	}

	// And a flag still wins over the file.
	third := &orderServer{}
	dir = project(t, third.start(t), "fuzz:\n  cases: 4\n  seed: 99\n  pin:\n    dry_run: true\n")
	output, _ = runIn(t, dir, binary, "fuzz", "--seed", "7")
	if !strings.Contains(output, "seed: 7") {
		t.Errorf("the flag did not win over the file:\n%s", output)
	}
}
