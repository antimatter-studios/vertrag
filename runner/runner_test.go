package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/compile"
)

// transaction builds a compiled transaction the way the compiler would.
func transaction(method, uri, status, body, schema string) compile.Transaction {
	return compile.Transaction{
		Name: method + " " + uri,
		Request: compile.Request{
			Method:  method,
			URI:     uri,
			Headers: []compile.Header{{Name: "Accept", Value: "application/json"}},
		},
		Response: compile.Response{
			Status:  status,
			Headers: []compile.Header{{Name: "Content-Type", Value: "application/json"}},
			Body:    body,
			Schema:  schema,
		},
	}
}

func TestRunPassesAndFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ok":
			fmt.Fprint(w, `{"id":"1"}`)
		case "/wrong-shape":
			fmt.Fprint(w, `{"other":"value"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	results, err := New(server.URL).Run(context.Background(), []compile.Transaction{
		transaction("GET", "/ok", "200", `{"id":"x"}`, ""),
		transaction("GET", "/wrong-shape", "200", `{"id":"x"}`, ""),
		transaction("GET", "/absent", "200", "", ""),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []Status{StatusPass, StatusFail, StatusFail}
	for i, status := range want {
		if results[i].Status != status {
			t.Errorf("result %d status = %q, want %q (errors: %v)",
				i, results[i].Status, status, results[i].Errors)
		}
	}

	// A failure names the field that disagreed, so a report says what to fix.
	if !strings.HasPrefix(results[1].Errors[0], "body:") {
		t.Errorf("body failure should be labelled: %v", results[1].Errors)
	}
	if !strings.HasPrefix(results[2].Errors[0], "statusCode:") {
		t.Errorf("status failure should be labelled: %v", results[2].Errors)
	}
}

func TestRequestIsSentAsDescribed(t *testing.T) {
	var got *http.Request
	var body string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		payload := make([]byte, r.ContentLength)
		r.Body.Read(payload)
		body = string(payload)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	source := transaction("POST", "/things", "201", "", "")
	source.Request.Body = `{"name":"x"}`
	source.Request.Headers = append(source.Request.Headers,
		compile.Header{Name: "Content-Type", Value: "application/json"})
	source.Response.Headers = nil

	engine := New(server.URL)
	engine.ExtraHeaders = []string{"X-Extra: added", "Malformed"}

	if _, err := engine.Run(context.Background(), []compile.Transaction{source}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got.Method != "POST" || got.URL.Path != "/things" {
		t.Errorf("request = %s %s, want POST /things", got.Method, got.URL.Path)
	}
	if body != `{"name":"x"}` {
		t.Errorf("body = %q", body)
	}
	if got.Header.Get("Content-Type") != "application/json" {
		t.Error("described headers should be sent")
	}
	if got.Header.Get("X-Extra") != "added" {
		t.Error("--header values should be sent")
	}
}

// TestUnreachableServerIsAnErrorNotAFailure pins the distinction: the API was
// never asked, so nothing was learned about it.
func TestUnreachableServerIsAnErrorNotAFailure(t *testing.T) {
	results, err := New("http://127.0.0.1:1").Run(context.Background(),
		[]compile.Transaction{transaction("GET", "/x", "200", "", "")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if results[0].Status != StatusError {
		t.Errorf("status = %q, want error", results[0].Status)
	}
}

// TestRedirectsAreNotFollowed pins that a described redirect is tested as
// itself rather than silently followed to its destination.
func TestRedirectsAreNotFollowed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusMovedPermanently)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// A redirect does not promise JSON, so the transaction does not declare a
	// content type — otherwise the content-type check would rightly object to
	// the text/html the redirect actually carries.
	source := transaction("GET", "/old", "301", "", "")
	source.Response.Headers = nil

	results, _ := New(server.URL).Run(context.Background(), []compile.Transaction{source})

	if results[0].Status != StatusPass {
		t.Errorf("status = %q, want pass: the redirect itself was described (%v)",
			results[0].Status, results[0].Errors)
	}
}

func TestResponseSchemaIsEnforced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":123}`)
	}))
	defer server.Close()

	schema := `{"type":"object","required":["id"],"properties":{"id":{"type":"string"}}}`
	results, _ := New(server.URL).Run(context.Background(),
		[]compile.Transaction{transaction("GET", "/x", "200", "", schema)})

	if results[0].Status != StatusFail {
		t.Fatalf("status = %q, want fail", results[0].Status)
	}
	if !strings.Contains(results[0].Errors[0], "got number, want string") {
		t.Errorf("errors = %v", results[0].Errors)
	}
}

