package runner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
)

// TestNothingSkippedLeavesTheRunnerWhoeverAsks pins a boundary rather than a
// behaviour, and it exists because the behaviour was already correct and the
// boundary was not.
//
// `skip` was enforced by every caller filtering its own list before sending.
// That is how it was lost: folding the probing commands into phases left one of
// those filters behind, and for two releases an operation whose configuration
// said "must never be sent" received generated requests. The project it
// happened to skips an operation that makes an outbound call to a host the
// caller names, so their own server dialled addresses vertrag had invented.
//
// A skip list is the one part of a configuration that is a safety boundary
// rather than a preference — "never send this" is different in kind from
// "expect 404 here" — and a boundary each caller must remember to check is one
// refactor away from being gone. This test calls Send DIRECTLY, the way a
// caller that forgot to filter would, and requires that nothing reaches the
// server.
func TestNothingSkippedLeavesTheRunnerWhoeverAsks(t *testing.T) {
	var reached atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	engine := New(server.URL)
	engine.Skip = map[string]string{"forbidden": "must never be sent"}

	forbidden := compile.Transaction{
		Name:    "forbidden",
		Request: compile.Request{Method: "POST", URI: "/danger"},
	}

	// Every door into the wire, including the ones a probing phase uses.
	if _, err := engine.Send(context.Background(), forbidden); !errors.Is(err, ErrSkippedByConfig) {
		t.Errorf("Send let a skipped transaction through: %v", err)
	}
	if _, err := engine.SendGenerated(context.Background(), forbidden); !errors.Is(err, ErrSkippedByConfig) {
		t.Errorf("SendGenerated let a skipped transaction through: %v", err)
	}
	if _, err := engine.Deliver(context.Background(), engine.Prepare(forbidden)); !errors.Is(err, ErrSkippedByConfig) {
		t.Errorf("Deliver let a skipped transaction through: %v", err)
	}

	if n := reached.Load(); n != 0 {
		t.Errorf("%d skipped request(s) reached the server", n)
	}

	// And the guard against over-reaching: an unskipped transaction still goes.
	allowed := compile.Transaction{Name: "allowed", Request: compile.Request{Method: "GET", URI: "/fine"}}
	if _, err := engine.Send(context.Background(), allowed); err != nil {
		t.Fatalf("an unskipped transaction was refused: %v", err)
	}
	if n := reached.Load(); n != 1 {
		t.Errorf("the allowed transaction did not reach the server (%d)", n)
	}
}
