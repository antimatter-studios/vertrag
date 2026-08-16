package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
