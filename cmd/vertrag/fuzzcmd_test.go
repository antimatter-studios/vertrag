package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/fuzz"
)

// parameterisedAPI describes one operation whose every parameter carries
// constraints: a bounded path integer, a bounded query integer, and a header
// string with a length. Each is a constraint a handler can forget separately.
const parameterisedAPI = `openapi: 3.0.3
info:
  title: Things
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
          schema:
            type: integer
            minimum: 1
        - name: limit
          in: query
          example: 10
          schema:
            type: integer
            minimum: 1
            maximum: 100
        - name: X-Tenant
          in: header
          example: acme
          schema:
            type: string
            minLength: 3
            maxLength: 8
      responses:
        '200':
          description: a thing
          content:
            application/json:
              schema:
                type: object
                properties:
                  id:
                    type: integer
`

// careless is the server the end-to-end test is aimed at: it uses the path id
// without checking it parses, and never looks at the query bounds at all. Both
// are what a handler looks like when its author assumed the framework had
// already validated the request against the description.
func careless() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/things/"))
		if err != nil {
			// A framework turning the handler's panic into a 500, which is what
			// an unvalidated parameter produces in practice.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":` + strconv.Itoa(id) + `}`))
	})
}

// careful enforces everything the description states, and answers 404 for any
// well-formed id it does not hold — which a run must not report, since the
// description promises which ids are well formed and not which ones exist.
func careful() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/things/"))
		if err != nil || id < 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if limit := r.URL.Query().Get("limit"); limit != "" {
			n, err := strconv.Atoi(limit)
			if err != nil || n < 1 || n > 100 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		if tenant := r.Header.Get("X-Tenant"); tenant != "" && (len(tenant) < 3 || len(tenant) > 8) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if id != 7 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":7}`))
	})
}

// TestTheTwoPackagesNameLocationsTheSameWay pins the one join between the
// compiler's vocabulary and the prober's.
//
// The compiler says where a parameter travels and the prober repeats it in the
// report and judges by it — a path parameter's 404 is forgiven and a query
// parameter's is not. They are separate packages with separate constants, so
// nothing but this stops one being renamed: the strings would simply stop
// matching, the exemption would apply to nothing, and a run would start
// reporting every missing resource as a contract violation.
func TestTheTwoPackagesNameLocationsTheSameWay(t *testing.T) {
	for _, pair := range [][2]string{
		{compile.InPath, fuzz.InPath},
		{compile.InQuery, fuzz.InQuery},
		{compile.InHeader, fuzz.InHeader},
	} {
		if pair[0] != pair[1] {
			t.Errorf("compile says %q where fuzz says %q", pair[0], pair[1])
		}
	}
}

// TestFuzzFindsParameterGapsEndToEnd is the whole pipeline in one test: a real
// description parsed, compiled, and probed against a real server over HTTP.
//
// It is here rather than in the fuzz package because most of the ways this
// breaks are joins between packages, and every one of them fails silently. A
// parser that drops a parameter's schema, a compiler that does not carry it, a
// probe loop that only looks at bodies — each leaves a run that sends its
// requests, reports no findings, and looks exactly like a clean bill of health.
func TestFuzzFindsParameterGapsEndToEnd(t *testing.T) {
	server := httptest.NewServer(careless())
	defer server.Close()

	output, err := fuzzOutput(t, server.URL, "--cases", "40", "--mode", "invalid")

	if !errors.Is(err, errFailed) {
		t.Fatalf("err = %v, want the run to fail; output:\n%s", err, output)
	}

	// Every location has to be reported, and reported as itself: a message
	// naming the operation but not the parameter leaves the reader to work out
	// which of three inputs was at fault.
	for _, want := range []string{
		`path parameter "id"`,
		`query parameter "limit"`,
		`header parameter "X-Tenant"`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("no finding mentions %s:\n%s", want, output)
		}
	}

	// A finding must be repeatable with one paste, which takes the absolute
	// address — the relative URI printed above only means something next to an
	// endpoint the reader would have to reconstruct.
	if !strings.Contains(output, "repro:  curl -X GET '"+server.URL) {
		t.Errorf("no curl repro line carrying the endpoint:\n%s", output)
	}

	// The request shown is the one that failed, not the compiled example, or
	// the report cannot be repeated by hand.
	if !strings.Contains(output, "request: GET /things/") {
		t.Errorf("the finding does not show the request that was sent:\n%s", output)
	}
	if strings.Contains(output, "0 finding(s)") {
		t.Errorf("the summary contradicts the findings:\n%s", output)
	}
}

