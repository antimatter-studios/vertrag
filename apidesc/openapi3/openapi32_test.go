package openapi3

import (
	"strings"
	"testing"
)

// OpenAPI 3.2 was detected and read from the day it existed, because the
// detection pattern matches any 3.x and the JSON Schema dialect is chosen by
// "3.1 or later". What it was not was VALIDATED: every field 3.2 added was a
// key the tables had never heard of, so a document that was correct in every
// particular came back covered in complaints about it, and one that omitted the
// response `description` 3.2 stopped requiring came back with no transactions at
// all — a missing required property is an error, and an error stops everything.
//
// So these tests are about a valid document being read as valid, which is a
// different property from a document being parsed.

// realistic32Document is a 3.2 description that uses what the revision added,
// in the combinations a document actually uses them in: a self-assigned URI that
// its own references are written through, a QUERY operation and an unregistered
// method beside the ordinary ones, a shared media type, examples in the new
// unambiguous spelling, a streaming response, and a whole-querystring parameter.
const realistic32Document = `
openapi: "3.2.0"
$self: https://api.example.com/openapi.yaml
info:
  title: Inventory
  summary: Stock levels, and the events that change them
  version: "1.4.0"
servers:
  - url: https://api.example.com
    name: production
tags:
  - name: items
    summary: Items
    kind: nav
  - name: bulk
    parent: items
paths:
  /items:
    get:
      operationId: listItems
      tags: [items]
      responses:
        "200":
          summary: A page of items
          description: The items in stock
          content:
            application/json:
              $ref: "#/components/mediaTypes/ItemList"
        "204":
          summary: Nothing in stock
    query:
      operationId: queryItems
      tags: [items]
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                minimumStock: {type: integer, example: 3}
      responses:
        "200":
          description: The items matching the query
          content:
            application/json:
              $ref: "https://api.example.com/openapi.yaml#/components/mediaTypes/ItemList"
    additionalOperations:
      PURGE:
        operationId: purgeItems
        tags: [bulk]
        responses:
          "202":
            description: Accepted
  /items/{sku}:
    parameters:
      - name: sku
        in: path
        required: true
        schema: {type: string}
        examples:
          aKnownItem:
            dataValue: RX-78
    get:
      operationId: getItem
      responses:
        "200":
          description: The item
          content:
            application/json:
              schema: {$ref: "https://api.example.com/openapi.yaml#/components/schemas/Item"}
  /items/events:
    get:
      operationId: itemEvents
      responses:
        "200":
          description: A stream of stock changes
          content:
            text/event-stream:
              itemSchema:
                type: object
                properties:
                  sku: {type: string}
  /search:
    get:
      operationId: search
      parameters:
        - name: criteria
          in: querystring
          content:
            application/x-www-form-urlencoded:
              schema:
                type: object
                properties:
                  q: {type: string}
      responses:
        "200":
          description: Results
components:
  mediaTypes:
    ItemList:
      schema:
        type: array
        items: {$ref: "#/components/schemas/Item"}
  schemas:
    Item:
      type: object
      properties:
        sku: {type: string, example: RX-78}
  securitySchemes:
    tokens:
      type: oauth2
      deprecated: false
      oauth2MetadataUrl: https://api.example.com/.well-known/oauth-authorization-server
      flows:
        clientCredentials:
          tokenUrl: https://api.example.com/token
          scopes: {}
`

// diagnosticsOf renders each annotation as one searchable line.
func diagnosticsOf(t *testing.T, source string) []string {
	t.Helper()

	var out []string
	for _, annotation := range compileSource(t, source).Annotations {
		out = append(out, annotation.Type+": "+annotation.Message)
	}
	return out
}

func assertNoDiagnosticMentions(t *testing.T, diagnostics []string, fragment string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic, fragment) {
			t.Errorf("a valid 3.2 document was told %q", diagnostic)
		}
	}
}

func assertSomeDiagnosticMentions(t *testing.T, diagnostics []string, fragment string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic, fragment) {
			return
		}
	}
	t.Errorf("nothing said about %q; the diagnostics were %v", fragment, diagnostics)
}

func TestNothingIn32IsCalledAnInvalidKey(t *testing.T) {
	diagnostics := diagnosticsOf(t, realistic32Document)

	// An invalid key means "this is not OpenAPI at all", and every field in the
	// document above is.
	assertNoDiagnosticMentions(t, diagnostics, "invalid key")

	// An error stops the whole parse, so one of these leaves a valid document
	// with nothing to run.
	assertNoDiagnosticMentions(t, diagnostics, "error:")
}

