package openapi3

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/refract"
)

// compileSource parses a document and compiles it, which is the pairing every
// caller uses and the only way to see what the parser actually produced.
func compileSource(t *testing.T, source string) compile.Result {
	t.Helper()
	elements, err := Parse([]byte(source))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return compile.Compile("application/vnd.oai.openapi", elements, "")
}

const minimalDocument = `
openapi: "3.0.0"
info:
  title: Minimal
  version: "1.0.0"
paths:
  /things:
    get:
      summary: List
      responses:
        "200":
          description: OK
`

func TestParseProducesTransactions(t *testing.T) {
	result := compileSource(t, minimalDocument)

	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1", len(result.Transactions))
	}
	got := result.Transactions[0]
	if got.Request.Method != "GET" || got.Request.URI != "/things" {
		t.Errorf("request = %s %s, want GET /things", got.Request.Method, got.Request.URI)
	}
	if got.Name != "Minimal > /things > List > 200" {
		t.Errorf("name = %q", got.Name)
	}
}

// TestCartesianProduct pins that every request content type is paired with
// every response, because each pairing is a separate exchange to test.
func TestCartesianProduct(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /p:
    post:
      summary: S
      requestBody:
        content:
          application/json: {schema: {type: object, properties: {a: {type: string}}}}
          text/plain: {schema: {type: string, example: hi}}
      responses:
        "200":
          description: OK
          content:
            application/json: {schema: {type: object, properties: {b: {type: string}}}}
        "400":
          description: Bad
`)

	// 2 request content types x 2 responses = 4 exchanges.
	if len(result.Transactions) != 4 {
		t.Fatalf("transactions = %d, want 4", len(result.Transactions))
	}
}

// TestAcceptHeaderAdvertisesTheResponse pins that the server is asked for the
// representation the document is being tested against.
func TestAcceptHeaderAdvertisesTheResponse(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /p:
    get:
      summary: S
      responses:
        "200":
          description: OK
          content:
            application/json: {schema: {type: object, properties: {a: {type: string}}}}
`)

	headers := result.Transactions[0].Request.Headers
	if len(headers) != 1 || headers[0].Name != "Accept" || headers[0].Value != "application/json" {
		t.Errorf("request headers = %#v, want a single Accept header", headers)
	}
}

