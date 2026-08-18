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

// TestFuzzWritesAJUnitReport pins the --reporter flag: a CI system consumes
// probe results as a junit file, one testcase per probe, findings as failures
// — without which fuzz results live only in a terminal scrollback nobody's
// pipeline can act on.
func TestFuzzWritesAJUnitReport(t *testing.T) {
	server := httptest.NewServer(careless())
	defer server.Close()

	junitPath := filepath.Join(t.TempDir(), "fuzz.xml")
	output, err := fuzzOutput(t, server.URL,
		"--cases", "40", "--mode", "invalid", "--reporter", "junit", "--output", junitPath)

	if !errors.Is(err, errFailed) {
		t.Fatalf("err = %v, want the run to fail; output:\n%s", err, output)
	}

	// The narrative is unchanged by the flag: findings still reach stdout.
	if !strings.Contains(output, "finding:") {
		t.Errorf("the narrative went missing when a reporter was added:\n%s", output)
	}

	report, readErr := os.ReadFile(junitPath)
	if readErr != nil {
		t.Fatalf("no junit file was written: %v", readErr)
	}
	xml := string(report)
	for _, want := range []string{
		"<testsuite",
		`path parameter &#34;id&#34; · invalid`,
		"failed rather than rejected",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("junit report missing %q:\n%s", want, xml)
		}
	}
	// Probes that behaved appear as passing testcases, so the suite's totals
	// describe what was tested rather than only what failed.
	if !strings.Contains(xml, "tests=") || strings.Contains(xml, `tests="0"`) {
		t.Errorf("the report does not count its probes:\n%s", xml)
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

// styledAPI declares the parameter styles a form-only prober used to skip: a
// deepObject filter (an object, which had no wire form at all) and a
// pipe-delimited list. Both carry constraints a handler can forget.
const styledAPI = `openapi: 3.0.3
info:
  title: Styled
  version: 1.0.0
paths:
  /items:
    get:
      summary: List
      parameters:
        - name: filter
          in: query
          style: deepObject
          example: {size: 5}
          schema:
            type: object
            properties:
              size:
                type: integer
                minimum: 1
                maximum: 10
        - name: ids
          in: query
          style: pipeDelimited
          example: [1, 2]
          schema:
            type: array
            items:
              type: integer
              minimum: 1
      responses:
        '200':
          description: items
          content:
            application/json:
              schema:
                type: object
`

// carelessStyled parses filter[size] and each pipe-separated id without
// checking bounds and 500s on anything that is not a number — the classic
// unvalidated-parameter handler, but reached through deepObject and pipe
// syntax that the prober could not previously speak.
func carelessStyled() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if size := r.URL.Query().Get("filter[size]"); size != "" {
			if _, err := strconv.Atoi(size); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		if ids := r.URL.Query().Get("ids"); ids != "" {
			for _, id := range strings.Split(ids, "|") {
				if _, err := strconv.Atoi(id); err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	})
}

// carefulStyled enforces every bound and answers 400, so a correct server
// must produce no findings however the values are laid out on the wire.
func carefulStyled() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if size := r.URL.Query().Get("filter[size]"); size != "" {
			n, err := strconv.Atoi(size)
			if err != nil || n < 1 || n > 10 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		if ids := r.URL.Query().Get("ids"); ids != "" {
			for _, id := range strings.Split(ids, "|") {
				n, err := strconv.Atoi(id)
				if err != nil || n < 1 {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	})
}

func fuzzStyledOutput(t *testing.T, endpoint string, extra ...string) (string, error) {
	t.Helper()
	description := filepath.Join(t.TempDir(), "styled.yml")
	if err := os.WriteFile(description, []byte(styledAPI), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--endpoint", endpoint, "--no-color", "--seed", "9"}, extra...)
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
	runErr := runFuzz(args)
	write.Close()
	return <-captured, runErr
}

// TestFuzzProbesDeepObjectAndPipeDelimitedParameters is the milestone in one
// test: parameters that were skipped for want of a wire form are now sent in
// the layout the description chose, and a handler careless about them is
// caught — named by parameter, so the reader knows which of two inputs.
func TestFuzzProbesDeepObjectAndPipeDelimitedParameters(t *testing.T) {
	server := httptest.NewServer(carelessStyled())
	defer server.Close()

	output, err := fuzzStyledOutput(t, server.URL, "--cases", "40", "--mode", "invalid")
	if !errors.Is(err, errFailed) {
		t.Fatalf("err = %v, want findings; output:\n%s", err, output)
	}
	for _, want := range []string{`query parameter "filter"`, `query parameter "ids"`} {
		if !strings.Contains(output, want) {
			t.Errorf("no finding names %s:\n%s", want, output)
		}
	}
	// The request shown must carry the style's own syntax, or the finding
	// cannot be repeated by hand.
	if !strings.Contains(output, "filter%5B") && !strings.Contains(output, "filter[") {
		t.Errorf("the deepObject request is not shown in deepObject syntax:\n%s", output)
	}
}

// TestFuzzStyledParametersAgainstACarefulServer is the half that keeps the
// other honest: laying values out by style must not invent findings against
// a server that enforces its description.
func TestFuzzStyledParametersAgainstACarefulServer(t *testing.T) {
	server := httptest.NewServer(carefulStyled())
	defer server.Close()

	output, err := fuzzStyledOutput(t, server.URL, "--cases", "60")
	if err != nil {
		t.Fatalf("a correct server produced findings:\n%s", output)
	}
	if !strings.Contains(output, "0 finding(s)") {
		t.Errorf("summary does not report a clean run:\n%s", output)
	}
	if strings.Contains(output, " 0 request(s) sent") {
		t.Errorf("nothing was sent, so nothing was tested:\n%s", output)
	}
	// Both parameters must actually have been probed — a clean run that
	// skipped them proves nothing.
	if !strings.Contains(output, "2 body and parameter target(s)") {
		t.Errorf("both styled parameters should be probed:\n%s", output)
	}
}

// formAPI posts a form-encoded body: the shape a login page or a classic
// HTML form sends, and one a JSON-only prober had to leave untested.
const formAPI = `openapi: 3.0.3
info:
  title: Forms
  version: 1.0.0
paths:
  /signup:
    post:
      summary: Sign up
      requestBody:
        required: true
        content:
          application/x-www-form-urlencoded:
            schema:
              type: object
              required: [name, age]
              properties:
                name:
                  type: string
                  minLength: 1
                age:
                  type: integer
                  minimum: 18
            example: {name: ann, age: 30}
      responses:
        '201':
          description: created
          content:
            application/json:
              schema:
                type: object
`

// carelessForm parses age without checking it is a number or its bound.
func carelessForm() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, err := strconv.Atoi(r.PostForm.Get("age")); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	})
}

