package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/validate"
)

// recorder collects what Observe was handed, safely enough for a run using
// workers.
type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *recorder) note(_ context.Context, source compile.Transaction, reply validate.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, source.Request.URI+" "+reply.StatusCode)
}

func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

// TestEveryDoorIntoTheRunnerIsObserved is the property the hook exists for.
//
// A run reaches the network four ways: Run for the documented examples, Send
// for a probing phase's baseline, SendGenerated for its probes, and Deliver
// for a step of a stateful chain. A collector wired up at some of them reports
// a smaller API than the one that answered, and the responses it never saw
// read as responses the server never gave — which is the exact conclusion the
// thing reading this hook is drawing.
func TestEveryDoorIntoTheRunnerIsObserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()

	var seen recorder
	engine := New(server.URL)
	engine.Observe = seen.note

	transaction := compile.Transaction{
		Name:     "thing",
		Request:  compile.Request{Method: "GET", URI: "/thing"},
		Response: compile.Response{Status: "418"},
	}

	ctx := context.Background()
	if _, err := engine.Run(ctx, []compile.Transaction{transaction}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Send(ctx, transaction); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.SendGenerated(ctx, transaction); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Deliver(ctx, engine.Prepare(transaction)); err != nil {
		t.Fatal(err)
	}

	if got := seen.all(); len(got) != 4 {
		t.Errorf("observed %d of the 4 ways a run reaches the network: %v", len(got), got)
	}
}

// TestARequestStrippedOfItsCredentialIsNotObserved.
//
// The ignored-auth check re-sends a documented request with the credential
// removed on purpose. Its 401 is a fact about that experiment, not about the
// operation, and letting it through would have a run report a 401 the
// description never mentions against an endpoint whose documented request
// never receives one — a finding manufactured by the tester's own probe.
func TestARequestStrippedOfItsCredentialIsNotObserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	var seen recorder
	engine := New(server.URL)
	engine.Observe = seen.note
	engine.Auth = Credential{Header: "Authorization: Bearer good"}
	engine.Checks = Checks{IgnoredAuth: true}

	results, err := engine.Run(context.Background(), []compile.Transaction{{
		Name:     "guarded",
		Request:  compile.Request{Method: "GET", URI: "/guarded"},
		Response: compile.Response{Status: "200"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != StatusPass {
		t.Fatalf("the guarded endpoint should pass: %v %v", results[0].Status, results[0].Beyond)
	}

	got := seen.all()
	if len(got) != 1 || got[0] != "/guarded 200" {
		t.Errorf("the credential probe's own answer reached the ledger: %v", got)
	}
}