// TestDefaultResponseBecomes200 pins the fallback that gives an OpenAPI
// `default` response a concrete status to assert.
func TestDefaultResponseBecomes200(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /p:
    get:
      summary: S
      responses:
        default:
          description: Anything
`)

	if got := result.Transactions[0].Response.Status; got != "200" {
		t.Errorf("status = %q, want 200", got)
	}
}

// TestBodyGeneration pins the reference's generator, including the cases where
// it produces nothing at all.
func TestBodyGeneration(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema string
		want   string // "" means no body at all
	}{
		{"object of primitives", `{type: object, properties: {s: {type: string}, n: {type: integer}, b: {type: boolean}}}`,
			`{"s":"","n":0,"b":false}`},
		{"nested object", `{type: object, properties: {o: {type: object, properties: {a: {type: string}}}}}`,
			`{"o":{"a":""}}`},
		{"enum uses the first value", `{type: object, properties: {e: {type: string, enum: [x, y]}}}`,
			`{"e":"x"}`},
		{"example wins", `{type: object, properties: {v: {type: string, example: given}}}`,
			`{"v":"given"}`},
		{"nullable yields null", `{type: object, properties: {n: {type: string, nullable: true}}}`,
			`{"n":null}`},

		// Dredd gives an array of a bare primitive no value at all, so an
		// optional property vanished and a top-level one produced no body. That
		// was catastrophic rather than cautious once the property was REQUIRED:
		// the failure propagates upward, so one `tags: [string]` field anywhere
		// in a required chain destroyed the whole body and the request went out
		// empty. An empty array is a valid specimen for any array the document
		// gave no minItems, which is exactly this case.
		{"an optional array of primitives is still demonstrated",
			`{type: object, properties: {a: {type: array, items: {type: string}}}}`,
			`{"a":[]}`},
		{"a top-level array of primitives is an empty array",
			`{type: array, items: {type: string}}`, `[]`},
		{"array of objects yields one specimen", `{type: array, items: {type: object, properties: {a: {type: string}}}}`,
			`[{"a":""}]`},

		// A required property the document cannot demonstrate leaves the whole
		// object with no value: any specimen built without it would be one the
		// document itself calls invalid. The same property, optional, is just
		// left out.
		// A reference to a schema the document never defines is the case that
		// genuinely has no specimen: there is nothing to read, so nothing can
		// be shown. An array of bare primitives used to stand in for this and
		// no longer does, having a specimen now — the empty array.
		//
		// The propagation is the point of both cases: a REQUIRED property with
		// no specimen sinks the whole object, because any body built without it
		// is one the document itself calls invalid. The same property, optional,
		// is simply left out.
		{"required property without a value sinks the object",
			`{type: object, required: [data], properties: {data: {$ref: "#/components/schemas/Loop"}}}`, ""},
		{"optional property without a value is omitted",
			`{type: object, properties: {data: {$ref: "#/components/schemas/Loop"}}}`, `{}`},
		{"a required property that does have a value is kept",
			`{type: object, required: [name], properties: {name: {type: string}}}`, `{"name":""}`},

		// Dredd tests the generated value for JavaScript truthiness before
		// emitting a body, so "" — and with it false, null and 0 — produced no
		// body at all. A documented response of `false` is a perfectly good
		// response, and as a REQUEST body the omission means sending nothing to
		// a server that requires one.
		{"a bare string is demonstrated by the empty string", `{type: string}`, `""`},

		// Dredd treats an array of a bare primitive as having no specimen at
		// all, which was catastrophic rather than cautious: a required property
		// propagates the failure upward, so one `tags: [string]` field anywhere
		// in a required chain destroyed the ENTIRE body and the request went
		// out empty. An empty array is a valid specimen for any array without a
		// minItems, which is exactly this case.
		{"an array of bare strings is demonstrated by an empty array",
			`{type: array, items: {type: string}}`, `[]`},
		{"a required array of bare strings does not take the body with it",
			`{type: object, required: [tags], properties: {tags: {type: array, items: {type: string}}}}`,
			`{"tags":[]}`},
		{"an untyped schema permits everything, so the empty string will do",
			`{properties: {a: {type: string}}}`, `""`},
		{"a boolean is demonstrated by false", `{type: boolean}`, `false`},
		{"a nullable schema is demonstrated by null", `{type: string, nullable: true}`, `null`},
		{"a number whose minimum is zero is demonstrated by zero",
			`{type: integer, minimum: 0}`, `0`},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /p:
    get:
      summary: S
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: `+test.schema+"\n")

			if len(result.Transactions) != 1 {
				t.Fatalf("transactions = %d", len(result.Transactions))
			}
			if got := result.Transactions[0].Response.Body; got != test.want {
				t.Errorf("body = %q, want %q", got, test.want)
			}
		})
	}
}

// TestRecursiveSchemaTerminates pins that a self-referencing schema produces a
// finite body rather than looping.
func TestRecursiveSchemaTerminates(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /p:
    get:
      summary: S
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: {$ref: "#/components/schemas/Node"}
components:
  schemas:
    Node:
      type: object
      properties:
        name: {type: string}
        child: {$ref: "#/components/schemas/Node"}
`)

	if got := result.Transactions[0].Response.Body; got != `{"name":""}` {
		t.Errorf("body = %q, want the cycle to be cut", got)
	}
}

// TestResponseSchemaUsesDefinitions pins that references are gathered rather
// than inlined, which is what keeps a recursive schema finite.
func TestResponseSchemaUsesDefinitions(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /p:
    get:
      summary: S
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: {$ref: "#/components/schemas/Wrapper"}
components:
  schemas:
    Wrapper:
      type: object
      properties:
        inner: {$ref: "#/components/schemas/Inner"}
    Inner:
      type: object
      properties:
        v: {type: string}
`)

	schema := result.Transactions[0].Response.Schema
	if schema == "" {
		t.Fatal("expected a response schema")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if _, ok := decoded["definitions"]; !ok {
		t.Errorf("schema should carry a definitions block, got %s", schema)
	}
	if !strings.Contains(schema, `"$ref":"#/definitions/Inner"`) {
		t.Errorf("references should point into definitions, got %s", schema)
	}
}