func TestA32ResponseNeedsNoDescription(t *testing.T) {
	// The 204 above says only what it is for. 3.2 stopped requiring
	// `description`, and a response that carries nothing has nothing to
	// describe.
	transactions := transactionsOf(t, realistic32Document)
	if len(transactions) == 0 {
		t.Fatalf("a valid 3.2 document produced no transactions at all: %v",
			diagnosticsOf(t, realistic32Document))
	}

	found := false
	for _, transaction := range transactions {
		if strings.Contains(transaction, "status=204") {
			found = true
		}
	}
	if !found {
		t.Errorf("the undescribed 204 produced no transaction: %v", transactions)
	}
}

func TestAResponseStillNeedsADescriptionBefore32(t *testing.T) {
	// The requirement was real until 3.2 removed it, and dropping the check for
	// every revision would stop reporting a 3.0 document that is genuinely
	// incomplete.
	assertSomeDiagnosticMentions(t, diagnosticsOf(t, `
openapi: "3.1.0"
info: {title: T, version: "1.0.0"}
paths:
  /a:
    get:
      responses:
        "204":
          summary: Nothing
`), "'Response Object' is missing required property 'description'")
}

func TestTheQueryMethodIsSentAsQUERY(t *testing.T) {
	transactions := transactionsOf(t, realistic32Document)

	found := false
	for _, transaction := range transactions {
		if strings.HasPrefix(transaction, "QUERY /items ") {
			found = true
			// A QUERY carries a request body, which is the whole reason the
			// method exists, so a description that gives one must produce one.
			if !strings.Contains(transaction, `body={"minimumStock":3}`) {
				t.Errorf("the QUERY operation sent no body: %s", transaction)
			}
		}
	}
	if !found {
		t.Errorf("the `query` operation produced no transaction: %v", transactions)
	}
}

func TestAnAdditionalOperationIsSentUnderTheMethodItNames(t *testing.T) {
	transactions := transactionsOf(t, realistic32Document)

	for _, transaction := range transactions {
		if strings.HasPrefix(transaction, "PURGE /items ") {
			return
		}
	}
	t.Errorf("the PURGE additional operation produced no transaction: %v", transactions)
}

func TestAnAdditionalOperationKeepsTheCapitalisationItWasGiven(t *testing.T) {
	// The specification says the key carries the capitalization to send, and a
	// method is case-sensitive, so a server that answers `Purge` and not
	// `PURGE` must still be describable.
	transactions := transactionsOf(t, `
openapi: "3.2.0"
info: {title: T, version: "1.0.0"}
paths:
  /a:
    additionalOperations:
      Purge:
        responses:
          "202": {description: Accepted}
`)
	for _, transaction := range transactions {
		if strings.HasPrefix(transaction, "Purge /a ") {
			return
		}
	}
	t.Errorf("the method was not sent as written: %v", transactions)
}

func TestQueryIsNotAnOperationBefore32(t *testing.T) {
	// `query` under a 3.0 Path Item is a key the specification has no meaning
	// for. Reading it as an operation would turn a mistake worth reporting into
	// a request nobody asked for.
	source := `
openapi: "3.0.3"
info: {title: T, version: "1.0.0"}
paths:
  /a:
    query:
      responses:
        "200": {description: OK}
`
	assertSomeDiagnosticMentions(t, diagnosticsOf(t, source),
		"'Path Item Object' contains invalid key 'query'")

	for _, transaction := range transactionsOf(t, source) {
		if strings.HasPrefix(transaction, "QUERY ") {
			t.Errorf("a 3.0 document sent a QUERY request: %s", transaction)
		}
	}
}

func TestAnAdditionalOperationForAMethodWithItsOwnFieldIsReported(t *testing.T) {
	// Both are read, so the path ends up with two sets of transactions under one
	// method and the hooks that address them by name cannot tell them apart.
	assertSomeDiagnosticMentions(t, diagnosticsOf(t, `
openapi: "3.2.0"
info: {title: T, version: "1.0.0"}
paths:
  /a:
    post:
      responses:
        "200": {description: OK}
    additionalOperations:
      POST:
        responses:
          "200": {description: OK}
`), "which has a field of its own")
}

