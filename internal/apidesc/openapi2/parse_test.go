package openapi2

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/internal/compile"
)

// compileSource parses a Swagger document and compiles it, which is the only
// way to see what the parser actually produced.
func compileSource(t *testing.T, source string) compile.Result {
	t.Helper()
	elements, err := Parse([]byte(source))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return compile.Compile("application/swagger+json", elements, "")
}

const minimal = `
swagger: '2.0'
info: {title: Minimal, version: '1.0'}
paths:
  /things:
    get:
      summary: List
      responses:
        200: {description: OK}
`

func TestParseProducesTransactions(t *testing.T) {
	result := compileSource(t, minimal)

	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1", len(result.Transactions))
	}
	got := result.Transactions[0]
	if got.Request.Method != "GET" || got.Request.URI != "/things" {
		t.Errorf("request = %s %s", got.Request.Method, got.Request.URI)
	}
	if want := "Minimal > /things > List > 200"; got.Name != want {
		t.Errorf("name = %q, want %q", got.Name, want)
	}
}

// TestBasePathPrefixesTheHref pins that Swagger states paths relative to
// basePath, so the request has to go to the whole thing.
func TestBasePathPrefixesTheHref(t *testing.T) {
	result := compileSource(t, `
swagger: '2.0'
info: {title: P, version: '1.0'}
basePath: /v1
paths:
  /things:
    get: {summary: S, responses: {200: {description: OK}}}
`)
	if got := result.Transactions[0].Request.URI; got != "/v1/things" {
		t.Errorf("uri = %q, want /v1/things", got)
	}
}

// TestConsumesMultipliesProducesDoesNot pins the asymmetry: each consumed type
// is a different request to make, while produced types describe one response.
func TestConsumesMultipliesProducesDoesNot(t *testing.T) {
	result := compileSource(t, `
swagger: '2.0'
info: {title: P, version: '1.0'}
consumes: [application/json, text/plain]
produces: [application/json, application/xml]
paths:
  /p:
    post:
      summary: S
      responses:
        200: {description: OK}
`)

	if len(result.Transactions) != 2 {
		t.Fatalf("transactions = %d, want 2 (one per consumed type)", len(result.Transactions))
	}

	var contentTypes, accepts []string
	for _, transaction := range result.Transactions {
		for _, header := range transaction.Request.Headers {
			switch header.Name {
			case "Content-Type":
				contentTypes = append(contentTypes, header.Value)
			case "Accept":
				accepts = append(accepts, header.Value)
			}
		}
	}
	if strings.Join(contentTypes, ",") != "application/json,text/plain" {
		t.Errorf("content types = %v", contentTypes)
	}
	// Only the first produced type is used.
	if strings.Join(accepts, ",") != "application/json,application/json" {
		t.Errorf("accepts = %v, want the first produced type only", accepts)
	}
}

// TestHeaderOrder pins Content-Type before Accept, which is the reverse of
// OpenAPI 3 and visible in every request.
func TestHeaderOrder(t *testing.T) {
	result := compileSource(t, `
swagger: '2.0'
info: {title: P, version: '1.0'}
consumes: [application/json]
produces: [application/json]
paths:
  /p:
    post: {summary: S, responses: {200: {description: OK}}}
`)
	headers := result.Transactions[0].Request.Headers
	if len(headers) != 2 || headers[0].Name != "Content-Type" || headers[1].Name != "Accept" {
		t.Errorf("headers = %#v, want Content-Type then Accept", headers)
	}
}

// TestExamplesDecideTheExchanges pins that a response with examples describes
// one exchange per demonstrated media type, keyed by the example rather than by
// the first produced type.
func TestExamplesDecideTheExchanges(t *testing.T) {
	result := compileSource(t, `
swagger: '2.0'
info: {title: P, version: '1.0'}
paths:
  /p:
    get:
      summary: S
      produces: [application/json, text/plain]
      responses:
        200:
          description: OK
          examples:
            'application/json': {message: hi}
            'text/plain': hi
`)

	if len(result.Transactions) != 2 {
		t.Fatalf("transactions = %d, want 2", len(result.Transactions))
	}
	if got := result.Transactions[1].Response.Headers[0].Value; got != "text/plain" {
		t.Errorf("second exchange content type = %q, want text/plain", got)
	}
	// A text example is sent as itself, not as a quoted JSON string.
	if got := result.Transactions[1].Response.Body; got != "hi" {
		t.Errorf("text body = %q, want hi", got)
	}
}

// TestOnlyProducedTypeUsedWithoutExamples is the other half of that rule.
func TestOnlyProducedTypeUsedWithoutExamples(t *testing.T) {
	result := compileSource(t, `
swagger: '2.0'
info: {title: P, version: '1.0'}
paths:
  /p:
    get:
      summary: S
      produces: [application/json, text/plain]
      responses:
        200: {description: OK}
`)
	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1", len(result.Transactions))
	}
}

// TestResponseContentTypeHeaderOverrides pins that a declared content-type
// replaces the produced type instead of being sent alongside it.
func TestResponseContentTypeHeaderOverrides(t *testing.T) {
	result := compileSource(t, `
swagger: '2.0'
info: {title: P, version: '1.0'}
paths:
  /p:
    get:
      summary: S
      produces: [application/json]
      responses:
        200:
          description: OK
          headers:
            content-type: {type: string, default: text/plain}
`)

	headers := result.Transactions[0].Response.Headers
	if len(headers) != 1 {
		t.Fatalf("response headers = %#v, want just the overridden Content-Type", headers)
	}
	if headers[0].Value != "text/plain" {
		t.Errorf("content type = %q, want text/plain", headers[0].Value)
	}
	if !strings.HasSuffix(result.Transactions[0].Name, "200 > text/plain") {
		t.Errorf("name = %q, want it to end in the overridden type", result.Transactions[0].Name)
	}
}