// TestSchemaKeyOrderFollowsDocument pins that schemas are compared as strings,
// so key order is part of the output.
func TestSchemaKeyOrderFollowsDocument(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /p:
    get:
      summary: S
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema:
                type: object
                required: [b, a]
                properties:
                  b: {type: string}
                  a: {type: string}
`)

	schema := result.Transactions[0].Response.Schema
	if !strings.HasPrefix(schema, `{"type":"object","required":["b","a"]`) {
		t.Errorf("schema key order should follow the document, got %s", schema)
	}
}

// TestErrorSuppressesEverything pins the reference's short-circuit: a document
// with any error yields errors alone.
func TestErrorSuppressesEverything(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info:
  title: Broken
paths:
  /warns:
    get:
      summary: Has an unsupported key
      tags: [a]
      responses:
        "200":
          description: OK
`)

	if len(result.Transactions) != 0 {
		t.Errorf("transactions = %d, want 0: the document has an error", len(result.Transactions))
	}
	for _, annotation := range result.Annotations {
		if annotation.Type != "error" {
			t.Errorf("warnings should be suppressed alongside transactions, got %q", annotation.Message)
		}
	}
	if len(result.Annotations) != 1 {
		t.Fatalf("annotations = %d, want 1", len(result.Annotations))
	}
	if want := "'Info Object' is missing required property 'version'"; result.Annotations[0].Message != want {
		t.Errorf("annotation = %q, want %q", result.Annotations[0].Message, want)
	}
}

// TestUnsupportedKeysAreCollapsed pins the occurrence counting, misspelling
// included — it reaches users through Dredd's output today.
func TestUnsupportedKeysAreCollapsed(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /a:
    get:
      summary: A
      tags: [x]
      responses:
        "200": {description: OK}
  /b:
    get:
      summary: B
      tags: [y]
      responses:
        "200": {description: OK}
`)

	if len(result.Annotations) != 1 {
		t.Fatalf("annotations = %d, want 1 collapsed warning: %v", len(result.Annotations), result.Annotations)
	}
	want := "'Operation Object' contains unsupported key 'tags' (2 occurances)"
	if result.Annotations[0].Message != want {
		t.Errorf("annotation = %q, want %q", result.Annotations[0].Message, want)
	}
}

// TestAdditionalPropertiesSchemaIsNotWarnedAbout pins the removal of a warning
// that was false: a schema under additionalProperties is converted and
// enforced (support_test.go proves the keyword acts), yet the parser said it
// was "currently unsupported" — telling users their map-value constraints did
// nothing, exactly when they did.
func TestAdditionalPropertiesSchemaIsNotWarnedAbout(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /config:
    get:
      summary: Config
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema:
                type: object
                additionalProperties:
                  type: string
`)

	for _, annotation := range result.Annotations {
		t.Errorf("a supported construct produced %q", annotation.Message)
	}
}

// TestAdditionalPropertiesSchemaIsStillChecked pins the other half of the same
// change: the subschema is walked like any other, so a key vertrag genuinely
// does nothing with is still reported when it hides inside one.
func TestAdditionalPropertiesSchemaIsStillChecked(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /config:
    get:
      summary: Config
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema:
                type: object
                additionalProperties:
                  type: string
                  xml: {name: share}
`)

	if len(result.Annotations) != 1 {
		t.Fatalf("annotations = %d, want the subschema's unsupported key reported: %v",
			len(result.Annotations), result.Annotations)
	}
	if want := "'Schema Object' contains unsupported key 'xml'"; result.Annotations[0].Message != want {
		t.Errorf("annotation = %q, want %q", result.Annotations[0].Message, want)
	}
}

// TestInvalidKeysAreNotCollapsed pins the other half: each invalid key is a
// separate mistake at a separate place.
func TestInvalidKeysAreNotCollapsed(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /a:
    get:
      summary: A
      notAKey: 1
      responses:
        "200": {description: OK}
  /b:
    get:
      summary: B
      notAKey: 1
      responses:
        "200": {description: OK}
`)

	if len(result.Annotations) != 2 {
		t.Fatalf("annotations = %d, want 2 separate warnings", len(result.Annotations))
	}
}

