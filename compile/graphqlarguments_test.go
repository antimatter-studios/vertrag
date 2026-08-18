package compile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc/graphql"
)

// argumentSchema is a schema written around the argument types, rather than
// around the selection sets testSchema exercises: an enum with a deprecated
// member, an input object that refers to itself, a @oneOf input, a custom
// scalar, and the Int whose bounds are only 32 bits wide.
func argumentSchema() *graphql.Schema {
	return &graphql.Schema{
		QueryType: "Query",
		Types: map[string]*graphql.Type{
			"Query": {Name: "Query", Kind: graphql.KindObject, Fields: []graphql.Field{
				{Name: "count", Type: named("Int", true), Args: []graphql.Argument{
					{Name: "size", Type: named("Int", true)},
				}},
				{Name: "byStatus", Type: named("String", true), Args: []graphql.Argument{
					{Name: "status", Type: named("Status", true)},
				}},
				{Name: "since", Type: named("String", true), Args: []graphql.Argument{
					{Name: "at", Type: named("DateTime", true)},
				}},
				{Name: "filtered", Type: named("String", true), Args: []graphql.Argument{
					{Name: "filter", Type: named("Filter", true)},
				}},
				{Name: "one", Type: named("String", true), Args: []graphql.Argument{
					{Name: "by", Type: named("Selector", true)},
				}},
				{Name: "paged", Type: named("String", true), Args: []graphql.Argument{
					{Name: "first", Type: named("Int", true), Default: int64(10), HasDefault: true},
					{Name: "order", Type: named("Status", true),
						Default: graphql.EnumValue("ACTIVE"), HasDefault: true},
				}},
			}},
			"Status": {Name: "Status", Kind: graphql.KindEnum,
				EnumValues:           []string{"ACTIVE", "RETIRED"},
				DeprecatedEnumValues: []string{"RETIRED"}},
			"DateTime": {Name: "DateTime", Kind: graphql.KindScalar,
				SpecifiedBy: "https://scalars.graphql.org/andimarek/date-time"},
			"Filter": {Name: "Filter", Kind: graphql.KindInputObject, Fields: []graphql.Field{
				{Name: "term", Type: named("String", true)},
				// The cycle every filter input has.
				{Name: "and", Type: listOf(named("Filter", true), false)},
			}},
			"Selector": {Name: "Selector", Kind: graphql.KindInputObject, OneOf: true, Fields: []graphql.Field{
				{Name: "id", Type: named("ID", false)},
				{Name: "name", Type: named("String", false)},
			}},
			"String":  {Name: "String", Kind: graphql.KindScalar},
			"Boolean": {Name: "Boolean", Kind: graphql.KindScalar},
			"Int":     {Name: "Int", Kind: graphql.KindScalar},
			"ID":      {Name: "ID", Kind: graphql.KindScalar},
		},
	}
}

// argumentOf returns the single argument a transaction carries.
func argumentOf(t *testing.T, transaction Transaction) GraphQLArgument {
	t.Helper()
	if len(transaction.Request.GraphQLArguments) != 1 {
		t.Fatalf("%s carries %d arguments, want one", transaction.Name, len(transaction.Request.GraphQLArguments))
	}
	return transaction.Request.GraphQLArguments[0]
}

// schemaOf decodes an argument's JSON Schema, so a test can assert about one
// keyword rather than about the whole encoded string.
func schemaOf(t *testing.T, argument GraphQLArgument) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(argument.Schema), &decoded); err != nil {
		t.Fatalf("the argument schema is not JSON: %v\n%s", err, argument.Schema)
	}
	return decoded
}

