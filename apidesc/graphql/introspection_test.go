package graphql

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// theSameSchemaAsSDL and theSameSchemaIntrospected describe the same API in the
// two forms this package reads, so that the two readers can be held against
// each other rather than each against its own idea of the answer.
const theSameSchemaAsSDL = `
schema { query: Query }

type Query {
  user(id: ID!, tags: [String!] = ["a"]): User
}

interface Node { id: ID! }

type User implements Node {
  id: ID!
  name: String
  old: String @deprecated(reason: "gone")
}

enum Order { ASC DESC }

input Filter { limit: Int = 10 }
`

const theSameSchemaIntrospected = `{
  "data": {
    "__schema": {
      "queryType": {"name": "Query"},
      "mutationType": null,
      "subscriptionType": null,
      "types": [
        {
          "kind": "OBJECT",
          "name": "Query",
          "fields": [
            {
              "name": "user",
              "args": [
                {"name": "id", "type": {"kind": "NON_NULL", "name": null,
                  "ofType": {"kind": "SCALAR", "name": "ID", "ofType": null}},
                 "defaultValue": null},
                {"name": "tags", "type": {"kind": "LIST", "name": null,
                  "ofType": {"kind": "NON_NULL", "name": null,
                    "ofType": {"kind": "SCALAR", "name": "String", "ofType": null}}},
                 "defaultValue": "[\"a\"]"}
              ],
              "type": {"kind": "OBJECT", "name": "User", "ofType": null},
              "isDeprecated": false,
              "deprecationReason": null
            }
          ],
          "interfaces": []
        },
        {
          "kind": "INTERFACE",
          "name": "Node",
          "fields": [
            {"name": "id", "args": [],
             "type": {"kind": "NON_NULL", "name": null,
               "ofType": {"kind": "SCALAR", "name": "ID", "ofType": null}},
             "isDeprecated": false, "deprecationReason": null}
          ],
          "possibleTypes": [{"kind": "OBJECT", "name": "User", "ofType": null}]
        },
        {
          "kind": "OBJECT",
          "name": "User",
          "fields": [
            {"name": "id", "args": [],
             "type": {"kind": "NON_NULL", "name": null,
               "ofType": {"kind": "SCALAR", "name": "ID", "ofType": null}},
             "isDeprecated": false, "deprecationReason": null},
            {"name": "name", "args": [],
             "type": {"kind": "SCALAR", "name": "String", "ofType": null},
             "isDeprecated": false, "deprecationReason": null},
            {"name": "old", "args": [],
             "type": {"kind": "SCALAR", "name": "String", "ofType": null},
             "isDeprecated": true, "deprecationReason": "gone"}
          ],
          "interfaces": [{"kind": "INTERFACE", "name": "Node", "ofType": null}]
        },
        {
          "kind": "ENUM",
          "name": "Order",
          "enumValues": [
            {"name": "ASC", "isDeprecated": false, "deprecationReason": null},
            {"name": "DESC", "isDeprecated": false, "deprecationReason": null}
          ]
        },
        {
          "kind": "INPUT_OBJECT",
          "name": "Filter",
          "inputFields": [
            {"name": "limit", "type": {"kind": "SCALAR", "name": "Int", "ofType": null},
             "defaultValue": "10"}
          ]
        },
        {"kind": "SCALAR", "name": "ID"},
        {"kind": "SCALAR", "name": "String"}
      ],
      "directives": [
        {"name": "include"},
        {"name": "skip"},
        {"name": "deprecated"}
      ]
    }
  }
}`

// render prints a schema's structure, deliberately without descriptions: it is
// used to compare the two input forms, and only the structure is claimed to be
// identical between them.
func render(schema *Schema) string {
	var b strings.Builder

	fmt.Fprintf(&b, "query=%s mutation=%s subscription=%s\n",
		schema.QueryType, schema.MutationType, schema.SubscriptionType)

	for _, name := range sortedTypeNames(schema.Types) {
		named := schema.Types[name]
		fmt.Fprintf(&b, "%s %s oneOf=%v implements=%v possible=%v values=%v deprecatedValues=%v\n",
			named.Kind, named.Name, named.OneOf, named.Interfaces, named.Possible,
			named.EnumValues, named.DeprecatedEnumValues)

		for _, field := range named.Fields {
			fmt.Fprintf(&b, "  %s: %s deprecated=%v/%q default=%#v has=%v\n",
				field.Name, field.Type, field.Deprecated, field.DeprecationReason,
				field.Default, field.HasDefault)
			for _, arg := range field.Args {
				fmt.Fprintf(&b, "    %s: %s default=%#v has=%v\n",
					arg.Name, arg.Type, arg.Default, arg.HasDefault)
			}
		}
	}
	return b.String()
}

