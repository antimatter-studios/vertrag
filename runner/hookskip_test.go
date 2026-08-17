package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
)

// A transaction a hook removes must still carry its request in the result.
//
// It did not: that result was built from the name and the status alone, so the
// report printed `skip:  /api/v1/thing > …` — two spaces and a gap where every
// pass and fail line carries `GET`. The method is the only thing separating a
// transaction line from an indented detail line, so a suite counting its own
// report reads 98 skips as none. inpace's `bin/test` counts exactly that way,
// and would have reported a total of 74 rather than 172 on the day it swapped.
//
// Found by comparing vertrag's output against Dredd's for the same run, where
// Dredd printed `skip: POST (500) /api/v1/auth/login` and vertrag did not.
func TestHookSkippedResultKeepsItsRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	transaction := func(name, method, path string) compile.Transaction {
		return compile.Transaction{
			Name:    name,
			Request: compile.Request{Method: method, URI: path},
			Response: compile.Response{
				Status:  "200",
				Headers: []compile.Header{{Name: "Content-Type", Value: "application/json"}},
			},
		}
	}

	// One skipped before the request is sent, one skipped after — the two places
	// a hook can take a transaction out, and both used to lose the request.
	engine := New(server.URL)
	engine.Hooks = &stubHooks{beforeFn: func(prepared *Transaction) {
		if prepared.Name == "skipped early" {
			prepared.Skip = true
		}
	}}

	results, err := engine.Run(context.Background(), []compile.Transaction{
		transaction("skipped early", "DELETE", "/gone"),
		transaction("ran", "GET", "/here"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	byName := map[string]Result{}
	for _, result := range results {
		byName[result.Name] = result
	}

	skipped := byName["skipped early"]
	if skipped.Status != StatusSkip {
		t.Fatalf("status = %q, want skip", skipped.Status)
	}
	if skipped.Request.Method != "DELETE" {
		t.Errorf("a hook-skipped result carries method %q, want DELETE", skipped.Request.Method)
	}
	if skipped.Request.URI != "/gone" {
		t.Errorf("a hook-skipped result carries URI %q, want /gone", skipped.Request.URI)
	}

	if got := byName["ran"].Request.Method; got != "GET" {
		t.Errorf("the transaction that ran carries method %q", got)
	}
}
