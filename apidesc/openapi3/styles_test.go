package openapi3

import (
	"net/url"
	"strings"
	"testing"
)

// TestParameterStylesRenderPerSpecification pins the OpenAPI serialisation
// table for the styles a query parameter can declare. Each row is the
// specification's own example, and each used to be wrong or absent: a list
// example was dropped by the parser and the parameter with it, and every
// absent `explode` was read as false — the wrong default for a query list.
func TestParameterStylesRenderPerSpecification(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.3"
info: {title: T, version: "1"}
paths:
  /search:
    get:
      summary: Search
      parameters:
        - {name: tags, in: query, example: [a, b], schema: {type: array, items: {type: string}}}
        - {name: ids, in: query, explode: false, example: [1, 2], schema: {type: array, items: {type: integer}}}
        - {name: sp, in: query, style: spaceDelimited, example: [x, y], schema: {type: array, items: {type: string}}}
        - {name: pp, in: query, style: pipeDelimited, example: [x, y], schema: {type: array, items: {type: string}}}
        - {name: filter, in: query, style: deepObject, example: {color: red, size: L}, schema: {type: object}}
      responses: {"200": {description: ok}}
`)

	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1; annotations: %v", len(result.Transactions), result.Annotations)
	}
	for _, annotation := range result.Annotations {
		t.Errorf("a valid document produced %q", annotation.Message)
	}

	uri := result.Transactions[0].Request.URI
	decoded, err := url.PathUnescape(uri)
	if err != nil {
		t.Fatalf("URI %q does not decode: %v", uri, err)
	}
	// Each fragment is the specification's own rendering of the style.
	for _, want := range []string{
		"tags=a&tags=b",                    // form, exploded — the spec default for a query list
		"ids=1,2",                          // form, explode: false
		"sp=x y",                           // spaceDelimited
		"pp=x|y",                           // pipeDelimited
		"filter[color]=red&filter[size]=L", // deepObject
	} {
		if !strings.Contains(decoded, want) {
			t.Errorf("URI lacks %q:\n  %s", want, decoded)
		}
	}
}

// TestPathAndHeaderListsAreSimpleByDefault pins the other half of the
// default table: outside the query, `simple` and not exploded, so a path
// list is comma-joined.
func TestPathAndHeaderListsAreSimpleByDefault(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.3"
info: {title: T, version: "1"}
paths:
  /things/{ids}:
    get:
      summary: Read
      parameters:
        - {name: ids, in: path, required: true, example: [3, 4, 5], schema: {type: array, items: {type: integer}}}
      responses: {"200": {description: ok}}
`)
	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1; annotations: %v", len(result.Transactions), result.Annotations)
	}
	if got := result.Transactions[0].Request.URI; got != "/things/3,4,5" {
		t.Errorf("URI = %q, want /things/3,4,5", got)
	}
}