// TestBothInputFormsProduceTheSameSchema is the test that keeps the two readers
// honest. They share the types they produce and almost none of the code that
// produces them, and a consumer is entitled to not care which form its schema
// arrived in.
func TestBothInputFormsProduceTheSameSchema(t *testing.T) {
	fromSDL, sdlWarnings := parseSchema(t, theSameSchemaAsSDL)
	fromIntrospection, introspectionWarnings := parseSchema(t, theSameSchemaIntrospected)

	if got, want := render(fromIntrospection), render(fromSDL); got != want {
		t.Errorf("the two forms of the same schema differ.\nfrom introspection:\n%s\nfrom SDL:\n%s", got, want)
	}
	if len(sdlWarnings) != 0 || len(introspectionWarnings) != 0 {
		t.Errorf("warnings on a complete schema: SDL %v, introspection %v",
			sdlWarnings, introspectionWarnings)
	}
}

func TestAnIntrospectionResultIsReadAtTheTopLevelAsWellAsUnderData(t *testing.T) {
	// Both placements are what people actually have: a raw GraphQL response is
	// wrapped in `data`, and every tool that saves one to a file unwraps it.
	unwrapped := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(theSameSchemaIntrospected), `{
  "data": {`), `}
}`)

	schema, _ := parseSchema(t, "{"+unwrapped+"}")
	if schema.QueryType != "Query" {
		t.Fatalf("query root = %q, want Query", schema.QueryType)
	}
	if _, ok := schema.Types["User"]; !ok {
		t.Errorf("the schema has no User type: %v", sortedTypeNames(schema.Types))
	}
}

func TestWrapperKindsBecomeListAndNonNullWrappers(t *testing.T) {
	schema, _ := parseSchema(t, `{"__schema": {"queryType": {"name": "Query"}, "types": [
		{"kind": "OBJECT", "name": "Query", "fields": [
			{"name": "names", "args": [], "type":
				{"kind": "NON_NULL", "ofType":
					{"kind": "LIST", "ofType":
						{"kind": "NON_NULL", "ofType": {"kind": "SCALAR", "name": "String"}}}}}
		]}
	]}}`)

	names := fieldNamed(t, typeNamed(t, schema, "Query"), "names")
	if got := names.Type.String(); got != "[String!]!" {
		t.Errorf("names = %s, want [String!]!", got)
	}

	// NON_NULL wraps the thing it applies to rather than being a level of its
	// own, so the reference is two wrappers deep and not four.
	named, lists, ok := schema.Resolve(names.Type)
	if !ok || lists != 1 || named.Name != "String" {
		t.Errorf("Resolve = %v, %d, %v; want String, 1, true", named, lists, ok)
	}
}

// TestAnIntrospectionDefaultIsReadFromItsGraphQLLiteral pins the detail that
// most easily goes unnoticed: introspection reports a default as SOURCE TEXT in
// a JSON string, not as JSON.
func TestAnIntrospectionDefaultIsReadFromItsGraphQLLiteral(t *testing.T) {
	schema, _ := parseSchema(t, `{"__schema": {"queryType": {"name": "Query"}, "types": [
		{"kind": "OBJECT", "name": "Query", "fields": [
			{"name": "search", "type": {"kind": "SCALAR", "name": "String"}, "args": [
				{"name": "first", "type": {"kind": "SCALAR", "name": "Int"}, "defaultValue": "10"},
				{"name": "order", "type": {"kind": "ENUM", "name": "Order"}, "defaultValue": "DESC"},
				{"name": "tags", "type": {"kind": "SCALAR", "name": "String"}, "defaultValue": "[\"a\", \"b\"]"},
				{"name": "filter", "type": {"kind": "SCALAR", "name": "String"}, "defaultValue": "{limit: 2}"}
			]}
		]}
	]}}`)

	args := map[string]Argument{}
	for _, arg := range fieldNamed(t, typeNamed(t, schema, "Query"), "search").Args {
		args[arg.Name] = arg
	}

	for _, want := range []struct {
		name  string
		value any
	}{
		{"first", int64(10)},
		{"order", EnumValue("DESC")},
		{"tags", []any{"a", "b"}},
		{"filter", map[string]any{"limit": int64(2)}},
	} {
		if !reflect.DeepEqual(args[want.name].Default, want.value) {
			t.Errorf("%s default = %#v (%T), want %#v (%T)", want.name,
				args[want.name].Default, args[want.name].Default, want.value, want.value)
		}
	}
}