// stubHooks records what it was asked and applies a canned edit.
type stubHooks struct {
	events   []string
	beforeFn func(*Transaction)
	afterFn  func(*Transaction)
}

func (h *stubHooks) BeforeAll(t []*Transaction) error {
	h.events = append(h.events, "beforeAll")
	return nil
}
func (h *stubHooks) AfterAll(t []*Transaction) error {
	h.events = append(h.events, "afterAll")
	return nil
}

func (h *stubHooks) BeforeEach(t *Transaction) error {
	h.events = append(h.events, "beforeEach")
	if h.beforeFn != nil {
		h.beforeFn(t)
	}
	return nil
}

func (h *stubHooks) BeforeEachValidation(t *Transaction) error {
	h.events = append(h.events, "beforeEachValidation")
	return nil
}

func (h *stubHooks) AfterEach(t *Transaction) error {
	h.events = append(h.events, "afterEach")
	if h.afterFn != nil {
		h.afterFn(t)
	}
	return nil
}

func TestHookOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	stub := &stubHooks{}
	engine := New(server.URL)
	engine.Hooks = stub

	source := transaction("GET", "/x", "200", "", "")
	source.Response.Headers = nil
	if _, err := engine.Run(context.Background(), []compile.Transaction{source}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{"beforeAll", "beforeEach", "beforeEachValidation", "afterEach", "afterAll"}
	if len(stub.events) != len(want) {
		t.Fatalf("events = %v, want %v", stub.events, want)
	}
	for i := range want {
		if stub.events[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, stub.events[i], want[i])
		}
	}
}

// TestHookSkipStopsTheRequest pins that a skipped transaction never reaches the
// server — a hook skipping an endpoint must not still call it.
func TestHookSkipStopsTheRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	engine := New(server.URL)
	engine.Hooks = &stubHooks{beforeFn: func(t *Transaction) { t.Skip = true }}

	results, _ := engine.Run(context.Background(),
		[]compile.Transaction{transaction("GET", "/x", "200", "", "")})

	if results[0].Status != StatusSkip {
		t.Errorf("status = %q, want skip", results[0].Status)
	}
	if called {
		t.Error("a skipped transaction must not reach the server")
	}
}

func TestHookCanFailAPassingTransaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	engine := New(server.URL)
	engine.Hooks = &stubHooks{afterFn: func(t *Transaction) { t.Fail = "the hook was unhappy" }}

	source := transaction("GET", "/x", "200", "", "")
	source.Response.Headers = nil
	results, _ := engine.Run(context.Background(), []compile.Transaction{source})

	if results[0].Status != StatusFail {
		t.Fatalf("status = %q, want fail", results[0].Status)
	}
	if results[0].Errors[0] != "the hook was unhappy" {
		t.Errorf("errors = %v", results[0].Errors)
	}
}

