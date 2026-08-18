package graphql

import (
	"reflect"
	"strings"
	"testing"
)

func parseSchema(t *testing.T, source string) (*Schema, []string) {
	t.Helper()

	schema, warnings, err := Parse([]byte(source))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return schema, warnings
}

// mentions reports whether any one warning contains every fragment given.
func mentions(warnings []string, fragments ...string) bool {
	for _, warning := range warnings {
		matched := true
		for _, fragment := range fragments {
			if !strings.Contains(warning, fragment) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func requireWarning(t *testing.T, warnings []string, fragments ...string) {
	t.Helper()
	if !mentions(warnings, fragments...) {
		t.Errorf("no warning mentioning %v; the warnings were:\n%s", fragments, strings.Join(warnings, "\n"))
	}
}

func refuseWarning(t *testing.T, warnings []string, fragments ...string) {
	t.Helper()
	if mentions(warnings, fragments...) {
		t.Errorf("warned about %v, which is acted on rather than warned about:\n%s",
			fragments, strings.Join(warnings, "\n"))
	}
}

func typeNamed(t *testing.T, schema *Schema, name string) *Type {
	t.Helper()

	named, ok := schema.Types[name]
	if !ok {
		t.Fatalf("the schema has no type %s; it has %v", name, sortedTypeNames(schema.Types))
	}
	return named
}

func fieldNamed(t *testing.T, named *Type, name string) Field {
	t.Helper()

	for _, field := range named.Fields {
		if field.Name == name {
			return field
		}
	}
	t.Fatalf("%s has no field %s", named.Name, name)
	return Field{}
}

func TestASchemaWithNoSchemaBlockUsesTheConventionalRootNames(t *testing.T) {
	schema, _ := parseSchema(t, `
type Query { a: String }
type Mutation { b: String }
`)

	if schema.QueryType != "Query" {
		t.Errorf("query root = %q, want Query", schema.QueryType)
	}
	if schema.MutationType != "Mutation" {
		t.Errorf("mutation root = %q, want Mutation", schema.MutationType)
	}
	if schema.SubscriptionType != "" {
		t.Errorf("subscription root = %q, want none: the schema defines no Subscription type",
			schema.SubscriptionType)
	}
}

func TestASchemaBlockNamesRootTypesTheConventionWouldNotFind(t *testing.T) {
	schema, warnings := parseSchema(t, `
schema {
  query: RootQuery
  mutation: RootMutation
}
type RootQuery { a: String }
type RootMutation { b: String }
`)

	if schema.QueryType != "RootQuery" || schema.MutationType != "RootMutation" {
		t.Errorf("roots = %q/%q, want RootQuery/RootMutation", schema.QueryType, schema.MutationType)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings on a complete schema: %v", warnings)
	}
}

func TestASchemaWithNoQueryRootIsWarnedAbout(t *testing.T) {
	_, warnings := parseSchema(t, `type Thing { a: String }`)

	requireWarning(t, warnings, "no query root type")
}

func TestARootTypeTheSchemaNeverDefinesIsWarnedAbout(t *testing.T) {
	_, warnings := parseSchema(t, `
schema { query: Missing }
type Query { a: String }
`)

	requireWarning(t, warnings, "Missing", "query root type")
}

func TestAFieldTypeKeepsItsListAndNonNullWrappers(t *testing.T) {
	schema, _ := parseSchema(t, `
type Query {
  plain: String
  required: String!
  list: [String]
  requiredListOfRequired: [String!]!
}
`)

	query := typeNamed(t, schema, "Query")
	for _, want := range []struct {
		field  string
		render string
	}{
		{"plain", "String"},
		{"required", "String!"},
		{"list", "[String]"},
		{"requiredListOfRequired", "[String!]!"},
	} {
		if got := fieldNamed(t, query, want.field).Type.String(); got != want.render {
			t.Errorf("Query.%s = %s, want %s", want.field, got, want.render)
		}
	}

	// The two `!` in `[String!]!` say different things, and a shape that kept
	// only one of them would make the nullable and non-nullable list identical.
	nested := fieldNamed(t, query, "requiredListOfRequired").Type
	if !nested.NonNull || nested.List == nil || !nested.List.NonNull {
		t.Errorf("[String!]! = %#v, want both wrappers non-null", nested)
	}
}

func TestArgumentDefaultsBecomeGoValuesOfTheRightType(t *testing.T) {
	schema, _ := parseSchema(t, `
enum Order { ASC DESC }
input Filter { name: String limit: Int }
type Query {
  search(
    first: Int = 10
    ratio: Float = 1.5
    term: String = "hi"
    exact: Boolean = false
    order: Order = DESC
    tags: [String!] = ["a", "b"]
    filter: Filter = {name: "x", limit: 2}
    after: String = null
    none: String
  ): [String!]!
}
`)

	args := map[string]Argument{}
	for _, arg := range fieldNamed(t, typeNamed(t, schema, "Query"), "search").Args {
		args[arg.Name] = arg
	}

	for _, want := range []struct {
		name  string
		value any
	}{
		{"first", int64(10)},
		{"ratio", 1.5},
		{"term", "hi"},
		{"exact", false},
		// An enum value is not a string: it is written unquoted, and rendering
		// it back as `"DESC"` is a value the server rejects.
		{"order", EnumValue("DESC")},
		{"tags", []any{"a", "b"}},
		{"filter", map[string]any{"name": "x", "limit": int64(2)}},
		{"after", nil},
	} {
		arg := args[want.name]
		if !arg.HasDefault {
			t.Errorf("%s has no default, want %#v", want.name, want.value)
			continue
		}
		if !reflect.DeepEqual(arg.Default, want.value) {
			t.Errorf("%s default = %#v (%T), want %#v (%T)",
				want.name, arg.Default, arg.Default, want.value, want.value)
		}
	}

	// A written `null` and no default at all both leave Default nil, which is
	// exactly why HasDefault exists.
	if args["none"].HasDefault {
		t.Errorf("none has a default, but none was written")
	}
	if !args["after"].HasDefault {
		t.Errorf("after has no default, but `= null` was written")
	}
}

func TestAnInputFieldDefaultIsReadableFromBothPlacesItIsRecorded(t *testing.T) {
	schema, _ := parseSchema(t, `
type Query { a: String }
input Paging {
  limit: Int = 25
  offset: Int
}
`)

	paging := typeNamed(t, schema, "Paging")
	limit := fieldNamed(t, paging, "limit")

	if !limit.HasDefault || limit.Default != int64(25) {
		t.Errorf("Paging.limit default = %#v (has=%v), want 25", limit.Default, limit.HasDefault)
	}
	if value, ok := limit.DefaultValue(); !ok || value != int64(25) {
		t.Errorf("DefaultValue() = %#v, %v, want 25, true", value, ok)
	}

	// Argument is documented as carrying "an input-object field's default", and
	// Args is the only place on a Field an Argument can sit, so the same value
	// is there too.
	if len(limit.Args) != 1 || limit.Args[0].Name != "limit" || limit.Args[0].Default != int64(25) {
		t.Errorf("Paging.limit args = %#v, want one argument carrying the default", limit.Args)
	}

	if offset := fieldNamed(t, paging, "offset"); len(offset.Args) != 0 || offset.HasDefault {
		t.Errorf("Paging.offset = %#v, want no default recorded anywhere", offset)
	}
}

func TestABlockStringDescriptionLosesTheIndentationItWasWrittenWith(t *testing.T) {
	schema, _ := parseSchema(t, `
"""
A person.

    Indented further, deliberately.
"""
type Query {
  "One line."
  a: String
}
`)

	query := typeNamed(t, schema, "Query")
	want := "A person.\n\n    Indented further, deliberately."
	if query.Description != want {
		t.Errorf("description = %q, want %q", query.Description, want)
	}
	if got := fieldNamed(t, query, "a").Description; got != "One line." {
		t.Errorf("field description = %q, want %q", got, "One line.")
	}
}

func TestStringEscapesAreDecodedInDescriptions(t *testing.T) {
	schema, _ := parseSchema(t, `
"A \"quoted\" word, a tab\there and \u00e9."
type Query { a: String }
`)

	want := "A \"quoted\" word, a tab\there and é."
	if got := typeNamed(t, schema, "Query").Description; got != want {
		t.Errorf("description = %q, want %q", got, want)
	}
}

func TestCommentsAndCommasDoNotReachTheParser(t *testing.T) {
	// The comma-separated `implements` list is the pre-2018 spelling and is
	// still all over schemas in the wild.
	schema, warnings := parseSchema(t, `
# The root.
type Query { a: String }

interface Node { id: ID! }   # an interface
interface Timestamped { at: String }

type User implements Node, Timestamped {
  id: ID!,    # trailing commas are ignored tokens
  at: String,
}
`)

	user := typeNamed(t, schema, "User")
	if !reflect.DeepEqual(user.Interfaces, []string{"Node", "Timestamped"}) {
		t.Errorf("User implements %v, want [Node Timestamped]", user.Interfaces)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings on a schema with nothing wrong with it: %v", warnings)
	}
}

// An `implements` list with no field block is where the comma spelling gets
// dangerous: the next definition's keyword is a name too, and swallowing it
// would silently lose a type.
func TestAnImplementsListStopsAtTheNextDefinition(t *testing.T) {
	schema, _ := parseSchema(t, `
type Query { a: String }
interface Node { id: ID! }
type Empty implements Node
type Later { b: String }
`)

	if !reflect.DeepEqual(typeNamed(t, schema, "Empty").Interfaces, []string{"Node"}) {
		t.Errorf("Empty implements %v, want [Node]", typeNamed(t, schema, "Empty").Interfaces)
	}
	typeNamed(t, schema, "Later")
}

func TestTheDeprecatedDirectiveIsActedOnRatherThanWarnedAbout(t *testing.T) {
	schema, warnings := parseSchema(t, `
type Query {
  old: String @deprecated(reason: "use new")
  older: String @deprecated
  new: String
}
enum Order { ASC DESC @deprecated(reason: "no") }
`)

	query := typeNamed(t, schema, "Query")
	old := fieldNamed(t, query, "old")
	if !old.Deprecated || old.DeprecationReason != "use new" {
		t.Errorf("Query.old = deprecated %v, reason %q; want true, %q", old.Deprecated, old.DeprecationReason, "use new")
	}

	// The specification gives @deprecated a default reason, which is what a
	// server's own introspection reports for a bare one.
	if older := fieldNamed(t, query, "older"); older.DeprecationReason != "No longer supported" {
		t.Errorf("bare @deprecated reason = %q, want the specification's default", older.DeprecationReason)
	}
	if fieldNamed(t, query, "new").Deprecated {
		t.Errorf("Query.new is marked deprecated")
	}

	order := typeNamed(t, schema, "Order")
	if !reflect.DeepEqual(order.EnumValues, []string{"ASC", "DESC"}) {
		t.Errorf("Order values = %v, want both: a deprecated value is still a legal one", order.EnumValues)
	}
	if !reflect.DeepEqual(order.DeprecatedEnumValues, []string{"DESC"}) {
		t.Errorf("Order deprecated values = %v, want [DESC]", order.DeprecatedEnumValues)
	}

	refuseWarning(t, warnings, "@deprecated")
}

func TestAnExtensionAddsToTheTypeItNames(t *testing.T) {
	schema, warnings := parseSchema(t, `
extend type Query { late: String }
type Query { early: String }
extend enum Order { DESC }
enum Order { ASC }
`)

	query := typeNamed(t, schema, "Query")
	if len(query.Fields) != 2 {
		t.Fatalf("Query has %d fields, want both the definition's and the extension's", len(query.Fields))
	}
	fieldNamed(t, query, "early")
	fieldNamed(t, query, "late")

	if got := typeNamed(t, schema, "Order").EnumValues; !reflect.DeepEqual(got, []string{"ASC", "DESC"}) {
		t.Errorf("Order values = %v, want [ASC DESC]", got)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings on a schema whose extensions all found their types: %v", warnings)
	}
}

func TestAnExtensionOfAnUndefinedTypeIsWarnedAboutAndReadAsTheDefinition(t *testing.T) {
	schema, warnings := parseSchema(t, `
type Query { a: String }
extend type Orphan { b: String }
`)

	// Read as a definition rather than discarded: an extension of a type
	// assembled elsewhere is how a federated schema is written.
	fieldNamed(t, typeNamed(t, schema, "Orphan"), "b")
	requireWarning(t, warnings, "extend type Orphan", "nothing to extend")
}

func TestAnInterfaceLearnsWhichObjectsImplementIt(t *testing.T) {
	schema, _ := parseSchema(t, `
type Query { a: String }
interface Node { id: ID! }
interface Animal implements Node { id: ID! }
type Dog implements Animal { id: ID! }
type Rock implements Node { id: ID! }
`)

	// Dog declares Animal only. The language says it should declare Node as
	// well, and reading it strictly would leave Node with a possible type
	// missing — which is the answer a consumer would act on.
	if got := typeNamed(t, schema, "Node").Possible; !reflect.DeepEqual(got, []string{"Dog", "Rock"}) {
		t.Errorf("Node possible types = %v, want [Dog Rock]", got)
	}
	if got := typeNamed(t, schema, "Animal").Possible; !reflect.DeepEqual(got, []string{"Dog"}) {
		t.Errorf("Animal possible types = %v, want [Dog]", got)
	}
}

func TestAUnionListsItsMembers(t *testing.T) {
	schema, _ := parseSchema(t, `
type Query { a: String }
type Book { title: String }
type Film { title: String }
union Media =
  | Book
  | Film
`)

	if got := typeNamed(t, schema, "Media").Possible; !reflect.DeepEqual(got, []string{"Book", "Film"}) {
		t.Errorf("Media members = %v, want [Book Film]", got)
	}
	if typeNamed(t, schema, "Media").Kind != KindUnion {
		t.Errorf("Media kind = %v, want union", typeNamed(t, schema, "Media").Kind)
	}
}

func TestAnUnmodelledDirectiveIsWarnedAboutOncePerDirectiveWithACount(t *testing.T) {
	_, warnings := parseSchema(t, `
type Query {
  a: String @auth(role: "admin")
  b: String @auth(role: "user")
  c: String @auth
}
`)

	requireWarning(t, warnings, "@auth", "3 places", "Query.a")

	// One line per directive, not one per application: a schema that puts
	// @auth on four hundred fields would otherwise bury every other warning.
	count := 0
	for _, warning := range warnings {
		if strings.Contains(warning, "@auth") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("@auth produced %d warnings, want 1:\n%s", count, strings.Join(warnings, "\n"))
	}
}

func TestADirectiveDefinitionIsWarnedAbout(t *testing.T) {
	_, warnings := parseSchema(t, `
directive @auth(role: String! = "user") repeatable on FIELD_DEFINITION | OBJECT
type Query { a: String }
`)

	requireWarning(t, warnings, "defines the directive @auth")
}

func TestASubscriptionRootIsWarnedAboutBecauseItCannotBeTestedOverHTTP(t *testing.T) {
	schema, warnings := parseSchema(t, `
type Query { a: String }
type Subscription { events: String }
`)

	if schema.SubscriptionType != "Subscription" {
		t.Errorf("subscription root = %q, want Subscription", schema.SubscriptionType)
	}
	requireWarning(t, warnings, "subscription root type")
}

func TestAReferenceToATypeTheSchemaNeverDefinesIsWarnedAbout(t *testing.T) {
	_, warnings := parseSchema(t, `
type Query {
  user(filter: MissingInput): MissingUser
}
union Media = MissingBook
type Thing implements MissingNode { a: String }
`)

	requireWarning(t, warnings, "field Query.user", "MissingUser")
	requireWarning(t, warnings, "argument Query.user(filter:)", "MissingInput")
	requireWarning(t, warnings, "union Media", "MissingBook")
	requireWarning(t, warnings, "Thing implements the interface MissingNode")
}

func TestOneOfIsRecordedAndSpecifiedByIsKept(t *testing.T) {
	schema, warnings := parseSchema(t, `
type Query { a: URL }
scalar URL @specifiedBy(url: "https://example.test/url")
input Either @oneOf {
  byID: ID
  byName: String
}
`)

	if !typeNamed(t, schema, "Either").OneOf {
		t.Errorf("Either is not marked @oneOf; a value setting every field is what it rejects")
	}
	if got := typeNamed(t, schema, "URL").SpecifiedBy; got != "https://example.test/url" {
		t.Errorf("URL specifiedBy = %q, want the url the directive gave", got)
	}
	refuseWarning(t, warnings, "@oneOf")
	refuseWarning(t, warnings, "@specifiedBy")
}

func TestAQueryDocumentIsRejectedWithAnExplanation(t *testing.T) {
	_, _, err := Parse([]byte("query Everything { user { id } }"))
	if err == nil {
		t.Fatalf("an operation document parsed as a schema")
	}
	if !strings.Contains(err.Error(), "introspection") {
		t.Errorf("error = %v; it should say what to hand vertrag instead", err)
	}
}

func TestAMalformedSchemaSaysWhereItWentWrong(t *testing.T) {
	_, _, err := Parse([]byte("type Query {\n  a: String\n  b:\n}\n"))
	if err == nil {
		t.Fatalf("a field with no type parsed")
	}
	// The position is the whole point: these documents are thousands of lines
	// long and usually generated.
	if !strings.Contains(err.Error(), "line 4") {
		t.Errorf("error = %v, want it to name line 4", err)
	}
}

func TestAnUnclosedBlockStringIsReportedWhereItStarted(t *testing.T) {
	_, _, err := Parse([]byte("type Query {\n  a: String\n}\n\"\"\"unfinished\n"))
	if err == nil {
		t.Fatalf("an unterminated block string parsed")
	}
	if !strings.Contains(err.Error(), "line 4") || !strings.Contains(err.Error(), "not closed") {
		t.Errorf("error = %v, want it to name line 4 and say the string is not closed", err)
	}
}