func TestADefaultThatCannotBeReadIsDroppedAndNamed(t *testing.T) {
	schema, warnings := parseSchema(t, `{"__schema": {"queryType": {"name": "Query"}, "types": [
		{"kind": "OBJECT", "name": "Query", "fields": [
			{"name": "search", "type": {"kind": "SCALAR", "name": "String"}, "args": [
				{"name": "filter", "type": {"kind": "SCALAR", "name": "String"}, "defaultValue": "{limit:"}
			]}
		]}
	]}}`)

	arg := fieldNamed(t, typeNamed(t, schema, "Query"), "search").Args[0]
	if arg.HasDefault {
		// Kept as the string it was written as, it would be SENT as a string,
		// and the server's rejection would be reported as the failure.
		t.Errorf("an unreadable default was kept as %#v", arg.Default)
	}
	requireWarning(t, warnings, "Query.search(filter:)", "not a value GraphQL can express")
}

func TestDisabledIntrospectionIsReportedAsItself(t *testing.T) {
	_, _, err := Parse([]byte(`{"errors": [{"message": "GraphQL introspection is not allowed"}], "data": null}`))
	if err == nil {
		t.Fatalf("an error response parsed as a schema")
	}
	if !strings.Contains(err.Error(), "introspection is not allowed") {
		t.Errorf("error = %v, want the server's own message in it", err)
	}
}

func TestJSONWithNoSchemaInItIsRejected(t *testing.T) {
	_, _, err := Parse([]byte(`{"openapi": "3.0.0", "paths": {}}`))
	if err == nil {
		t.Fatalf("an OpenAPI document parsed as a GraphQL schema")
	}
	if !strings.Contains(err.Error(), "__schema") {
		t.Errorf("error = %v, want it to say what was looked for", err)
	}
}

func TestATypeWithNoFieldsIsWarnedAboutBecauseNothingCanBeAskedOfIt(t *testing.T) {
	// The usual causes are an introspection query that asked for `fields`
	// without includeDeprecated, and one that was truncated.
	_, warnings := parseSchema(t, `{"__schema": {"queryType": {"name": "Query"}, "types": [
		{"kind": "OBJECT", "name": "Query", "fields": [
			{"name": "thing", "type": {"kind": "OBJECT", "name": "Thing"}, "args": []}
		]},
		{"kind": "OBJECT", "name": "Thing", "fields": []}
	]}}`)

	requireWarning(t, warnings, "object type Thing no fields")
}

func TestAKindGraphQLDoesNotDefineIsWarnedAboutRatherThanGuessedAt(t *testing.T) {
	_, warnings := parseSchema(t, `{"__schema": {"queryType": {"name": "Query"}, "types": [
		{"kind": "OBJECT", "name": "Query", "fields": [
			{"name": "a", "type": {"kind": "SCALAR", "name": "String"}, "args": []}
		]},
		{"kind": "MAGIC", "name": "Wat"},
		{"kind": "NON_NULL", "name": "Wrapper"}
	]}}`)

	requireWarning(t, warnings, "Wat", "MAGIC")
	requireWarning(t, warnings, "Wrapper", "NON_NULL")
}

func TestADirectiveTheServerDefinesIsWarnedAboutButTheBuiltInsAreNot(t *testing.T) {
	_, warnings := parseSchema(t, `{"__schema": {"queryType": {"name": "Query"}, "types": [
		{"kind": "OBJECT", "name": "Query", "fields": [
			{"name": "a", "type": {"kind": "SCALAR", "name": "String"}, "args": []}
		]}
	], "directives": [{"name": "skip"}, {"name": "include"}, {"name": "deprecated"},
		{"name": "specifiedBy"}, {"name": "oneOf"}, {"name": "auth"}]}}`)

	requireWarning(t, warnings, "defines the directive @auth")
	for _, builtIn := range []string{"@skip", "@include", "@deprecated", "@specifiedBy", "@oneOf"} {
		refuseWarning(t, warnings, builtIn)
	}
}