func TestAMethodThatCannotBeSentIsReportedRatherThanAttempted(t *testing.T) {
	// Otherwise the refusal arrives from the transport and reads as though the
	// server or the network broke.
	assertSomeDiagnosticMentions(t, diagnosticsOf(t, `
openapi: "3.2.0"
info: {title: T, version: "1.0.0"}
paths:
  /a:
    additionalOperations:
      "NOT A METHOD":
        responses:
          "200": {description: OK}
`), "which is not a method that can be sent")
}

func TestAReferenceThroughTheDocumentsOwnSelfURIIsFollowed(t *testing.T) {
	// This is the failure `$self` support exists to prevent, and it is the quiet
	// kind: the reference is not local by spelling, so it was not followed, and
	// what reached the validator was an empty schema that accepts anything. The
	// run passed having checked nothing.
	result := compileSource(t, realistic32Document)

	found := false
	for _, transaction := range result.Transactions {
		if transaction.Request.Method != "GET" || !strings.HasPrefix(transaction.Request.URI, "/items/RX-78") {
			continue
		}
		found = true
		if !strings.Contains(transaction.Response.Schema, `"sku"`) {
			t.Errorf("the schema named through $self did not arrive: %q", transaction.Response.Schema)
		}
	}
	if !found {
		t.Fatalf("no transaction for the referencing operation: %v", result.Transactions)
	}
}

func TestAReferenceThroughSelfThatNamesNothingIsStillReported(t *testing.T) {
	// The point of following these references is that they are local, and a
	// local reference to nothing is the mistake this parser already refuses to
	// swallow. Reading them without reporting the broken ones would trade one
	// silence for another.
	assertSomeDiagnosticMentions(t, diagnosticsOf(t, `
openapi: "3.2.0"
$self: https://api.example.com/openapi.yaml
info: {title: T, version: "1.0.0"}
paths:
  /a:
    get:
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: {$ref: "https://api.example.com/openapi.yaml#/components/schemas/Absent"}
`), "resolves to nothing")
}

func TestASharedMediaTypeCarriesItsSchemaToEveryOperationThatNamesIt(t *testing.T) {
	// 3.2 made a `content` entry referenceable and gave components a place to
	// keep the shared ones. Accepting the key without following the reference
	// would leave both operations above looking like operations whose author
	// forgot to describe the response.
	result := compileSource(t, realistic32Document)

	named := 0
	for _, transaction := range result.Transactions {
		if transaction.Request.URI != "/items" || transaction.Response.Status != "200" {
			continue
		}
		named++
		if !strings.Contains(transaction.Response.Schema, `"sku"`) {
			t.Errorf("%s /items got no schema from the shared media type: %q",
				transaction.Request.Method, transaction.Response.Schema)
		}
	}
	if named != 2 {
		t.Fatalf("expected the two operations that name the shared media type, got %d", named)
	}
}

func TestAnExampleGivenAsDataValueIsUsed(t *testing.T) {
	// `dataValue` is 3.2's unambiguous spelling of `value`. Reading only the old
	// one leaves a document written the new way sending generated values while
	// its author's examples sit unused — and a generated `sku` is not the one the
	// server has.
	for _, transaction := range transactionsOf(t, realistic32Document) {
		if strings.HasPrefix(transaction, "GET /items/RX-78 ") {
			return
		}
	}
	t.Errorf("the dataValue example did not reach the URI: %v",
		transactionsOf(t, realistic32Document))
}

func TestWhat32AddsAndVertragDoesNotActOnIsSaidRatherThanSilenced(t *testing.T) {
	// The other half of the rule. A key accepted without comment is a claim to
	// be honouring it, and each of these describes something vertrag does not
	// do: validate a stream item by item, parse an already-serialised example
	// back into data, discover an authorisation server, or put a value in the
	// query string as a whole.
	diagnostics := diagnosticsOf(t, realistic32Document)

	for _, fragment := range []string{
		"'Media Type Object' contains unsupported key 'itemSchema'",
		"'Security Scheme Object' contains unsupported key 'oauth2MetadataUrl'",
		"'Security Scheme Object' contains unsupported key 'deprecated'",
		"'Parameter Object' 'in' 'querystring' is unsupported",
	} {
		assertSomeDiagnosticMentions(t, diagnostics, fragment)
	}

	assertSomeDiagnosticMentions(t, diagnosticsOf(t, `
openapi: "3.2.0"
info: {title: T, version: "1.0.0"}
paths:
  /a:
    get:
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: {type: object}
              examples:
                one:
                  serializedValue: '{"a":1}'
`), "'Example Object' contains unsupported key 'serializedValue'")
}
