package graphql

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectRecognisesEveryFormASchemaIsWrittenIn(t *testing.T) {
	for _, source := range []string{
		"type Query { a: String }",
		"interface Node {\n  id: ID!\n}",
		"input Filter { limit: Int }",
		"enum Order { ASC }",
		"union Media = Book | Film",
		"scalar DateTime",
		"schema {\n  query: Query\n}",
		"extend type Query { a: String }",
		"\"\"\"Docs\"\"\"\ntype Query @auth { a: String }",
		`{"data": {"__schema": {"types": []}}}`,
		`{"__schema":{"types":[]}}`,
	} {
		if !Detect([]byte(source)) {
			t.Errorf("Detect said no to a GraphQL schema:\n%s", source)
		}
	}
}

// TestDetectDoesNotClaimTheOtherFormatsVertragReads is the check that matters
// more than recognition does. Detection runs before anything else, so a pattern
// that also matches OpenAPI would send a perfectly good OpenAPI document to a
// parser that cannot read it — and `type:` is on nearly every line of one.
func TestDetectDoesNotClaimTheOtherFormatsVertragReads(t *testing.T) {
	for _, source := range []string{
		"openapi: 3.0.0\ninfo:\n  title: T\ncomponents:\n  schemas:\n    Thing:\n      type: object\n",
		`{"swagger": "2.0", "definitions": {"Thing": {"type": "object"}}}`,
		"FORMAT: 1A\n\n# An API Blueprint\n",
		"",
		"# Just a comment\n",
		// An operation document is GraphQL and is not a schema. Detecting it
		// would promise a schema the parser then rejects.
		"query Everything { user { id } }",
	} {
		if Detect([]byte(source)) {
			t.Errorf("Detect claimed a document that is not a GraphQL schema:\n%s", source)
		}
	}
}

// TestDetectRefusesEveryDescriptionInTheCorpus runs the same check over the
// real OpenAPI documents the rest of the suite is built on, which is where a
// pattern loosened later would actually do damage.
func TestDetectRefusesEveryDescriptionInTheCorpus(t *testing.T) {
	descriptions, err := filepath.Glob(filepath.Join("..", "..", "corpus", "descriptions", "*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(descriptions) == 0 {
		t.Fatalf("no corpus descriptions found; this test is not checking anything")
	}

	for _, path := range descriptions {
		source, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if Detect(source) {
			t.Errorf("Detect claimed %s, which is an OpenAPI description", filepath.Base(path))
		}
	}
}

func TestResolveCountsTheListWrappersItPassesThrough(t *testing.T) {
	schema, _ := parseSchema(t, `
type Query {
  one: User
  many: [User!]!
  grid: [[User]]
  missing: Ghost
}
type User { id: ID! }
`)

	query := typeNamed(t, schema, "Query")
	for _, want := range []struct {
		field string
		lists int
	}{
		{"one", 0},
		{"many", 1},
		{"grid", 2},
	} {
		named, lists, ok := schema.Resolve(fieldNamed(t, query, want.field).Type)
		if !ok || named.Name != "User" || lists != want.lists {
			t.Errorf("Resolve(%s) = %v, %d, %v; want User, %d, true",
				want.field, named, lists, ok, want.lists)
		}
	}
}

func TestResolveReportsATypeTheSchemaDoesNotDefine(t *testing.T) {
	schema, _ := parseSchema(t, `
type Query { missing: [Ghost!]! }
`)

	named, lists, ok := schema.Resolve(fieldNamed(t, typeNamed(t, schema, "Query"), "missing").Type)
	if ok {
		t.Errorf("Resolve found %v for a type the schema never defines", named)
	}
	// The wrapper count is still reported: the caller may want to say what
	// shape the missing thing was in.
	if lists != 1 {
		t.Errorf("lists = %d, want 1", lists)
	}
}

func TestTheBuiltInScalarsArePresentEvenWhenTheSchemaNeverMentionsThem(t *testing.T) {
	// Without them every field of type String would be reported as referring to
	// an undefined type, which is every schema ever written.
	schema, warnings := parseSchema(t, `type Query { a: String }`)

	for _, name := range []string{"Int", "Float", "String", "Boolean", "ID"} {
		named, ok := schema.Types[name]
		if !ok {
			t.Fatalf("the built-in scalar %s is missing", name)
		}
		if named.Kind != KindScalar {
			t.Errorf("%s is %v, want a scalar", name, named.Kind)
		}
	}
	refuseWarning(t, warnings, "String")
}

// TestTheWarningsComeOutTheSameWayEveryTime guards the one thing a map-backed
// schema will get wrong by accident. The warnings are compared in tests and
// read in reports, and an order that changed between runs would make both
// useless — and would make a diff of two runs meaningless.
func TestTheWarningsComeOutTheSameWayEveryTime(t *testing.T) {
	source := `
type Query {
  a: MissingOne @auth
  b: MissingTwo @auth
  c: MissingThree @tag
}
type Subscription { events: MissingFour }
`

	_, first, err := Parse([]byte(source))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for i := 0; i < 20; i++ {
		_, again, err := Parse([]byte(source))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if strings.Join(again, "\n") != strings.Join(first, "\n") {
			t.Fatalf("the warnings differ between runs of the same schema:\n%s\n\nand:\n%s",
				strings.Join(first, "\n"), strings.Join(again, "\n"))
		}
	}
}