// GraphQL's Int is a signed 32-BIT integer, not the language's own int. Stating
// the bounds is what sends the coverage phase to 2147483647 and one past it,
// which is where a server backed by a 64-bit column and no validation is wrong.
func TestAnIntArgumentIsBoundedToThirtyTwoBits(t *testing.T) {
	result := CompileGraphQL(argumentSchema(), GraphQLOptions{}, "schema.graphql")
	schema := schemaOf(t, argumentOf(t, transactionNamed(t, result, "Query > count")))

	if schema["type"] != "integer" {
		t.Errorf("type = %v, want integer", schema["type"])
	}
	if schema["minimum"] != float64(-2147483648) || schema["maximum"] != float64(2147483647) {
		t.Errorf("bounds = %v..%v, want the 32-bit range", schema["minimum"], schema["maximum"])
	}
}

// An enum member is a JSON string in `variables` — the coercion into the enum
// is the server's — so the schema says string, and lists the members.
//
// A deprecated member is dropped while another remains. It is still a legal
// value, so it is not an error to send one; it is simply not what a generator
// should reach for first.
func TestAnEnumArgumentOffersItsMembersAndPrefersTheLivingOnes(t *testing.T) {
	result := CompileGraphQL(argumentSchema(), GraphQLOptions{}, "schema.graphql")
	schema := schemaOf(t, argumentOf(t, transactionNamed(t, result, "Query > byStatus")))

	values, _ := schema["enum"].([]any)
	if len(values) != 1 || values[0] != "ACTIVE" {
		t.Errorf("enum = %v, want the one member that is not deprecated", values)
	}
	if schema["type"] != "string" {
		t.Errorf("type = %v, want string: in `variables` an enum member is a JSON string", schema["type"])
	}
}

// An enum member is written UNQUOTED in query text. `status: Status! = ACTIVE`
// rendered as `= "ACTIVE"` is a different value the server rejects, and the two
// are only distinguishable because apidesc keeps EnumValue apart from string —
// which only helps if this reads the enum case first.
func TestAnEnumDefaultIsWrittenUnquotedInTheVariableDeclaration(t *testing.T) {
	result := CompileGraphQL(argumentSchema(), GraphQLOptions{}, "schema.graphql")
	document := query(t, transactionNamed(t, result, "Query > paged"))

	if !strings.Contains(document, "$order: Status! = ACTIVE") {
		t.Errorf("the enum default was not written as a bare enum value:\n%s", document)
	}
	if strings.Contains(document, `"ACTIVE"`) {
		t.Errorf("the enum default was quoted, which makes it a string:\n%s", document)
	}
}

// A non-null argument carrying a default cannot have its variable declared bare
// and then left undefined: the specification makes an undefined non-null
// variable a coercion error, so the whole request fails before the server looks
// at anything. Repeating the default on the variable is what keeps it optional.
func TestANonNullArgumentWithADefaultRepeatsItOnTheVariable(t *testing.T) {
	result := CompileGraphQL(argumentSchema(), GraphQLOptions{}, "schema.graphql")
	transaction := transactionNamed(t, result, "Query > paged")

	if document := query(t, transaction); !strings.Contains(document, "$first: Int! = 10") {
		t.Errorf("the default was not carried onto the variable:\n%s", document)
	}
	if values := variables(t, transaction); len(values) != 0 {
		t.Errorf("variables = %v, want none: the default is what makes them unnecessary", values)
	}
}

// A custom scalar's value space is defined outside the schema — that is what
// makes it custom — so a generated value is a guess and its rejection would be
// a finding about vertrag. The field is withheld and the annotation names the
// scalar, so the reader can see which type cost them the operation.
func TestACustomScalarArgumentWithholdsTheFieldAndNamesTheScalar(t *testing.T) {
	result := CompileGraphQL(argumentSchema(), GraphQLOptions{}, "schema.graphql")

	for _, transaction := range result.Transactions {
		if transaction.Name == "Query > since" {
			t.Fatalf("a DateTime argument was given a guessed value:\n%s", query(t, transaction))
		}
	}

	var reason string
	for _, withheld := range result.Withheld {
		if withheld.Name == "Query > since" {
			reason = withheld.Reason
		}
	}
	if reason != WithheldArguments {
		t.Errorf("since was withheld for %q, want the argument reason", reason)
	}

	named := false
	for _, annotation := range result.Annotations {
		if strings.Contains(annotation.Message, `"DateTime"`) &&
			strings.Contains(annotation.Message, "scalars.graphql.org") {
			named = true
		}
	}
	if !named {
		t.Errorf("no annotation names the scalar or its @specifiedBy: %+v", result.Annotations)
	}
}

