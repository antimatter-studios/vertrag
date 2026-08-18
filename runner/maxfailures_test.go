package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
)

func failingSuite(n int) []compile.Transaction {
	out := make([]compile.Transaction, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, compile.Transaction{
			Name:     "t" + string(rune('a'+i)),
			Request:  compile.Request{Method: "GET", URI: "/"},
			Response: compile.Response{Status: "200"},
		})
	}
	return out
}

// TestMaxFailuresStopsSendingButKeepsReporting: past the budget nothing more
// is sent, yet every transaction still appears — as skipped, with the reason —
// so a report that stopped early cannot be mistaken for a shorter suite that
// passed.
func TestMaxFailuresStopsSendingButKeepsReporting(t *testing.T) {
	var sent int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&sent, 1)
		w.WriteHeader(500) // every one fails
	}))
	defer server.Close()

	engine := New(server.URL)
	engine.MaxFailures = 2
	results, err := engine.Run(context.Background(), failingSuite(5))
	if err != nil {
		t.Fatal(err)
	}

	if got := atomic.LoadInt32(&sent); got != 2 {
		t.Errorf("server saw %d requests, want exactly 2 — the budget", got)
	}
	if len(results) != 5 {
		t.Fatalf("results = %d, want all 5 reported", len(results))
	}
	for i, r := range results[:2] {
		if r.Status != StatusFail {
			t.Errorf("results[%d].Status = %s, want fail", i, r.Status)
		}
	}
	for i, r := range results[2:] {
		if r.Status != StatusSkip {
			t.Errorf("results[%d].Status = %s, want skip", i+2, r.Status)
		}
		if !strings.Contains(strings.Join(r.Errors, " "), "stopped after 2 failure(s)") {
			t.Errorf("results[%d] does not say why it was not run: %v", i+2, r.Errors)
		}
	}
}

// TestMaxFailuresZeroMeansNever pins the default: without the flag every
// transaction runs, as every run before the flag existed.
func TestMaxFailuresZeroMeansNever(t *testing.T) {
	var sent int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&sent, 1)
		w.WriteHeader(500)
	}))
	defer server.Close()

	engine := New(server.URL)
	results, _ := engine.Run(context.Background(), failingSuite(4))
	if got := atomic.LoadInt32(&sent); got != 4 {
		t.Errorf("server saw %d requests, want all 4", got)
	}
	for i, r := range results {
		if r.Status != StatusFail {
			t.Errorf("results[%d].Status = %s, want fail", i, r.Status)
		}
	}
}

// TestMaxFailuresCountsErrorsToo: an unreachable server is as much a reason
// to stop as a wrong answer — arguably more.
func TestMaxFailuresCountsErrorsToo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close() // now everything errors

	engine := New(url)
	engine.MaxFailures = 1
	results, _ := engine.Run(context.Background(), failingSuite(3))
	if results[0].Status != StatusError {
		t.Fatalf("results[0].Status = %s, want error", results[0].Status)
	}
	for i, r := range results[1:] {
		if r.Status != StatusSkip {
			t.Errorf("results[%d].Status = %s, want skip after the error", i+1, r.Status)
		}
	}
}