// TestFuzzReportsNothingAgainstACarefulServer is the half that makes the other
// half worth having.
//
// A tool that reports findings against a correct server costs more than it
// saves: the first false one is investigated, the second is argued about, and
// after that the run is turned off. Everything generated for a parameter goes
// out as text, so this fails the moment a value is judged as something other
// than what the server actually received.
func TestFuzzReportsNothingAgainstACarefulServer(t *testing.T) {
	server := httptest.NewServer(careful())
	defer server.Close()

	output, err := fuzzOutput(t, server.URL, "--cases", "60")

	if err != nil {
		t.Fatalf("a correct server produced findings:\n%s", output)
	}
	if !strings.Contains(output, "0 finding(s)") {
		t.Errorf("summary does not report a clean run:\n%s", output)
	}
	// A clean run only means something if requests were actually sent.
	if strings.Contains(output, " 0 request(s) sent") {
		t.Errorf("nothing was sent, so nothing was tested:\n%s", output)
	}
}

// TestFuzzReportsItsSeed pins the line that makes replay possible at all.
//
// The seed used to be chosen inside rapid and reported only into a log nothing
// read, so a default run — seed zero — could never be replayed, despite the
// README promising exactly that. The run now picks its own seed and prints it,
// and this fails if that line ever goes quiet again.
func TestFuzzReportsItsSeed(t *testing.T) {
	server := httptest.NewServer(careful())
	defer server.Close()

	// An explicit seed is echoed back, so the value on screen is always the
	// value in use.
	output, err := fuzzOutput(t, server.URL)
	if err != nil {
		t.Fatalf("a correct server produced findings:\n%s", output)
	}
	if !strings.Contains(output, "seed: 9 (replay with --seed 9)") {
		t.Errorf("the explicit seed is not reported:\n%s", output)
	}

	// A run left to pick its own must say which one it picked.
	output, err = fuzzOutput(t, server.URL, "--seed", "0")
	if err != nil {
		t.Fatalf("a correct server produced findings:\n%s", output)
	}
	if reportedSeed(output) == 0 {
		t.Errorf("no usable seed is reported for a run that picked its own:\n%s", output)
	}
}

// reportedSeed digs the seed out of a run's output, zero when there is none.
func reportedSeed(output string) uint64 {
	for _, line := range strings.Split(output, "\n") {
		rest, found := strings.CutPrefix(line, "seed: ")
		if !found {
			continue
		}
		number, _, _ := strings.Cut(rest, " ")
		seed, err := strconv.ParseUint(number, 10, 64)
		if err != nil {
			return 0
		}
		return seed
	}
	return 0
}

// TestFuzzReplaysExactly is the promise behind the seed line: the same seed
// against the same server is the same run, byte for byte. Without this the
// printed seed is decoration — a finding replayed with it would come back
// different, and the first time that happens the report stops being trusted.
func TestFuzzReplaysExactly(t *testing.T) {
	server := httptest.NewServer(careless())
	defer server.Close()

	first, errFirst := fuzzOutput(t, server.URL, "--cases", "25", "--mode", "invalid")
	second, errSecond := fuzzOutput(t, server.URL, "--cases", "25", "--mode", "invalid")

	if !errors.Is(errFirst, errFailed) || !errors.Is(errSecond, errFailed) {
		t.Fatalf("errs = %v, %v; want both runs to fail", errFirst, errSecond)
	}
	if first != second {
		t.Errorf("two runs with the same seed differ:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// fuzzOutput runs `vertrag fuzz` against an endpoint and returns what it
// printed, so a test can assert on the report a user would read.
func fuzzOutput(t *testing.T, endpoint string, extra ...string) (string, error) {
	t.Helper()

	description := filepath.Join(t.TempDir(), "api.yml")
	if err := os.WriteFile(description, []byte(parameterisedAPI), 0o600); err != nil {
		t.Fatalf("writing the description: %v", err)
	}

	args := append([]string{"--endpoint", endpoint, "--no-color", "--seed", "9"}, extra...)
	args = append(args, description)

	// The command reports through os.Stdout, which is the surface being tested:
	// a finding nobody can read is not a finding.
	real := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = write
	defer func() { os.Stdout = real }()

	captured := make(chan string, 1)
	go func() {
		text, _ := io.ReadAll(read)
		captured <- string(text)
	}()

	runErr := runFuzz(args)

	write.Close()
	return <-captured, runErr
}
