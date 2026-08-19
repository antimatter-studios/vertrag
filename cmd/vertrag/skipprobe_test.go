package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// countingServer records how many requests each path received.
type countingServer struct {
	mu   sync.Mutex
	hits map[string]int
}

func (c *countingServer) start(t *testing.T) string {
	t.Helper()
	c.hits = map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.hits[r.URL.Path]++
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func (c *countingServer) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits[path]
}

// TestASkippedOperationIsNotProbed pins a regression that reached a release.
//
// Folding the probing commands into phases left their skip filter behind with
// them: the standalone commands removed skipped transactions before probing,
// and the phase path built its own list from the compiled transactions with no
// such filter. `withoutSkipped` then had no callers at all — which I saw while
// deleting the rest of that scaffolding, and did not ask why.
//
// An operation marked `skip` with the reason "must never be sent" took five
// generated requests. A skip list is where a suite records what it has decided
// not to touch, and some of those decisions are about what an endpoint DOES:
// one project skips an operation that would forward a credential to any host a
// caller names. Generating for that is worse than running it once as documented.
func TestASkippedOperationIsNotProbed(t *testing.T) {
	binary := build(t)
	server := &countingServer{}
	endpoint := server.start(t)

	dir := t.TempDir()
	description := `openapi: 3.0.3
info: {title: Skips, version: "1.0"}
paths:
  /safe:
    post:
      requestBody: {required: true, content: {application/json: {schema: {type: object, required: [n], properties: {n: {type: integer, minimum: 1, maximum: 5}}}}}}
      responses: {"200": {description: ok, content: {application/json: {schema: {type: object}}}}}
  /dangerous:
    post:
      requestBody: {required: true, content: {application/json: {schema: {type: object, required: [url], properties: {url: {type: string}}}}}}
      responses: {"200": {description: ok, content: {application/json: {schema: {type: object}}}}}
`
	if err := os.WriteFile(filepath.Join(dir, "api.yml"), []byte(description), 0o600); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`spec: ./api.yml
endpoint: %s
skip:
  - name: "/dangerous > POST > 200 > application/json"
    reason: "must never be sent"
phases: [examples, coverage]
`, endpoint)
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, code := runIn(t, dir, binary, "run"); code > 2 {
		t.Fatalf("the run did not complete, exit %d", code)
	}

	if hits := server.count("/dangerous"); hits != 0 {
		t.Errorf("a skipped operation took %d generated request(s); `skip` must keep probing away too", hits)
	}
	// And the guard against over-reaching: everything else is still probed, or
	// the fix would be "skip everything".
	if hits := server.count("/safe"); hits == 0 {
		t.Error("nothing was probed at all, so the skip test proves nothing")
	}
}
