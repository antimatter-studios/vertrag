package graphql

import "testing"

// The tests here are all one rule: a schema that says something this package
// cannot act on gets said out loud. The alternative is a run that quietly tests
// less than the reader thinks it does and then passes.

func TestATypeDefinedTwiceIsWarnedAboutRatherThanSilentlyMerged(t *testing.T) {
	schema, warnings := parseSchema(t, `
type Query { a: String }
type Query { b: String }
`)

	// Merging is the useful answer — discarding either definition would lose
	// fields that exist — but it matches neither definition, so it is said.
	if len(typeNamed(t, schema, "Query").Fields) != 2 {
		t.Errorf("Query has %d fields, want both", len(typeNamed(t, schema, "Query").Fields))
	}
	requireWarning(t, warnings, "Query is defined twice")
}

func TestADirectiveOnTheWrongKindOfThingIsWarnedAbout(t *testing.T) {
	schema, warnings := parseSchema(t, `
type Query @oneOf { a: String }
`)

	if typeNamed(t, schema, "Query").OneOf {
		t.Errorf("@oneOf was applied to an object type, where it means nothing")
	}
	requireWarning(t, warnings, "@oneOf", "rather than an input object")
}

func TestARootTypeThatIsNotAnObjectIsWarnedAbout(t *testing.T) {
	_, warnings := parseSchema(t, `
schema { query: Order }
enum Order { ASC DESC }
`)

	requireWarning(t, warnings, "Order", "rather than an object type")
}

func TestExtendSchemaAddsARootType(t *testing.T) {
	schema, warnings := parseSchema(t, `
schema { query: Query }
extend schema { mutation: Changes }
type Query { a: String }
type Changes { b: String }
`)

	if schema.MutationType != "Changes" {
		t.Errorf("mutation root = %q, want Changes", schema.MutationType)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings on a schema with nothing wrong with it: %v", warnings)
	}
}

func TestAnExtensionThatContradictsItsDefinitionIsRefusedOutLoud(t *testing.T) {
	schema, warnings := parseSchema(t, `
type Query { a: String }
extend input Query { b: String }
`)

	// Folding an input field into an object type would produce a type that
	// cannot exist, so the extension is dropped — and named, because the
	// document plainly meant something by it.
	if len(typeNamed(t, schema, "Query").Fields) != 1 {
		t.Errorf("Query has %d fields, want only the definition's", len(typeNamed(t, schema, "Query").Fields))
	}
	requireWarning(t, warnings, "extend input Query", "does not match the definition")
}

func TestAnArgumentCarriesItsOwnDeprecation(t *testing.T) {
	schema, warnings := parseSchema(t, `
type Query {
  search(term: String, old: String @deprecated(reason: "use term")): String
}
`)

	args := fieldNamed(t, typeNamed(t, schema, "Query"), "search").Args
	if len(args) != 2 {
		t.Fatalf("search has %d arguments, want 2", len(args))
	}
	if args[0].Deprecated {
		t.Errorf("term is marked deprecated")
	}
	if !args[1].Deprecated || args[1].DeprecationReason != "use term" {
		t.Errorf("old = deprecated %v, reason %q; want true, %q",
			args[1].Deprecated, args[1].DeprecationReason, "use term")
	}
	refuseWarning(t, warnings, "@deprecated")
}

// TestADefaultRecordedOnlyInArgsIsStillFound covers the other half of the two
// places an input field's default lives: a consumer that builds a Field itself,
// or reads one from a future source that fills in only the Argument, gets the
// same answer.
func TestADefaultRecordedOnlyInArgsIsStillFound(t *testing.T) {
	field := Field{
		Name: "limit",
		Type: TypeRef{Named: "Int"},
		Args: []Argument{{Name: "limit", Type: TypeRef{Named: "Int"}, Default: int64(25), HasDefault: true}},
	}

	value, ok := field.DefaultValue()
	if !ok || value != int64(25) {
		t.Errorf("DefaultValue() = %#v, %v; want 25, true", value, ok)
	}

	if _, ok := (Field{Name: "limit"}).DefaultValue(); ok {
		t.Errorf("a field with no default reported one")
	}
}

// TestAnEnumDefaultIsNotAString is the distinction a generator gets wrong the
// moment it is lost: an enum value is written unquoted, and `"DESC"` is a
// different value that the server rejects.
func TestAnEnumDefaultIsNotAString(t *testing.T) {
	schema, _ := parseSchema(t, `
enum Order { ASC DESC }
type Query { search(order: Order = DESC): String }
`)

	value := fieldNamed(t, typeNamed(t, schema, "Query"), "search").Args[0].Default
	enum, ok := value.(EnumValue)
	if !ok {
		t.Fatalf("default = %#v (%T), want an EnumValue", value, value)
	}
	if enum.String() != "DESC" {
		t.Errorf("EnumValue.String() = %q, want DESC", enum.String())
	}
	if _, isString := value.(string); isString {
		t.Errorf("an enum default came through as a string, which would be rendered quoted")
	}
}