// An input object that refers to itself is the ordinary way to write a filter,
// and a walk that did not notice would expand for ever. The repeat is always
// through a nullable field — the specification forbids an input object to
// require itself — so dropping it there leaves a value the server accepts.
func TestARecursiveInputObjectStopsAtTheRepeat(t *testing.T) {
	result := CompileGraphQL(argumentSchema(), GraphQLOptions{}, "schema.graphql")
	schema := schemaOf(t, argumentOf(t, transactionNamed(t, result, "Query > filtered")))

	properties, _ := schema["properties"].(map[string]any)
	if _, present := properties["term"]; !present {
		t.Errorf("the input object lost its own fields: %v", properties)
	}
	nested, _ := properties["and"].(map[string]any)
	items, _ := nested["items"].(map[string]any)
	inner, _ := items["properties"].(map[string]any)
	if _, cycles := inner["and"]; cycles {
		t.Errorf("the cycle was followed a second time: %v", inner)
	}
	if _, present := inner["term"]; !present {
		t.Errorf("the first repeat lost the fields that are not the cycle: %v", inner)
	}
}

// A @oneOf input must carry exactly one of its fields, and filling one in field
// by field is what it exists to reject: generation includes each optional
// property on a coin toss, which produces the empty object and the two-field
// object. Enumerating the legal shapes means every value drawn is one of them.
func TestAOneOfInputOffersOnlyItsSingleFieldShapes(t *testing.T) {
	result := CompileGraphQL(argumentSchema(), GraphQLOptions{}, "schema.graphql")
	argument := argumentOf(t, transactionNamed(t, result, "Query > one"))
	schema := schemaOf(t, argument)

	if _, hasProperties := schema["properties"]; hasProperties {
		t.Errorf("a @oneOf input was described as an ordinary object: %s", argument.Schema)
	}
	shapes, _ := schema["enum"].([]any)
	if len(shapes) != 2 {
		t.Fatalf("enum = %v, want one shape per field", shapes)
	}
	for _, shape := range shapes {
		object, ok := shape.(map[string]any)
		if !ok || len(object) != 1 {
			t.Errorf("shape %v sets %d fields, want exactly one", shape, len(object))
		}
	}
}

// Two fields of one document can declare the same argument name — `first` on
// every paginated field is the ordinary case — and one variable holding both
// values would silently tie them together.
func TestTwoFieldsSharingAnArgumentNameGetDistinctVariables(t *testing.T) {
	schema := argumentSchema()
	schema.Types["Query"].Fields = []graphql.Field{{
		Name: "outer", Type: named("Page", true),
		Args: []graphql.Argument{{Name: "first", Type: named("Int", true)}},
	}}
	schema.Types["Page"] = &graphql.Type{Name: "Page", Kind: graphql.KindObject, Fields: []graphql.Field{
		{Name: "inner", Type: named("String", true),
			Args: []graphql.Argument{{Name: "first", Type: named("Int", true)}}},
	}}

	result := CompileGraphQL(schema, GraphQLOptions{}, "schema.graphql")
	transaction := transactionNamed(t, result, "Query > outer")

	document := query(t, transaction)
	if !strings.Contains(document, "outer(first: $first)") || !strings.Contains(document, "inner(first: $first2)") {
		t.Errorf("the two `first` arguments share one variable:\n%s", document)
	}
	if values := variables(t, transaction); len(values) != 2 {
		t.Errorf("variables = %v, want a value for each", values)
	}
}