// TestAnnotationSourcePositions pins that diagnostics point at the source,
// including the quotes a quoted key adds to its span.
func TestAnnotationSourcePositions(t *testing.T) {
	result := compileSource(t, `openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /p:
    get:
      summary: S
      responses:
        "2XX":
          description: Any success
`)

	if len(result.Annotations) != 1 {
		t.Fatalf("annotations = %d, want 1", len(result.Annotations))
	}
	got := result.Annotations[0]
	if !strings.Contains(got.Message, "status code ranges are unsupported") {
		t.Fatalf("annotation = %q", got.Message)
	}
	// `"2XX"` starts at line 8 column 9 and is five characters wide with its
	// quotes, so it ends at column 14.
	want := [][]int{{8, 9}, {8, 14}}
	if len(got.Location) != 2 || got.Location[0][0] != want[0][0] || got.Location[0][1] != want[0][1] ||
		got.Location[1][0] != want[1][0] || got.Location[1][1] != want[1][1] {
		t.Errorf("location = %v, want %v (a quoted key spans its quotes)", got.Location, want)
	}
}

// TestParameterValuesComeFromTheParameterObject pins that a schema inside a
// parameter contributes its enum and nothing else.
func TestParameterValuesComeFromTheParameterObject(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /from-parameter/{id}:
    get:
      summary: A
      parameters:
        - {name: id, in: path, required: true, example: used}
      responses:
        "200": {description: OK}
  /from-schema/{id}:
    get:
      summary: B
      parameters:
        - name: id
          in: path
          required: true
          schema: {type: string, example: ignored}
      responses:
        "200": {description: OK}
`)

	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1: only the parameter-level example is used", len(result.Transactions))
	}
	if got := result.Transactions[0].Request.URI; got != "/from-parameter/used" {
		t.Errorf("uri = %q, want /from-parameter/used", got)
	}
}

func TestQueryParametersExpand(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /search:
    get:
      summary: S
      parameters:
        - {name: q, in: query, example: term}
        - {name: absent, in: query}
      responses:
        "200": {description: OK}
`)

	if got := result.Transactions[0].Request.URI; got != "/search?q=term" {
		t.Errorf("uri = %q, want /search?q=term", got)
	}
}

func TestOperationParameterOverridesPathItem(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /p/{id}:
    parameters:
      - {name: id, in: path, required: true, example: from-path}
    get:
      summary: S
      parameters:
        - {name: id, in: path, required: true, example: from-operation}
      responses:
        "200": {description: OK}
`)

	if got := result.Transactions[0].Request.URI; got != "/p/from-operation" {
		t.Errorf("uri = %q, want /p/from-operation", got)
	}
}

func TestPathItemSummaryTitlesTheResource(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /p:
    summary: The Things
    get:
      summary: List
      responses:
        "200": {description: OK}
`)

	if want := "P > The Things > List > 200"; result.Transactions[0].Name != want {
		t.Errorf("name = %q, want %q", result.Transactions[0].Name, want)
	}
}