// carefulForm enforces the whole schema.
func carefulForm() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		age, err := strconv.Atoi(r.PostForm.Get("age"))
		if err != nil || age < 18 || r.PostForm.Get("name") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	})
}

func fuzzFormOutput(t *testing.T, endpoint string, extra ...string) (string, error) {
	t.Helper()
	description := filepath.Join(t.TempDir(), "form.yml")
	if err := os.WriteFile(description, []byte(formAPI), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--endpoint", endpoint, "--no-color", "--seed", "9"}, extra...)
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
	runErr := runFuzz(args)
	write.Close()
	return <-captured, runErr
}

// TestFuzzProbesFormEncodedBodies: a form body is generated in its own
// layout and a handler careless about a field is caught. Before, a request
// whose content type was not JSON had its body left alone entirely.
func TestFuzzProbesFormEncodedBodies(t *testing.T) {
	server := httptest.NewServer(carelessForm())
	defer server.Close()

	output, err := fuzzFormOutput(t, server.URL, "--cases", "40", "--mode", "invalid")
	if !errors.Is(err, errFailed) {
		t.Fatalf("err = %v, want a finding; output:\n%s", err, output)
	}
	if !strings.Contains(output, "body:") {
		t.Errorf("no finding names the body:\n%s", output)
	}
	// The body shown must be form-encoded, or the finding cannot be repeated.
	if !strings.Contains(output, "age=") {
		t.Errorf("the finding does not show a form-encoded body:\n%s", output)
	}
	if strings.Contains(output, "1 transaction(s) skipped for having no schema") {
		t.Errorf("the form body was skipped instead of probed:\n%s", output)
	}
}

// TestFuzzFormBodiesAgainstACarefulServer keeps the other half honest.
func TestFuzzFormBodiesAgainstACarefulServer(t *testing.T) {
	server := httptest.NewServer(carefulForm())
	defer server.Close()

	output, err := fuzzFormOutput(t, server.URL, "--cases", "60")
	if err != nil {
		t.Fatalf("a correct server produced findings:\n%s", output)
	}
	if !strings.Contains(output, "0 finding(s)") || strings.Contains(output, " 0 request(s) sent") {
		t.Errorf("not a clean, non-empty run:\n%s", output)
	}
}