func TestHookCanRewriteTheRequest(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	engine := New(server.URL)
	engine.Hooks = &stubHooks{beforeFn: func(t *Transaction) {
		t.Request.Headers["Authorization"] = "Bearer token"
	}}

	source := transaction("GET", "/x", "200", "", "")
	source.Response.Headers = nil
	if _, err := engine.Run(context.Background(), []compile.Transaction{source}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if seen != "Bearer token" {
		t.Errorf("Authorization = %q, want the hook's value", seen)
	}
}

// TestEditingRequestURIDoesNotRedirect pins Dredd's behaviour: the address is
// resolved before hooks run, so a hook writing to Request.URI changes what a
// later hook reads there and nothing else. Following the edit would make the
// same hook file send different requests under each tool.
func TestEditingRequestURIDoesNotRedirect(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	engine := New(server.URL)
	engine.Hooks = &stubHooks{beforeFn: func(t *Transaction) {
		t.Request.URI = "/rewritten"
	}}

	source := transaction("GET", "/original", "200", "", "")
	source.Response.Headers = nil
	results, err := engine.Run(context.Background(), []compile.Transaction{source})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if seen != "/original" {
		t.Errorf("server saw %q, want /original: editing Request.URI must not redirect", seen)
	}
	// The report shows where the request went, not what a hook wrote.
	if results[0].Request.URI != "/original" {
		t.Errorf("reported URI = %q, want the address actually used", results[0].Request.URI)
	}
}

// TestFullPathRedirects pins the field that DOES move a request.
func TestFullPathRedirects(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	engine := New(server.URL)
	engine.Hooks = &stubHooks{beforeFn: func(t *Transaction) {
		t.FullPath = "/redirected"
		t.SetFullURL(t.Endpoint() + "/redirected")
	}}

	source := transaction("GET", "/original", "200", "", "")
	source.Response.Headers = nil
	results, _ := engine.Run(context.Background(), []compile.Transaction{source})

	if seen != "/redirected" {
		t.Errorf("server saw %q, want /redirected", seen)
	}
	if results[0].Request.URI != "/redirected" {
		t.Errorf("reported URI = %q, want /redirected", results[0].Request.URI)
	}
}

// TestChecksBeyondDredd pins the failures vertrag raises and Dredd does not.
func TestChecksBeyondDredd(t *testing.T) {
	for _, test := range []struct {
		name        string
		handler     http.HandlerFunc
		wantFinding string
	}{
		{
			name: "a server error is named as one",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantFinding: "failed rather than disagreed",
		},
		{
			// Dredd checks that expected headers are present and never compares
			// their values, so this passes there.
			name: "a wrong content type is caught",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
			},
			wantFinding: "the response is text/html, but the description promises application/json",
		},
		{
			name: "a missing content type is caught",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header()["Content-Type"] = nil
				w.WriteHeader(http.StatusOK)
			},
			wantFinding: "carries no Content-Type",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()

			results, _ := New(server.URL).Run(context.Background(),
				[]compile.Transaction{transaction("GET", "/x", "200", "", "")})

			if results[0].Status != StatusFail {
				t.Fatalf("status = %q, want fail", results[0].Status)
			}
			joined := strings.Join(results[0].Beyond, " | ")
			if !strings.Contains(joined, test.wantFinding) {
				t.Errorf("beyond-Dredd findings = %v, want one mentioning %q",
					results[0].Beyond, test.wantFinding)
			}
		})
	}
}

// TestMatchingContentTypeIgnoresParameters pins that a charset the document did
// not mention is not a contract violation.
func TestMatchingContentTypeIgnoresParameters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	results, _ := New(server.URL).Run(context.Background(),
		[]compile.Transaction{transaction("GET", "/x", "200", "", "")})

	if len(results[0].Beyond) != 0 {
		t.Errorf("findings = %v, want none: a charset is not a violation", results[0].Beyond)
	}
}

func TestFullURLOverride(t *testing.T) {
	source := transaction("GET", "/original", "200", "", "")
	prepared := newTransaction(source, "http://example.com", nil)

	if got := prepared.FullURL(); got != "http://example.com/original" {
		t.Errorf("FullURL = %q", got)
	}
	prepared.SetFullURL("http://example.com/rewritten")
	if got := prepared.FullURL(); got != "http://example.com/rewritten" {
		t.Errorf("FullURL after override = %q", got)
	}
	if prepared.Endpoint() != "http://example.com" {
		t.Errorf("Endpoint = %q", prepared.Endpoint())
	}
}

