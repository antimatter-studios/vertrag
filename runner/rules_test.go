package runner_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/runner"
)

// recordingServer answers every request and remembers one header per path.
type recordingServer struct {
	*httptest.Server
	mu     sync.Mutex
	seen   map[string]string
	called map[string]bool
}

func newRecordingServer(t *testing.T, header string, status func(path string) int) *recordingServer {
	t.Helper()
	server := &recordingServer{seen: map[string]string{}, called: map[string]bool{}}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.mu.Lock()
		server.seen[r.URL.Path] = r.Header.Get(header)
		server.called[r.URL.Path] = true
		server.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status(r.URL.Path))
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func expecting(name, path, method, status string) compile.Transaction {
	return compile.Transaction{
		Name:    name,
		Request: compile.Request{Method: method, URI: path},
		Response: compile.Response{
			Status:  status,
			Headers: []compile.Header{{Name: "Content-Type", Value: "application/json"}},
		},
	}
}

// A mock told which failure to simulate is how a suite reaches the error
// responses its description promises, and which failure to ask for follows from
// which response is expected. That mapping is the case conditional headers
// exist for.
func TestConditionalHeaderMatchesOnExpectedStatus(t *testing.T) {
	server := newRecordingServer(t, "X-Mock-Scenario", func(string) int { return http.StatusOK })

	engine := runner.New(server.URL)
	engine.ConditionalHeaders = []runner.ConditionalHeader{
		{Name: "X-Mock-Scenario", Value: "absent", Status: "404"},
		{Name: "X-Mock-Scenario", Value: "broken", Status: "500"},
	}

	_, err := engine.Run(context.Background(), []compile.Transaction{
		expecting("ok", "/ok", "GET", "200"),
		expecting("missing", "/missing", "GET", "404"),
		expecting("broken", "/broken", "GET", "500"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	for path, want := range map[string]string{
		"/ok":      "",
		"/missing": "absent",
		"/broken":  "broken",
	} {
		if got := server.seen[path]; got != want {
			t.Errorf("%s received X-Mock-Scenario %q, want %q", path, got, want)
		}
	}
}

func TestConditionalHeaderMatchesOnMethod(t *testing.T) {
	server := newRecordingServer(t, "X-Writes", func(string) int { return http.StatusOK })

	engine := runner.New(server.URL)
	engine.ConditionalHeaders = []runner.ConditionalHeader{
		// Lower case in the config, upper case on the transaction: a config that
		// failed over the case of a method would be a poor use of anyone's time.
		{Name: "X-Writes", Value: "yes", Method: "post"},
	}

	_, err := engine.Run(context.Background(), []compile.Transaction{
		expecting("read", "/read", "GET", "200"),
		expecting("write", "/write", "POST", "200"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if got := server.seen["/read"]; got != "" {
		t.Errorf("GET received X-Writes %q, want none", got)
	}
	if got := server.seen["/write"]; got != "yes" {
		t.Errorf("POST received X-Writes %q, want yes", got)
	}
}

// An unconditional rule and a conditional one for the same header: the
// conditional must win where it matches, or writing both is pointless.
func TestConditionalHeaderOverridesTheRunWideValue(t *testing.T) {
	server := newRecordingServer(t, "X-Mode", func(string) int { return http.StatusOK })

	engine := runner.New(server.URL)
	engine.ExtraHeaders = []string{"X-Mode: default"}
	engine.ConditionalHeaders = []runner.ConditionalHeader{
		{Name: "X-Mode", Value: "special", Status: "404"},
	}

	_, err := engine.Run(context.Background(), []compile.Transaction{
		expecting("plain", "/plain", "GET", "200"),
		expecting("special", "/special", "GET", "404"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if got := server.seen["/plain"]; got != "default" {
		t.Errorf("unmatched transaction got X-Mode %q, want default", got)
	}
	if got := server.seen["/special"]; got != "special" {
		t.Errorf("matched transaction got X-Mode %q, want special", got)
	}
}

func TestSkipRemovesTransactionAndReportsWhy(t *testing.T) {
	server := newRecordingServer(t, "X-Any", func(string) int { return http.StatusOK })

	engine := runner.New(server.URL)
	engine.Skip = map[string]string{
		"skipped":     "the mock always finds the device",
		"unexplained": "",
	}

	results, err := engine.Run(context.Background(), []compile.Transaction{
		expecting("ran", "/ran", "GET", "200"),
		expecting("skipped", "/skipped", "GET", "200"),
		expecting("unexplained", "/unexplained", "GET", "200"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	server.mu.Lock()
	for _, path := range []string{"/skipped", "/unexplained"} {
		if server.called[path] {
			t.Errorf("%s was sent despite being skipped", path)
		}
	}
	if !server.called["/ran"] {
		t.Error("/ran was not sent")
	}
	server.mu.Unlock()

	byName := map[string]runner.Result{}
	for _, result := range results {
		byName[result.Name] = result
	}
	if got := byName["ran"].Status; got != runner.StatusPass {
		t.Errorf("ran status = %q, want pass", got)
	}

	for name, want := range map[string]string{
		"skipped": "skipped by configuration: the mock always finds the device",
		// A reasonless skip still says who skipped it: config and hooks are
		// fixed in different files.
		"unexplained": "skipped by configuration",
	} {
		result := byName[name]
		if result.Status != runner.StatusSkip {
			t.Errorf("%s status = %q, want skip", name, result.Status)
			continue
		}
		if len(result.Errors) != 1 || result.Errors[0] != want {
			t.Errorf("%s reason = %v, want %q", name, result.Errors, want)
		}
	}
}
