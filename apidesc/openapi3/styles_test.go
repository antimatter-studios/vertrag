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