func TestAllHTTPMethods(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /p:
    get: {summary: A, responses: {"200": {description: OK}}}
    put: {summary: B, responses: {"200": {description: OK}}}
    post: {summary: C, responses: {"200": {description: OK}}}
    delete: {summary: D, responses: {"200": {description: OK}}}
    patch: {summary: E, responses: {"200": {description: OK}}}
    head: {summary: F, responses: {"200": {description: OK}}}
    options: {summary: G, responses: {"200": {description: OK}}}
`)

	if len(result.Transactions) != 7 {
		t.Fatalf("transactions = %d, want 7", len(result.Transactions))
	}
	methods := map[string]bool{}
	for _, transaction := range result.Transactions {
		methods[transaction.Request.Method] = true
	}
	for _, method := range []string{"GET", "PUT", "POST", "DELETE", "PATCH", "HEAD", "OPTIONS"} {
		if !methods[method] {
			t.Errorf("missing method %s", method)
		}
	}
}

// TestCorpusDocumentsParse is a breadth check: every document in the corpus must
// parse without error and produce a self-consistent result.
//
// The oracle checks these against Dredd, but that needs Node. This keeps them
// exercised in a plain `go test`.
func TestCorpusDocumentsParse(t *testing.T) {
	dir := filepath.Join("..", "..", "oracle", "corpus", "openapi3")
	documents, err := filepath.Glob(filepath.Join(dir, "*.yml"))
	if err != nil || len(documents) == 0 {
		t.Fatalf("no corpus documents found in %s: %v", dir, err)
	}

	for _, path := range documents {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".yml"), func(t *testing.T) {
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}

			elements, err := Parse(source)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			result := compile.Compile("application/vnd.oai.openapi", elements, filepath.Base(path))

			for i, transaction := range result.Transactions {
				if transaction.Request.Method == "" {
					t.Errorf("transaction %d has no method", i)
				}
				if transaction.Request.URI == "" {
					t.Errorf("transaction %d has no URI", i)
				}
				if transaction.Name == "" {
					t.Errorf("transaction %d has no name; hooks address transactions by name", i)
				}
				if transaction.Response.Status == "" {
					t.Errorf("transaction %d has no status", i)
				}
				if schema := transaction.Response.Schema; schema != "" {
					if !json.Valid([]byte(schema)) {
						t.Errorf("transaction %d has an invalid response schema", i)
					}
				}
			}
		})
	}
}

// TestOpenAPI31 pins the differences that make 3.1 a different dialect rather
// than a newer version of the same one.
//
// Before this, a 3.1 document parsed silently and wrongly: `type` lists were
// read as no type at all, `const` was ignored, and the emitted schema claimed
// to be draft-04 — under which a 3.1 document's numeric `exclusiveMinimum` and
// its `const` mean something else or nothing.
func TestOpenAPI31(t *testing.T) {
	result := compileSource(t, `
openapi: "3.1.0"
info: {title: Modern, version: "1.0.0"}
paths:
  /p:
    get:
      summary: S
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema:
                type: object
                properties:
                  maybe: {type: [string, "null"]}
                  exact: {const: fixed}
                  bounded: {type: integer, exclusiveMinimum: 0}
`)

	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d", len(result.Transactions))
	}

	// A type list including "null" means the same as 3.0's `nullable`, and a
	// const has exactly one value it could be.
	// `bounded` is exclusiveMinimum: 0 in the 3.1 numeric spelling, so it must
	// be greater than zero and the smallest specimen the schema permits is 1.
	// This asserted 0 while the generator ignored bounds, which is to say it
	// asserted a body the document called invalid.
	if got, want := result.Transactions[0].Response.Body, `{"maybe":null,"exact":"fixed","bounded":1}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}

	// The schema has to declare the dialect it was written in, or a validator
	// reads 2020-12 keywords under draft-04 rules.
	schema := result.Transactions[0].Response.Schema
	if !strings.Contains(schema, "2020-12") {
		t.Errorf("schema should declare 2020-12, got %s", schema)
	}
	// 2020-12 keywords are acted on, so warning that they are unsupported
	// would be wrong.
	for _, annotation := range result.Annotations {
		if strings.Contains(annotation.Message, "exclusiveMinimum") {
			t.Errorf("unexpected warning about a valid 2020-12 keyword: %s", annotation.Message)
		}
	}
}