// TestReadOnlyAndWriteOnlyAreHonoured pins semantics that were reported as
// unsupported, and which cost more than noise.
//
// A readOnly property is one the SERVER sets, so a client must not send it —
// and a server that validates its input strictly answers 400 when one arrives,
// which vertrag would have reported as a failure it caused itself. The mirror
// holds for writeOnly in a response. The specification is also explicit that
// such a property in `required` is required of the other half of the exchange
// only, so requiring it here would reject a perfectly correct message.
func TestReadOnlyAndWriteOnlyAreHonoured(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.3"
info: {title: T, version: "1"}
paths:
  /things:
    post:
      summary: Create
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [id, name, secret]
              properties:
                id: {type: integer, readOnly: true}
                name: {type: string}
                secret: {type: string, writeOnly: true}
      responses:
        "201":
          description: made
          content:
            application/json:
              schema:
                type: object
                required: [id, name, secret]
                properties:
                  id: {type: integer, readOnly: true}
                  name: {type: string}
                  secret: {type: string, writeOnly: true}
`)

	// Neither key is reported any more: they are read, not ignored.
	for _, annotation := range result.Annotations {
		if strings.Contains(annotation.Message, "readOnly") || strings.Contains(annotation.Message, "writeOnly") {
			t.Errorf("a key vertrag now acts on was reported as unsupported: %q", annotation.Message)
		}
	}

	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d", len(result.Transactions))
	}
	transaction := result.Transactions[0]

	// The generated request body leaves out what the server sets, and keeps
	// what the client sends.
	if strings.Contains(transaction.Request.Body, `"id"`) {
		t.Errorf("the request body carries a readOnly property the client must not send: %s", transaction.Request.Body)
	}
	if !strings.Contains(transaction.Request.Body, `"secret"`) {
		t.Errorf("the request body dropped a writeOnly property, which belongs in a request: %s", transaction.Request.Body)
	}
	if !strings.Contains(transaction.Request.Body, `"name"`) {
		t.Errorf("the request body dropped an ordinary property: %s", transaction.Request.Body)
	}

	// The request schema, which generation draws from, agrees — and does not
	// require the property it just left out.
	if strings.Contains(transaction.Request.Schema, `"id"`) {
		t.Errorf("the request schema still describes a readOnly property: %s", transaction.Request.Schema)
	}
	if strings.Contains(transaction.Request.Schema, `"required":["id"`) {
		t.Errorf("the request schema still requires a readOnly property: %s", transaction.Request.Schema)
	}

	// The response schema is the mirror: it keeps id and drops secret, so a
	// response that omits the write-only value is not reported as wrong.
	if !strings.Contains(transaction.Response.Schema, `"id"`) {
		t.Errorf("the response schema dropped a readOnly property, which belongs in a response: %s", transaction.Response.Schema)
	}
	if strings.Contains(transaction.Response.Schema, `"secret"`) {
		t.Errorf("the response schema still describes a writeOnly property: %s", transaction.Response.Schema)
	}
}

// TestAParameterExampleIsFoundWhereverTheDocumentPutsIt pins the three places
// a value may be demonstrated, and the consequence of missing one.
//
// vertrag read only the Parameter Object's `example`. For a required path
// parameter that usually found nothing, so no URI could be built and NO
// TRANSACTION was produced — and an untestable route does not appear in a
// report as a gap, it is simply absent. Coverage reads as complete while part
// of the surface was never called.
//
// The third form is the one that matters in practice: FastAPI's only
// non-deprecated spelling emits the value as JSON Schema 2020-12's `examples`
// array INSIDE the schema, so every document that generator produces depended
// on a keyword vertrag did not read. Note the two `examples` keywords differ
// in shape — a map of Example Objects at the parameter, a plain array in the
// schema — and are told apart by where they sit.
func TestAParameterExampleIsFoundWhereverTheDocumentPutsIt(t *testing.T) {
	for _, test := range []struct {
		name      string
		parameter string
		want      string
	}{
		{
			"the Parameter Object's own example",
			`{name: id, in: path, required: true, example: seven, schema: {type: string}}`,
			"/things/seven",
		},
		{
			"a map of Example Objects",
			`{name: id, in: path, required: true, schema: {type: string},
			  examples: {first: {value: from-examples}, second: {value: ignored}}}`,
			"/things/from-examples",
		},
		{
			// The FastAPI shape, verbatim from a generated 3.1 document.
			"the schema's own examples array",
			`{name: id, in: path, required: true, schema: {type: string, examples: [analyst], title: Name}}`,
			"/things/analyst",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := compileSource(t, `
openapi: "3.1.0"
info: {title: T, version: "1"}
paths:
  /things/{id}:
    get:
      summary: Read
      parameters:
        - `+test.parameter+`
      responses:
        "200": {description: OK}
`)

			if len(result.Transactions) != 1 {
				t.Fatalf("no transaction was built, so the operation is untestable: %v", result.Annotations)
			}
			if got := result.Transactions[0].Request.URI; got != test.want {
				t.Errorf("URI = %q, want %q", got, test.want)
			}
			for _, annotation := range result.Annotations {
				t.Errorf("a demonstrated value still produced %q", annotation.Message)
			}
		})
	}
}

// TestAParameterWithNoExampleAnywhereIsStillReported keeps the other half: a
// document that demonstrates nothing cannot have a URI invented for it, and
// must say so rather than quietly dropping the operation.
func TestAParameterWithNoExampleAnywhereIsStillReported(t *testing.T) {
	result := compileSource(t, `
openapi: "3.1.0"
info: {title: T, version: "1"}
paths:
  /things/{id}:
    get:
      summary: Read
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      responses:
        "200": {description: OK}
`)

	if len(result.Transactions) != 0 {
		t.Errorf("a URI was invented for a parameter nothing demonstrates: %s",
			result.Transactions[0].Request.URI)
	}
	var said bool
	for _, annotation := range result.Annotations {
		if strings.Contains(annotation.Message, "no example or default value") {
			said = true
		}
	}
	if !said {
		t.Errorf("the missing example was not reported: %v", result.Annotations)
	}
}