// TestBodyGeneration pins Swagger's own generator, which differs from
// OpenAPI 3's in every respect that shows up in a request.
func TestBodyGeneration(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema string
		want   string
	}{
		{"pretty-printed with two spaces",
			`{type: object, properties: {a: {type: string}}}`, "{\n  \"a\": \"\"\n}"},
		{"numbers demonstrate an implausible placeholder",
			`{type: object, properties: {n: {type: integer}}}`, "{\n  \"n\": -100000000\n}"},
		{"booleans are false",
			`{type: object, properties: {b: {type: boolean}}}`, "{\n  \"b\": false\n}"},
		{"an array of primitives still yields a specimen",
			`{type: array, items: {type: string}}`, "[\n  \"\"\n]"},
		{"optional and required properties are both included",
			`{type: object, required: [a], properties: {a: {type: string}, b: {type: string}}}`,
			"{\n  \"a\": \"\",\n  \"b\": \"\"\n}"},
		{"enum uses the first value",
			`{type: object, properties: {e: {type: string, enum: [x, y]}}}`, "{\n  \"e\": \"x\"\n}"},
		{"a bare string sends nothing",
			`{type: string}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := compileSource(t, `
swagger: '2.0'
info: {title: P, version: '1.0'}
produces: [application/json]
paths:
  /p:
    get:
      summary: S
      responses:
        200:
          description: OK
          schema: `+test.schema+"\n")

			if got := result.Transactions[0].Response.Body; got != test.want {
				t.Errorf("body = %q, want %q", got, test.want)
			}
		})
	}
}

// TestReferencedSchemaIsInlined pins that a schema which is only a reference
// stands in for its target: draft-04 makes a validator ignore anything beside a
// $ref, so carrying definitions along would validate nothing.
func TestReferencedSchemaIsInlined(t *testing.T) {
	result := compileSource(t, `
swagger: '2.0'
info: {title: P, version: '1.0'}
produces: [application/json]
paths:
  /p:
    get:
      summary: S
      responses:
        200:
          description: OK
          schema: {$ref: '#/definitions/Item'}
definitions:
  Item:
    type: object
    properties:
      id: {type: string}
`)

	schema := result.Transactions[0].Response.Schema
	if strings.Contains(schema, "$ref") {
		t.Errorf("schema should be inlined, got %s", schema)
	}
	if !strings.Contains(schema, `"id"`) {
		t.Errorf("schema = %s, want the target's properties", schema)
	}
}

func TestBodyParameter(t *testing.T) {
	result := compileSource(t, `
swagger: '2.0'
info: {title: P, version: '1.0'}
consumes: [application/json]
paths:
  /p:
    post:
      summary: S
      parameters:
        - name: payload
          in: body
          schema: {type: object, properties: {a: {type: string}}}
      responses:
        200: {description: OK}
`)
	if got := result.Transactions[0].Request.Body; got != "{\n  \"a\": \"\"\n}" {
		t.Errorf("request body = %q", got)
	}
}

func TestPathAndQueryParameters(t *testing.T) {
	result := compileSource(t, `
swagger: '2.0'
info: {title: P, version: '1.0'}
paths:
  /things/{id}:
    get:
      summary: S
      parameters:
        - {name: id, in: path, required: true, type: string, x-example: ignored, default: abc}
        - {name: q, in: query, type: string, default: term}
      responses:
        200: {description: OK}
`)
	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d", len(result.Transactions))
	}
	if got := result.Transactions[0].Request.URI; got != "/things/abc?q=term" {
		t.Errorf("uri = %q, want /things/abc?q=term", got)
	}
}

func TestHeaderParameters(t *testing.T) {
	result := compileSource(t, `
swagger: '2.0'
info: {title: P, version: '1.0'}
paths:
  /p:
    get:
      summary: S
      parameters:
        - {name: X-Token, in: header, type: string, default: abc}
      responses:
        200: {description: OK}
`)
	headers := result.Transactions[0].Request.Headers
	if len(headers) != 1 || headers[0].Name != "X-Token" || headers[0].Value != "abc" {
		t.Errorf("headers = %#v", headers)
	}
}

func TestAllHTTPMethods(t *testing.T) {
	result := compileSource(t, `
swagger: '2.0'
info: {title: P, version: '1.0'}
paths:
  /p:
    get: {summary: A, responses: {200: {description: OK}}}
    put: {summary: B, responses: {200: {description: OK}}}
    post: {summary: C, responses: {200: {description: OK}}}
    delete: {summary: D, responses: {200: {description: OK}}}
    patch: {summary: E, responses: {200: {description: OK}}}
    head: {summary: F, responses: {200: {description: OK}}}
    options: {summary: G, responses: {200: {description: OK}}}
`)
	if len(result.Transactions) != 7 {
		t.Errorf("transactions = %d, want 7", len(result.Transactions))
	}
}

func TestParseRejectsMalformedYAML(t *testing.T) {
	if _, err := Parse([]byte("swagger: '2.0'\n\tbad")); err == nil {
		t.Error("unparseable YAML should be an error")
	}
}

func TestOrderedMap(t *testing.T) {
	m := newOrderedMap()
	m.Set("b", 1)
	m.Set("a", 2)
	m.Set("b", 3)

	encoded, err := m.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if string(encoded) != `{"b":3,"a":2}` {
		t.Errorf("encoded = %s", encoded)
	}
	m.Delete("b")
	if m.Has("b") || m.Len() != 1 {
		t.Error("Delete should remove the key and its position")
	}
}