// TestOpenAPI30KeepsItsOwnRules pins that 3.0 is unaffected: `nullable` still
// means nullable, and its schemas still declare draft-04.
func TestOpenAPI30KeepsItsOwnRules(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: Classic, version: "1.0.0"}
paths:
  /p:
    get:
      summary: S
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema:
                type: object
                properties:
                  maybe: {type: string, nullable: true}
`)
	if got := result.Transactions[0].Response.Body; got != `{"maybe":null}` {
		t.Errorf("body = %s", got)
	}
	if schema := result.Transactions[0].Response.Schema; !strings.Contains(schema, "draft-04") {
		t.Errorf("a 3.0 schema should declare draft-04, got %s", schema)
	}
}

func TestVersionDetection(t *testing.T) {
	for _, test := range []struct {
		version string
		modern  bool
	}{
		{"3.0.0", false}, {"3.0.3", false}, {"3.1.0", true}, {"3.1.1", true}, {"3.2.0", true}, {"", false},
	} {
		if got := isModernVersion(test.version); got != test.modern {
			t.Errorf("isModernVersion(%q) = %v, want %v", test.version, got, test.modern)
		}
	}
}

// TestMultipartRequestBody pins the payload assembled for a file upload.
//
// Dredd sends nothing for multipart/form-data, which is why projects testing
// uploads skip those endpoints — inpace skips ten of them for exactly this
// reason.
func TestMultipartRequestBody(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /upload:
    post:
      summary: Upload
      requestBody:
        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                note: {type: string, example: hello}
                file: {type: string, format: binary}
      responses:
        "200": {description: OK}
`)

	transaction := result.Transactions[0]

	// The boundary has to reach the header, or a server cannot find the parts.
	var contentType string
	for _, header := range transaction.Request.Headers {
		if header.Name == "Content-Type" {
			contentType = header.Value
		}
	}
	if !strings.Contains(contentType, "boundary=") {
		t.Errorf("Content-Type = %q, want a boundary", contentType)
	}

	body := transaction.Request.Body
	for _, want := range []string{
		`Content-Disposition: form-data; name="note"`,
		"hello",
		// A binary field is sent as a file, since that is what an upload
		// handler looks for.
		`name="file"; filename="file"`,
		"--vertrag-boundary--",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}

	// Two runs of the same document must produce identical bytes, so the
	// boundary is fixed rather than random.
	again := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /upload:
    post:
      summary: Upload
      requestBody:
        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                note: {type: string, example: hello}
                file: {type: string, format: binary}
      responses:
        "200": {description: OK}