// typed builds a transaction expecting a response of a given media type, which
// the JSON-shaped helper above cannot express.
func typed(uri, status, mediaType, body string) compile.Transaction {
	source := transaction("GET", uri, status, body, "")
	source.Response.Headers = []compile.Header{{Name: "Content-Type", Value: mediaType}}
	return source
}

// streamServer answers /endless with a stream that never finishes, /prompt with
// one that finishes at once, and /flood with one that arrives faster than the
// time bound. Each handler stops when the client goes away, so the test server
// can close.
func streamServer(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		switch r.URL.Path {
		case "/prompt":
			fmt.Fprint(w, "data: only\n\n")
		case "/flood":
			chunk := "data: " + strings.Repeat("x", 8<<10) + "\n\n"
			for r.Context().Err() == nil {
				fmt.Fprint(w, chunk)
				flusher.Flush()
			}
		default:
			for r.Context().Err() == nil {
				fmt.Fprint(w, "data: tick\n\n")
				flusher.Flush()
				time.Sleep(10 * time.Millisecond)
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestAnEndlessStreamIsReadForABoundedTimeRatherThanUntilTheClientTimeout pins
// the fix for a Server-Sent Events endpoint stalling a whole run. Reading such a
// body to EOF cannot finish, so the read used to run into the thirty second
// client timeout and the transaction was then reported as an ERROR — as though
// the server had never been reached, when it had answered instantly and
// correctly. Both halves matter: the run has to stay quick, and the verdict has
// to be about the response rather than about vertrag's own patience.
func TestAnEndlessStreamIsReadForABoundedTimeRatherThanUntilTheClientTimeout(t *testing.T) {
	server := streamServer(t)

	results, err := New(server.URL).Run(context.Background(),
		[]compile.Transaction{typed("/endless", "200", "text/event-stream", "data: tick\n\n")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if results[0].Status == StatusError {
		t.Errorf("a server that answered should not be an error: %v", results[0].Errors)
	}
	// Generous next to the two second bound and still far under the thirty
	// second client timeout this is here to keep the run away from.
	if results[0].Duration > 10*time.Second {
		t.Errorf("the read took %s, so the bound did not apply", results[0].Duration)
	}
	if !strings.Contains(results[0].Actual.Body, "data: tick") {
		t.Errorf("what arrived before the bound should be the body, got %q", results[0].Actual.Body)
	}
}

// TestAStreamThatEndsPromptlyIsNotHeldForTheWholeBudget pins that the bound is a
// ceiling and not a wait. An endpoint that declares itself a stream but answers
// in one event is common — a description's example is one event — and paying two
// seconds for each of those would be a slower run than reading to EOF was.
func TestAStreamThatEndsPromptlyIsNotHeldForTheWholeBudget(t *testing.T) {
	server := streamServer(t)

	results, err := New(server.URL).Run(context.Background(),
		[]compile.Transaction{typed("/prompt", "200", "text/event-stream", "data: only\n\n")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if results[0].Duration >= streamReadBudget {
		t.Errorf("a stream that ended took %s, the whole budget", results[0].Duration)
	}
	if results[0].Status != StatusPass {
		t.Errorf("status = %q, want pass: %v", results[0].Status, results[0].Errors)
	}
}

// TestAFastStreamStopsAtTheByteBound pins the second bound. The time bound alone
// leaves a producer that is quick rather than slow free to hand the process as
// much as it can push in two seconds, which is a memory problem rather than a
// timing one.
func TestAFastStreamStopsAtTheByteBound(t *testing.T) {
	server := streamServer(t)

	results, err := New(server.URL).Run(context.Background(),
		[]compile.Transaction{typed("/flood", "200", "text/event-stream", "data: x\n\n")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := len(results[0].Actual.Body); got != streamReadLimit {
		t.Errorf("body = %d bytes, want the byte bound of %d", got, streamReadLimit)
	}
	if results[0].Duration >= streamReadBudget {
		t.Error("the byte bound should end the read without waiting out the time bound")
	}
}

// TestANonStreamingBodyIsReadWholeHoweverLarge pins that the bounds are scoped
// to media types that stream. Applying them everywhere would silently truncate a
// large but perfectly finite payload and report a mismatch the server had
// nothing to do with.
func TestANonStreamingBodyIsReadWholeHoweverLarge(t *testing.T) {
	payload := strings.Repeat("a", streamReadLimit+4096)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, payload)
	}))
	defer server.Close()

	results, err := New(server.URL).Run(context.Background(),
		[]compile.Transaction{typed("/big", "200", "text/plain", payload)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := len(results[0].Actual.Body); got != len(payload) {
		t.Errorf("body = %d bytes, want the whole %d", got, len(payload))
	}
	if results[0].Status != StatusPass {
		t.Errorf("status = %q, want pass: %v", results[0].Status, results[0].Errors)
	}
}

// TestBodiesThatAreNotJSONAreRunAndCompared pins that CSV, binary and empty
// responses are ordinary transactions. Projects using Dredd routinely excluded
// these endpoints from their runs, so the assumption they cannot be tested
// outlives the tools it came from; this says plainly that vertrag compares them,
// passes the ones that match and fails the ones that do not.
func TestBodiesThatAreNotJSONAreRunAndCompared(t *testing.T) {
	// Two NULs, a lone 0x80 continuation byte and an 0xff, none of which is
	// valid UTF-8 — the bytes that would corrupt a report if anything on the way
	// out mistook the body for text.
	binary := "\x00\x01\xff\xfe\x00ab\x80\x00"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/csv":
			w.Header().Set("Content-Type", "text/csv")
			fmt.Fprint(w, "id,name\r\n1,alice\r\n")
		case "/binary":
			w.Header().Set("Content-Type", "application/octet-stream")
			fmt.Fprint(w, binary)
		case "/empty":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	results, err := New(server.URL).Run(context.Background(), []compile.Transaction{
		typed("/csv", "200", "text/csv", "id,name\r\n1,alice\r\n"),
		typed("/csv", "200", "text/csv", "id,name\r\n9,zed\r\n"),
		typed("/binary", "200", "application/octet-stream", binary),
		typed("/binary", "200", "application/octet-stream", "\x00different\x00"),
		typed("/empty", "204", "application/octet-stream", ""),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	for i, want := range []Status{StatusPass, StatusFail, StatusPass, StatusFail, StatusPass} {
		if results[i].Status != want {
			t.Errorf("result %d status = %q, want %q (errors: %v, beyond: %v)",
				i, results[i].Status, want, results[i].Errors, results[i].Beyond)
		}
	}

	// Byte for byte, because a binary body is only comparable if nothing on the
	// way in has re-encoded it — replacing the invalid bytes with U+FFFD here
	// would make two different downloads compare equal.
	if results[2].Actual.Body != binary {
		t.Errorf("binary body = %q, want it recorded unchanged", results[2].Actual.Body)
	}
}

func TestExpectedSchemaIsCarried(t *testing.T) {
	source := transaction("GET", "/x", "200", "", `{"type":"object"}`)
	prepared := newTransaction(source, "http://example.com", nil)

	if len(prepared.Expected.BodySchema) == 0 {
		t.Fatal("a described schema should reach validation")
	}
	if !json.Valid(prepared.Expected.BodySchema) {
		t.Error("the carried schema should be valid JSON")
	}
}

// TestTheGrantingOperationNeverReceivesTheCredential pins a rule that is
// definitional rather than configurable: the request that OBTAINS the
// credential is not sent it.
//
// Found on a real suite, whose login transaction went out carrying the cookie
// it had just minted. Harmless there — the server ignored it — but the suite
// had to spell the exclusion out in `auth.except`, and at worst a server
// takes the cookie as "already authenticated" and answers a login it never
// performed, which is a different exchange from the one documented.
func TestTheGrantingOperationNeverReceivesTheCredential(t *testing.T) {
	engine := New("http://example.invalid")
	engine.Auth = Credential{
		Header:      "Cookie: session=minted",
		LoginMethod: "POST",
		LoginPath:   "/auth/login",
	}

	login := compile.Transaction{
		Name:    "login",
		Request: compile.Request{Method: "POST", URI: "/auth/login"},
	}
	if headers := engine.headersFor(login); len(headers) != 0 {
		t.Errorf("the login request was sent the credential it granted: %v", headers)
	}

	// Everything else still gets it, including a different method on the same
	// path — a GET /auth/login is not the operation that granted anything.
	for _, other := range []compile.Transaction{
		{Name: "list", Request: compile.Request{Method: "GET", URI: "/things"}},
		{Name: "peek", Request: compile.Request{Method: "GET", URI: "/auth/login"}},
	} {
		headers := engine.headersFor(other)
		if len(headers) != 1 || headers[0] != "Cookie: session=minted" {
			t.Errorf("%s should carry the credential, got %v", other.Name, headers)
		}
	}

	// A query string on the compiled URI does not hide the login path.
	withQuery := compile.Transaction{
		Name:    "login with query",
		Request: compile.Request{Method: "POST", URI: "/auth/login?next=%2Fhome"},
	}
	if headers := engine.headersFor(withQuery); len(headers) != 0 {
		t.Errorf("a query string let the credential reach the login request: %v", headers)
	}
}

// TestIgnoredAuthFindsAnEndpointThatIsNotActuallyAuthenticated pins the check
// that no ordinary run can make.
//
// Every request in a run carries the credential, so every response is
// correct, and an endpoint that would have answered just as happily without
// one is indistinguishable from a properly guarded one. The only way to tell
// is to ask again without it.
func TestIgnoredAuthFindsAnEndpointThatIsNotActuallyAuthenticated(t *testing.T) {
	// /guarded checks the credential; /open does not, though the suite sends
	// one to both.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/guarded" && r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	engine := New(server.URL)
	engine.Auth = Credential{Header: "Authorization: Bearer good"}
	engine.Checks = Checks{IgnoredAuth: true}

	transactions := []compile.Transaction{
		{
			Name:     "guarded",
			Request:  compile.Request{Method: "GET", URI: "/guarded"},
			Response: compile.Response{Status: "200"},
		},
		{
			Name:     "open",
			Request:  compile.Request{Method: "GET", URI: "/open"},
			Response: compile.Response{Status: "200"},
		},
	}

	results, err := engine.Run(context.Background(), transactions)
	if err != nil {
		t.Fatal(err)
	}

	if results[0].Status != StatusPass {
		t.Errorf("the guarded endpoint should pass: %v %v", results[0].Status, results[0].Beyond)
	}
	if results[1].Status != StatusFail {
		t.Fatalf("the unauthenticated endpoint should be reported, got %s", results[1].Status)
	}
	if len(results[1].Beyond) != 1 || !strings.Contains(results[1].Beyond[0], "not authenticated") {
		t.Errorf("the finding does not say what is wrong: %v", results[1].Beyond)
	}
	// It is a Beyond finding: a check Dredd never made, kept apart so an
	// upgrade is not mistaken for a regression.
	if len(results[1].Errors) != 0 {
		t.Errorf("an ignored-auth finding is not a validation error: %v", results[1].Errors)
	}
}

// TestIgnoredAuthIsSilentWithoutACredential: with nothing to withhold there
// is no question to ask, and asking anyway would report every endpoint of an
// unauthenticated suite.
func TestIgnoredAuthIsSilentWithoutACredential(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	engine := New(server.URL)
	engine.Checks = Checks{IgnoredAuth: true}
	results, _ := engine.Run(context.Background(), []compile.Transaction{{
		Name: "open", Request: compile.Request{Method: "GET", URI: "/open"},
		Response: compile.Response{Status: "200"},
	}})

	if results[0].Status != StatusPass || len(results[0].Beyond) != 0 {
		t.Errorf("an unauthenticated run should raise nothing: %v %v", results[0].Status, results[0].Beyond)
	}
	if requests != 1 {
		t.Errorf("the server saw %d requests; with no credential there is nothing to re-send", requests)
	}
}
