package runner

import (
	"context"
	"fmt"
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
	engine.AddObserver(seen.note)

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
	engine.AddObserver(seen.note)
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

// TestEveryResponseReachesTheObserverIncludingGeneratedOnes is why the observer
// sits in send rather than beside the results.
//
// A Result exists for each documented transaction and for each probe's verdict,
// and a probe's verdict is not its responses: generation sends many bodies
// through one operation and keeps only what it concluded. So a caller wanting
// to know something across a whole run — that one status answered with two
// incompatible body shapes, say — cannot learn it from the results, because the
// responses that would show it were never kept. send is the one point every
// request of every phase passes through.
func TestEveryResponseReachesTheObserverIncludingGeneratedOnes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"path":%q}`, r.URL.Path)
	}))
	defer server.Close()

	var seen []string
	engine := New(server.URL)
	engine.AddObserver(func(_ context.Context, source compile.Transaction, reply validate.Message) {
		seen = append(seen, source.Request.Method+" "+reply.StatusCode+" "+reply.Body)
	})

	documented := transaction("GET", "/documented", "200", `{"path":"x"}`, "")
	if _, err := engine.Run(context.Background(), []compile.Transaction{documented}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	generated := transaction("GET", "/generated", "200", "", "")
	if _, err := engine.SendGenerated(context.Background(), generated); err != nil {
		t.Fatalf("SendGenerated: %v", err)
	}

	want := []string{
		`GET 200 {"path":"/documented"}`,
		`GET 200 {"path":"/generated"}`,
	}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Errorf("the observer saw %v, want %v", seen, want)
	}
}

// TestAnObserverIsCalledFromWhicheverWorkerSent says out loud what the field's
// documentation promises, because the alternative is a data race discovered in
// somebody else's CI.
func TestAnObserverIsCalledFromWhicheverWorkerSent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"1"}`)
	}))
	defer server.Close()

	var guard sync.Mutex
	observed := 0

	engine := New(server.URL)
	engine.Workers = 4
	engine.AddObserver(func(context.Context, compile.Transaction, validate.Message) {
		guard.Lock()
		defer guard.Unlock()
		observed++
	})

	var transactions []compile.Transaction
	for i := 0; i < 12; i++ {
		transactions = append(transactions, transaction("GET", fmt.Sprintf("/item/%d", i), "200", `{"id":"x"}`, ""))
	}
	if _, err := engine.Run(context.Background(), transactions); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if observed != len(transactions) {
		t.Errorf("the observer saw %d of %d responses", observed, len(transactions))
	}
}
