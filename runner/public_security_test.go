package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
)

// TestIgnoredAuthTakesAPublicOperationAtItsWord is issue #73 end to end, from
// the description rather than a hand-built transaction: a document-wide bearer
// requirement, one operation that overrides it with `security: []`, and one
// that says nothing and so inherits it.
//
// The server answers both without a credential. That is the contract for the
// public one, which must not be re-sent bare and reported as unauthenticated,
// and a finding for the other, which shows the override did not leak into the
// operation that inherits the document's requirement.
func TestIgnoredAuthTakesAPublicOperationAtItsWord(t *testing.T) {
	parsed, err := apidesc.Parse([]byte(`openapi: 3.0.3
info: {title: Public override, version: '1'}
security: [{bearerAuth: []}]
components:
  securitySchemes:
    bearerAuth: {type: http, scheme: bearer}
paths:
  /health:
    get:
      security: []
      responses: {'200': {description: OK}}
  /messages:
    get:
      responses: {'200': {description: OK}}
`), "public.yaml")
	if err != nil {
		t.Fatal(err)
	}
	transactions := compile.Compile(parsed.MediaType, parsed.Elements, "public.yaml").Transactions
	if len(transactions) != 2 {
		t.Fatalf("compiled %d transactions, want 2", len(transactions))
	}

	var mu sync.Mutex
	bare := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			mu.Lock()
			bare[r.URL.Path]++
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	engine := New(server.URL)
	engine.Auth = Credential{Header: "Authorization: Bearer good"}
	engine.Checks.IgnoredAuth = true
	results, err := engine.Run(context.Background(), transactions)
	if err != nil {
		t.Fatal(err)
	}

	byPath := map[string]Result{}
	for i, transaction := range transactions {
		byPath[transaction.Request.URI] = results[i]
	}

	if health := byPath["/health"]; health.Status != StatusPass || len(health.Beyond) != 0 {
		t.Errorf("the public operation was reported: %s %v", health.Status, health.Beyond)
	}
	if bare["/health"] != 0 {
		t.Errorf("the public operation was re-sent without the credential %d time(s)", bare["/health"])
	}

	messages := byPath["/messages"]
	if messages.Status != StatusFail || len(messages.Beyond) != 1 ||
		!strings.Contains(messages.Beyond[0], "not authenticated") {
		t.Errorf("the operation inheriting the bearer requirement was not checked: %s %v",
			messages.Status, messages.Beyond)
	}
	if bare["/messages"] != 1 {
		t.Errorf("the inheriting operation was re-sent without the credential %d time(s), want 1", bare["/messages"])
	}
}