// The mutation gate is the outer interlock, and generation must never become a
// way around it. A withheld mutation produces no transaction, so it produces no
// argument schema, no variable and no value — there is nothing for a probing
// phase to fill in, because there is nothing for it to fill in.
func TestAWithheldMutationIsNotReachableByGeneration(t *testing.T) {
	schema := argumentSchema()
	schema.MutationType = "Mutation"
	schema.Types["Mutation"] = &graphql.Type{Name: "Mutation", Kind: graphql.KindObject, Fields: []graphql.Field{
		{Name: "deleteAccount", Type: named("Boolean", true), Args: []graphql.Argument{
			{Name: "id", Type: named("ID", true)},
		}},
	}}

	result := CompileGraphQL(schema, GraphQLOptions{}, "schema.graphql")

	for _, transaction := range result.Transactions {
		if strings.Contains(transaction.Name, "deleteAccount") {
			t.Fatalf("the withheld mutation became a transaction: %s", transaction.Name)
		}
		for _, argument := range transaction.Request.GraphQLArguments {
			if strings.Contains(argument.Field, "deleteAccount") {
				t.Errorf("an argument of the withheld mutation is generatable: %+v", argument)
			}
		}
		if strings.Contains(transaction.Request.Body, "deleteAccount") {
			t.Errorf("%s carries the withheld mutation in its body: %s", transaction.Name, transaction.Request.Body)
		}
	}

	withheld := false
	for _, entry := range result.Withheld {
		if entry.Name == "Mutation > deleteAccount" && entry.Reason == WithheldMutation {
			withheld = true
		}
	}
	if !withheld {
		t.Errorf("the mutation was not withheld with the mutation reason: %+v", result.Withheld)
	}
}

// Introspection's meta-fields describe the schema rather than the API, and
// `__type(name: String!)` is exactly the shape generating argument values would
// otherwise make sendable.
func TestAnIntrospectionMetaFieldIsNotSentAsAnOperation(t *testing.T) {
	schema := argumentSchema()
	schema.Types["Query"].Fields = append(schema.Types["Query"].Fields, graphql.Field{
		Name: "__type", Type: named("String", false),
		Args: []graphql.Argument{{Name: "name", Type: named("String", true)}},
	})

	result := CompileGraphQL(schema, GraphQLOptions{}, "schema.graphql")
	for _, transaction := range result.Transactions {
		if strings.Contains(transaction.Name, "__type") {
			t.Fatalf("an introspection meta-field became an operation: %s", transaction.Name)
		}
	}

	var reason string
	for _, withheld := range result.Withheld {
		if withheld.Name == "Query > __type" {
			reason = withheld.Reason
		}
	}
	if reason != WithheldIntrospection {
		t.Errorf("__type was withheld for %q, want the introspection reason", reason)
	}
}

// A generated value has to be replaceable without touching the query, because
// the query is vertrag's and a value substituted into it would be a different
// question put to the server.
func TestAnArgumentValueIsReplacedInTheVariablesAndNotInTheQuery(t *testing.T) {
	result := CompileGraphQL(argumentSchema(), GraphQLOptions{}, "schema.graphql")
	transaction := transactionNamed(t, result, "Query > count")

	before := query(t, transaction)
	request, err := transaction.Request.SetGraphQLArgument(argumentOf(t, transaction), int64(2147483647))
	if err != nil {
		t.Fatalf("setting the argument: %v", err)
	}

	transaction.Request = request
	if after := query(t, transaction); after != before {
		t.Errorf("the query changed:\n%s\nwas\n%s", after, before)
	}
	// json.Number, not float64: re-encoding the boundary as a float would hand
	// the server 2.147483647e+09, which it rejects — a finding about the
	// encoder, appearing only at the boundary the coverage phase exists to send.
	if !strings.Contains(request.Body, "2147483647") {
		t.Errorf("the value did not survive the round trip: %s", request.Body)
	}
}
