package apidesc

import (
	"testing"
)

// A GraphQL schema has no version line to match on, so detection rests on the
// one thing every schema must have: a query root, either declared in a
// `schema { … }` block or named by the convention the specification sets out.
func TestAGraphQLSchemaIsDetected(t *testing.T) {
	for _, test := range []struct {
		name       string
		source     string
		mediaType  string
		recognised bool
	}{
		{"SDL with a Query type", "type Query {\n  viewer: User!\n}\n", MediaTypeGraphQL, true},
		{"SDL with a schema block", "schema {\n  query: RootQuery\n}\n", MediaTypeGraphQL, true},
		{"SDL that leads with an enum", "enum Status {\n  ACTIVE\n}\n", MediaTypeGraphQL, true},
		{"an introspection result", `{"data":{"__schema":{"types":[]}}}`, MediaTypeGraphQL, true},

		// The patterns here are the loosest vertrag has, so the formats that
		// identify themselves must keep winning. An OpenAPI document is full
		// of the word `schema`, and one that was read as GraphQL would be
		// reported as unparseable rather than tested.
		{"OpenAPI keeps its own format", "openapi: 3.0.0\ncomponents:\n  schemas:\n    Thing:\n      type: object\n",
			MediaTypeOpenAPI3, true},
		{"an inline JSON schema does not look like a schema block",
			"openapi: 3.0.0\npaths:\n  /a:\n    get:\n      responses:\n        '200':\n          content:\n            application/json:\n              schema: {type: string}\n",
			MediaTypeOpenAPI3, true},
		{"a document that is neither is still unrecognised", "# My API\n\nSome prose about a schema.\n",
			MediaTypeUnknown, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			mediaType, recognised := Detect([]byte(test.source))
			if mediaType != test.mediaType || recognised != test.recognised {
				t.Errorf("Detect = %q, %v; want %q, %v", mediaType, recognised, test.mediaType, test.recognised)
			}
		})
	}
}

// A GraphQL schema is the one format that does not become API Elements: it
// has no resources, URIs or methods to put in them. Parse hands back a schema
// instead, and the caller compiles it on the parallel path.
func TestAGraphQLSchemaIsParsedToASchemaRatherThanToAPIElements(t *testing.T) {
	result, err := Parse([]byte("type Query {\n  version: String!\n}\n"), "schema.graphql")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result.MediaType != MediaTypeGraphQL {
		t.Errorf("mediaType = %q, want %q", result.MediaType, MediaTypeGraphQL)
	}
	if result.Schema == nil {
		t.Fatal("no schema came back, so there is nothing to compile")
	}
	if result.Elements != nil {
		t.Error("a GraphQL schema was forced into API Elements")
	}
	if len(result.Schema.Types["Query"].Fields) != 1 {
		t.Errorf("the schema arrived empty: %+v", result.Schema)
	}
}