`)
	if again.Transactions[0].Request.Body != body {
		t.Error("the same document produced two different bodies")
	}
}

func TestParseRejectsMalformedYAML(t *testing.T) {
	if _, err := Parse([]byte("openapi: \"3.0.0\"\n\tbad")); err == nil {
		t.Error("unparseable YAML should be an error")
	}
}

func TestNonObjectDocumentYieldsNothing(t *testing.T) {
	elements, err := Parse([]byte(`just a string`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	result := compile.Compile("application/vnd.oai.openapi", elements, "")
	if len(result.Transactions) != 0 {
		t.Errorf("transactions = %d, want 0", len(result.Transactions))
	}
}

func TestOrderedMap(t *testing.T) {
	m := newOrderedMap()
	m.Set("b", 1)
	m.Set("a", 2)
	m.Set("b", 3) // replacing keeps the original position

	encoded, err := m.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if string(encoded) != `{"b":3,"a":2}` {
		t.Errorf("encoded = %s, want {\"b\":3,\"a\":2}", encoded)
	}

	if !m.Has("a") || m.Len() != 2 {
		t.Errorf("Has/Len wrong: %v %d", m.Has("a"), m.Len())
	}
	m.Delete("b")
	if m.Has("b") || m.Len() != 1 {
		t.Error("Delete should remove the key and its position")
	}
	m.Delete("missing") // must not panic

	var absent *orderedMap
	if encoded, _ := absent.MarshalJSON(); string(encoded) != "null" {
		t.Errorf("a nil ordered map should encode as null, got %s", encoded)
	}
}

func TestMediaTypeClassification(t *testing.T) {
	for _, test := range []struct {
		mediaType string
		isJSON    bool
		isText    bool
	}{
		{"application/json", true, false},
		{"application/json; charset=utf-8", true, false},
		{"APPLICATION/JSON", true, false},
		{"application/vnd.example+json", true, false},
		{"text/plain", false, true},
		{"application/xml", false, false},
		{"application/octet-stream", false, false},
	} {
		if got := isJSONMediaType(test.mediaType); got != test.isJSON {
			t.Errorf("isJSONMediaType(%q) = %v, want %v", test.mediaType, got, test.isJSON)
		}
		if got := isTextMediaType(test.mediaType); got != test.isText {
			t.Errorf("isTextMediaType(%q) = %v, want %v", test.mediaType, got, test.isText)
		}
	}
}

func TestRefractBuildersProduceUsableElements(t *testing.T) {
	// A guard on the assumption the parser makes everywhere: an element built
	// by hand behaves like one loaded from Refract.
	element := refract.Named("resource")
	element.SetAttr("href", refract.String("/x"))
	if element.Attr("href").String() != "/x" {
		t.Error("a built element should read back its attributes")
	}
}

const headerSchemasDocument = `
openapi: "3.0.0"
info: {title: H, version: "1.0.0"}
paths:
  /things:
    get:
      summary: List
      responses:
        "200":
          description: OK
          headers:
            X-Rate-Limit:
              required: true
              schema: {type: integer, minimum: 0}
            X-Style-Matrix:
              style: matrix
              schema: {type: string}
            X-No-Schema:
              description: nothing to check against
            Content-Type:
              schema: {type: string}
`

// TestResponseHeaderSchemasReachTheCompiledTransaction pins the whole carriage
// path for a capability Dredd does not have: a Header Object's schema has to
// survive parsing, the API Elements representation and compilation, or the check
// downstream has nothing to check against.
//
// The exclusions are as load-bearing as the inclusion. A style other than
// `simple` means the value on the wire is not the plain rendering of the schema,
// and a `Content-Type` Header Object is one the specification says to ignore —
// carrying either would produce failures against servers behaving exactly as
// documented.
func TestResponseHeaderSchemasReachTheCompiledTransaction(t *testing.T) {
	result := compileSource(t, headerSchemasDocument)
	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1", len(result.Transactions))
	}

	schemas := result.Transactions[0].Response.HeaderSchemas
	if len(schemas) != 1 {
		t.Fatalf("header schemas = %v, want only X-Rate-Limit", schemas)
	}

	var declared map[string]any
	if err := json.Unmarshal(schemas["X-Rate-Limit"], &declared); err != nil {
		t.Fatalf("the carried schema is not JSON: %v", err)
	}
	if declared["type"] != "integer" || declared["minimum"] != float64(0) {
		t.Errorf("the constraints did not survive: %v", declared)
	}
}

// TestHeaderSchemasStayOutOfTheCompiledJSON pins the oracle's comparison
// surface. Dredd emits no such field, and the oracle compares vertrag's
// marshalled output against the reference byte for byte — so a visible field
// here would break every OpenAPI fixture that documents a response header.
func TestHeaderSchemasStayOutOfTheCompiledJSON(t *testing.T) {
	result := compileSource(t, headerSchemasDocument)

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), "HeaderSchemas") ||
		strings.Contains(string(encoded), "headerSchemas") {
		t.Errorf("the compiled JSON must not mention header schemas: %s", encoded)
	}
}

// TestADocumentWithNoHeaderSchemasCarriesNone keeps the asset from appearing
// where it has nothing to say, so the common description is unchanged.
func TestADocumentWithNoHeaderSchemasCarriesNone(t *testing.T) {
	result := compileSource(t, minimalDocument)
	if schemas := result.Transactions[0].Response.HeaderSchemas; schemas != nil {
		t.Errorf("header schemas = %v, want none", schemas)
	}
}
