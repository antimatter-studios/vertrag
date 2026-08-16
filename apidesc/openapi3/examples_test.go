package openapi3

import (
	"fmt"
	"strings"
	"testing"
)

// transactionsOf renders each compiled transaction as a single searchable
// string, so a test can assert which request went with which response.
func transactionsOf(t *testing.T, source string) []string {
	t.Helper()

	var out []string
	for _, transaction := range compileSource(t, source).Transactions {
		out = append(out, fmt.Sprintf("%s %s body=%s status=%s response=%s",
			transaction.Request.Method, transaction.Request.URI, transaction.Request.Body,
			transaction.Response.Status, transaction.Response.Body))
	}
	return out
}

// The case this feature exists for: a description saying that one request is
// accepted and another rejected, so a run tests both paths instead of only the
// happy one.
const namedExamples = `
openapi: 3.0.0
info: {title: Login, version: "1.0"}
paths:
  /login:
    post:
      requestBody:
        content:
          application/json:
            schema: {type: object, properties: {username: {type: string}}}
            examples:
              accepted:
                value: {username: gooduser}
              rejected:
                value: {username: baduser}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {type: object, properties: {token: {type: string}}}
              examples:
                accepted:
                  value: {token: abc}
        "401":
          description: denied
          content:
            application/json:
              schema: {type: object, properties: {error: {type: string}}}
              examples:
                rejected:
                  value: {error: unknown user}
`

func TestNamedExamplesPairRequestsWithTheirOwnResponses(t *testing.T) {
	transactions := transactionsOf(t, namedExamples)

	// Two exchanges, not the four a plain product would give. The two a product
	// would add are "gooduser is denied" and "baduser succeeds", neither of
	// which the document claims and both of which would fail against any
	// correct server.
	if len(transactions) != 2 {
		t.Fatalf("got %d transactions, want 2:\n%s", len(transactions), strings.Join(transactions, "\n---\n"))
	}

	for _, want := range []struct{ request, status string }{
		{"gooduser", "200"},
		{"baduser", "401"},
	} {
		found := false
		for _, transaction := range transactions {
			if strings.Contains(transaction, want.request) && strings.Contains(transaction, "status="+want.status) {
				found = true
			}
		}
		if !found {
			t.Errorf("no exchange sending %s and expecting %s:\n%s",
				want.request, want.status, strings.Join(transactions, "\n---\n"))
		}
	}
}

// TestUnnamedResponsesStillPairWithEveryRequest pins that naming only one side
// constrains nothing — the case of a document that illustrates its requests but
// describes its responses by schema alone.
func TestUnnamedResponsesStillPairWithEveryRequest(t *testing.T) {
	transactions := transactionsOf(t, `
openapi: 3.0.0
info: {title: T, version: "1.0"}
paths:
  /t:
    post:
      requestBody:
        content:
          application/json:
            examples:
              one: {value: {a: 1}}
              two: {value: {a: 2}}
      responses:
        "200": {description: ok}
`)
	if len(transactions) != 2 {
		t.Fatalf("got %d transactions, want 2", len(transactions))
	}
}

// TestASingleExampleStillProducesOneExchange pins that the common case is
// unchanged.
func TestASingleExampleStillProducesOneExchange(t *testing.T) {
	transactions := transactionsOf(t, `
openapi: 3.0.0
info: {title: T, version: "1.0"}
paths:
  /t:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              example: {a: 1}
`)
	if len(transactions) != 1 {
		t.Fatalf("got %d transactions, want 1", len(transactions))
	}
}
