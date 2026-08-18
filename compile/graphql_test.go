package compile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc/graphql"
)

// The schema the tests below are built from. It is hand-built rather than
// parsed, because the parser is somebody else's branch and because a fixture
// written in Go says exactly what it means: the cycle, the interface and the
// argument that makes a field unaskable are all deliberate.
//
//	type Query {
//	  version: String!
//	  viewer: User!
//	  userById(id: ID!): User          # unaskable: a required argument
//	  search(term: String = "x"): [SearchResult!]!
//	}
//	type Mutation { ping: String!  deleteAccount: Boolean! }
//	type User { id: ID!  name: String  friends: [User!]!  pet: Pet }
//	interface Pet { name: String! }
//	type Dog implements Pet { name: String!  barks: Boolean! }
//	type Cat implements Pet { name: String!  lives: Int }
//	union SearchResult = User | Dog
func testSchema() *graphql.Schema {
	return &graphql.Schema{
		QueryType:    "Query",
		MutationType: "Mutation",
		Types: map[string]*graphql.Type{
			"Query": {Name: "Query", Kind: graphql.KindObject, Fields: []graphql.Field{
				{Name: "version", Type: named("String", true)},
				{Name: "viewer", Type: named("User", true)},
				{Name: "userById", Type: named("User", false), Args: []graphql.Argument{
					{Name: "id", Type: named("ID", true)},
				}},
				{Name: "search", Type: listOf(named("SearchResult", true), true), Args: []graphql.Argument{
					{Name: "term", Type: named("String", false), Default: "x", HasDefault: true},
				}},
			}},
			"Mutation": {Name: "Mutation", Kind: graphql.KindObject, Fields: []graphql.Field{
				{Name: "ping", Type: named("String", true)},
				{Name: "deleteAccount", Type: named("Boolean", true)},
			}},
			"User": {Name: "User", Kind: graphql.KindObject, Fields: []graphql.Field{
				{Name: "id", Type: named("ID", true)},
				{Name: "name", Type: named("String", false)},
				// The cycle every real schema has.
				{Name: "friends", Type: listOf(named("User", true), true)},
				{Name: "pet", Type: named("Pet", false)},
			}},
			"Pet": {Name: "Pet", Kind: graphql.KindInterface, Possible: []string{"Dog", "Cat"},
				Fields: []graphql.Field{{Name: "name", Type: named("String", true)}}},
			"Dog": {Name: "Dog", Kind: graphql.KindObject, Interfaces: []string{"Pet"}, Fields: []graphql.Field{
				{Name: "name", Type: named("String", true)},
				{Name: "barks", Type: named("Boolean", true)},
			}},
			"Cat": {Name: "Cat", Kind: graphql.KindObject, Interfaces: []string{"Pet"}, Fields: []graphql.Field{
				{Name: "name", Type: named("String", true)},
				{Name: "lives", Type: named("Int", false)},
			}},
			"SearchResult": {Name: "SearchResult", Kind: graphql.KindUnion, Possible: []string{"User", "Dog"}},
			"String":       {Name: "String", Kind: graphql.KindScalar},
			"Boolean":      {Name: "Boolean", Kind: graphql.KindScalar},
			"Int":          {Name: "Int", Kind: graphql.KindScalar},
			"ID":           {Name: "ID", Kind: graphql.KindScalar},
		},
	}
}

func named(name string, nonNull bool) graphql.TypeRef {
	return graphql.TypeRef{Named: name, NonNull: nonNull}
}

func listOf(inner graphql.TypeRef, nonNull bool) graphql.TypeRef {
	return graphql.TypeRef{List: &inner, NonNull: nonNull}
}

// query returns the query document one transaction carries, which is what most
// of these tests are actually about.
func query(t *testing.T, transaction Transaction) string {
	t.Helper()
	var body struct {
		Query     string          `json:"query"`
		Variables json.RawMessage `json:"variables"`
	}
	if err := json.Unmarshal([]byte(transaction.Request.Body), &body); err != nil {
		t.Fatalf("the request body is not JSON: %v\n%s", err, transaction.Request.Body)
	}
	return body.Query
}

func transactionNamed(t *testing.T, result GraphQLResult, name string) Transaction {
	t.Helper()
	for _, transaction := range result.Transactions {
		if transaction.Name == name {
			return transaction
		}
	}
	var names []string
	for _, transaction := range result.Transactions {
		names = append(names, transaction.Name)
	}
	t.Fatalf("no transaction named %q; there are: %s", name, strings.Join(names, ", "))
	return Transaction{}
}

func TestAFieldReturningAnObjectGetsASelectionSet(t *testing.T) {
	result := CompileGraphQL(testSchema(), GraphQLOptions{MaxDepth: 1}, "schema.graphql")

	got := query(t, transactionNamed(t, result, "Query > viewer"))
	want := strings.Join([]string{
		"query viewer {",
		"  viewer {",
		"    id",
		"    name",
		"  }",
		"}",
	}, "\n")
	if got != want {
		t.Errorf("query =\n%s\nwant\n%s", got, want)
	}
}

// A scalar has no fields to select, and asking for a selection on one is a
// syntax error in the other direction — so the root field is the whole query.
func TestAScalarFieldIsALeafOfTheSelection(t *testing.T) {
	result := CompileGraphQL(testSchema(), GraphQLOptions{}, "schema.graphql")

	got := query(t, transactionNamed(t, result, "Query > version"))
	want := "query version {\n  version\n}"
	if got != want {
		t.Errorf("query =\n%s\nwant\n%s", got, want)
	}
}

// The one every GraphQL tester has to get right: `User.friends: [User!]!` is a
// cycle, and an unbounded walk of it produces a query that never ends.
func TestACyclicSchemaDoesNotProduceAnInfiniteQuery(t *testing.T) {
	result := CompileGraphQL(testSchema(), GraphQLOptions{MaxDepth: 3}, "schema.graphql")

	got := query(t, transactionNamed(t, result, "Query > viewer"))
	if strings.Count(got, "friends") != 2 {
		t.Errorf("expanded `friends` %d times at a depth bound of 3; the query is\n%s",
			strings.Count(got, "friends"), got)
	}
	// The bound is a bound on nesting, so the deepest selection set holds
	// leaves alone.
	if strings.Contains(got, "\n            friends") {
		t.Errorf("the depth bound did not stop the walk:\n%s", got)
	}
}

// A field whose expansion is cut has to be dropped, not emitted bare: `{ user
// }` where user is an object is not a thinner query, it is one the server
// refuses to parse — and it takes every other field in the request with it.
func TestAFieldWhoseExpansionWasCutIsDroppedFromTheSelection(t *testing.T) {
	result := CompileGraphQL(testSchema(), GraphQLOptions{MaxDepth: 1}, "schema.graphql")

	got := query(t, transactionNamed(t, result, "Query > viewer"))
	for _, cut := range []string{"friends", "pet"} {
		if strings.Contains(got, cut) {
			t.Errorf("`%s` is beyond the depth bound and was still selected:\n%s", cut, got)
		}
	}
	// Dropped from the SELECTION, while the fields that fit stayed.
	if !strings.Contains(got, "id") {
		t.Errorf("the whole selection was dropped rather than the cut field:\n%s", got)
	}
	// And the count is reported rather than silently swallowed: two fields of
	// User, plus the four the `search` transaction loses the same way.
	if result.Trimmed == 0 {
		t.Error("fields were dropped from the selections and the result reported none")
	}
}

// A selection on a union with no inline fragments is a syntax error, so the
// fragments are not a nicety.
func TestAUnionIsSelectedWithInlineFragments(t *testing.T) {
	result := CompileGraphQL(testSchema(), GraphQLOptions{MaxDepth: 2}, "schema.graphql")

	got := query(t, transactionNamed(t, result, "Query > search"))
	for _, want := range []string{"__typename", "... on User {", "... on Dog {"} {
		if !strings.Contains(got, want) {
			t.Errorf("the union selection is missing %q:\n%s", want, got)
		}
	}
}

// An interface's own fields can be selected directly, so the fragments carry
// only what each implementation adds — repeating the common fields inside
// every one of them would ask for the same value twice per implementation.
func TestAnInterfaceAddsOnlyWhatEachTypeDeclaresBeyondIt(t *testing.T) {
	result := CompileGraphQL(testSchema(), GraphQLOptions{MaxDepth: 2}, "schema.graphql")

	got := query(t, transactionNamed(t, result, "Query > viewer"))
	if !strings.Contains(got, "... on Dog {\n        barks\n      }") {
		t.Errorf("the Dog fragment does not carry what Dog adds:\n%s", got)
	}
	// `name` belongs to the interface, so it is selected on the interface and
	// not again inside each fragment.
	if !strings.Contains(got, "... on Cat {\n        lives\n      }") {
		t.Errorf("the Cat fragment repeats the interface's own fields:\n%s", got)
	}
}

// Names are what hook files and `--only` address, so they must not move when
// an unrelated part of the schema changes. They are built from the operation
// kind and the field, and from nothing else — not the root type's name, which
// can be renamed, and not the path, which is configuration.
func TestTransactionNamesAreStableAndReadable(t *testing.T) {
	schema := testSchema()
	first := CompileGraphQL(schema, GraphQLOptions{Mutations: true}, "schema.graphql")

	want := []string{"Query > version", "Query > viewer", "Query > search",
		"Mutation > ping", "Mutation > deleteAccount"}
	var got []string
	for _, transaction := range first.Transactions {
		got = append(got, transaction.Name)
	}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("names = %v\nwant     %v", got, want)
	}

	// The things that must not move a name: the root type's own name, the path
	// the queries go to, and the depth the selections were built at.
	schema.QueryType = "RootQuery"
	schema.Types["RootQuery"] = schema.Types["Query"]
	second := CompileGraphQL(schema, GraphQLOptions{
		Mutations: true, Path: "/api/graphql", MaxDepth: 1}, "schema.graphql")

	var moved []string
	for _, transaction := range second.Transactions {
		moved = append(moved, transaction.Name)
	}
	if strings.Join(moved, ", ") != strings.Join(got, ", ") {
		t.Errorf("renaming the root type and moving the path moved the names:\n%v\nwas\n%v", moved, got)
	}
}

func TestTheRequestIsAPostOfAGraphQLDocument(t *testing.T) {
	result := CompileGraphQL(testSchema(), GraphQLOptions{}, "schema.graphql")
	transaction := transactionNamed(t, result, "Query > version")

	if transaction.Request.Method != "POST" {
		t.Errorf("method = %q, want POST", transaction.Request.Method)
	}
	if transaction.Request.URI != "/graphql" {
		t.Errorf("URI = %q, want /graphql", transaction.Request.URI)
	}
	var contentType string
	for _, header := range transaction.Request.Headers {
		if header.Name == "Content-Type" {
			contentType = header.Value
		}
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}

	// The body is the transport form GraphQL over HTTP defines, and
	// `variables` is present and empty: nothing fills it yet, and a server
	// that refuses an empty variables object is refusing a legal request.
	var body map[string]any
	if err := json.Unmarshal([]byte(transaction.Request.Body), &body); err != nil {
		t.Fatalf("the body is not JSON: %v", err)
	}
	if _, ok := body["query"].(string); !ok {
		t.Errorf("the body carries no query: %s", transaction.Request.Body)
	}
	variables, ok := body["variables"].(map[string]any)
	if !ok || len(variables) != 0 {
		t.Errorf("variables = %v, want an empty object", body["variables"])
	}

	// A status and nothing else: at this endpoint 200 is what an error looks
	// like too, so the expectation cannot be where the judgement lives.
	if transaction.Response.Status != "200" {
		t.Errorf("expected status = %q, want 200", transaction.Response.Status)
	}
	if len(transaction.Response.Headers) != 0 {
		t.Errorf("the expectation demands headers %v; both application/json and "+
			"application/graphql-response+json are correct answers", transaction.Response.Headers)
	}
}

func TestThePathTheQueriesGoToIsConfigurable(t *testing.T) {
	for _, test := range []struct{ configured, want string }{
		{"", "/graphql"},
		{"/api/graphql", "/api/graphql"},
		// A path written without its leading slash is what somebody meant.
		{"v1/graphql", "/v1/graphql"},
	} {
		result := CompileGraphQL(testSchema(), GraphQLOptions{Path: test.configured}, "schema.graphql")
		if got := result.Transactions[0].Request.URI; got != test.want {
			t.Errorf("path %q compiled to URI %q, want %q", test.configured, got, test.want)
		}
	}
}

// The safety interlock, and the reason the section exists. A mutation is by
// definition the operation that changes something, and `deleteAccount` is as
// reachable from a schema as `viewer`.
func TestAMutationIsNotBuiltUnlessItWasAskedFor(t *testing.T) {
	withheldResult := CompileGraphQL(testSchema(), GraphQLOptions{}, "schema.graphql")

	for _, transaction := range withheldResult.Transactions {
		if strings.HasPrefix(transaction.Name, "Mutation > ") {
			t.Errorf("a mutation was compiled into a run that did not ask for one: %s", transaction.Name)
		}
	}

	// Withheld is not the same as invisible. A run that tested less than the
	// reader believes is the failure this repository cares most about, so the
	// count, the names and the way to enable them all have to be reported.
	var withheldNames []string
	for _, withheld := range withheldResult.Withheld {
		if withheld.Reason == WithheldMutation {
			withheldNames = append(withheldNames, withheld.Name)
		}
	}
	if strings.Join(withheldNames, ", ") != "Mutation > ping, Mutation > deleteAccount" {
		t.Errorf("withheld mutations = %v", withheldNames)
	}
	notes := strings.Join(withheldResult.Notes(), "\n")
	for _, want := range []string{"2 of the schema's operations", "Mutation > deleteAccount", "mutations: true"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the note does not say %q:\n%s", want, notes)
		}
	}

	// And asked for, they are built.
	asked := CompileGraphQL(testSchema(), GraphQLOptions{Mutations: true}, "schema.graphql")
	transaction := transactionNamed(t, asked, "Mutation > deleteAccount")
	if got := query(t, transaction); !strings.HasPrefix(got, "mutation deleteAccount {") {
		t.Errorf("the document is not a mutation:\n%s", got)
	}
	for _, withheld := range asked.Withheld {
		if withheld.Reason == WithheldMutation {
			t.Errorf("%s was still withheld after mutations were asked for", withheld.Name)
		}
	}
}

// Round three generates argument values. Until it does, a field that cannot be
// asked for without them is withheld and says so — sending it bare would put a
// query the server refuses on the wire and report the refusal as though the
// API were at fault.
func TestAFieldThatRequiresArgumentsIsWithheldWithItsReason(t *testing.T) {
	result := CompileGraphQL(testSchema(), GraphQLOptions{}, "schema.graphql")

	for _, transaction := range result.Transactions {
		if transaction.Name == "Query > userById" {
			t.Fatalf("userById(id: ID!) was compiled without a value for `id`:\n%s", query(t, transaction))
		}
	}

	var reason string
	for _, withheld := range result.Withheld {
		if withheld.Name == "Query > userById" {
			reason = withheld.Reason
		}
	}
	if reason != WithheldArguments {
		t.Errorf("userById was withheld for %q, want the argument reason", reason)
	}
}

// An argument is required only when it is non-null AND has no default: the
// server applies the default for one that is left out, so `search(term: String
// = "x")` is perfectly askable.
func TestAnArgumentWithADefaultDoesNotWithholdTheField(t *testing.T) {
	result := CompileGraphQL(testSchema(), GraphQLOptions{}, "schema.graphql")
	transaction := transactionNamed(t, result, "Query > search")

	if got := query(t, transaction); strings.Contains(got, "term") {
		t.Errorf("the default was written into the query rather than left to the server:\n%s", got)
	}
}

// A schema referring to a type it does not declare is a broken document, and
// the transaction it would have produced cannot be built. Both halves are
// reported: the field is withheld, and the document is annotated.
func TestAnUndeclaredTypeIsReportedAsAnAnnotation(t *testing.T) {
	schema := testSchema()
	schema.Types["Query"].Fields = append(schema.Types["Query"].Fields,
		graphql.Field{Name: "ghost", Type: named("Phantom", false)})

	result := CompileGraphQL(schema, GraphQLOptions{}, "schema.graphql")

	var found bool
	for _, annotation := range result.Annotations {
		if strings.Contains(annotation.Message, "Phantom") {
			found = true
			if annotation.Type != "warning" {
				t.Errorf("the annotation is a %q; an undeclared type stops one field, not the run", annotation.Type)
			}
		}
	}
	if !found {
		t.Errorf("nothing was said about the undeclared type: %+v", result.Annotations)
	}
}

// The expectation is derived from the selection that was asked for, because
// that is what the server owes an answer to.
func TestTheExpectationFollowsTheSelectionSet(t *testing.T) {
	result := CompileGraphQL(testSchema(), GraphQLOptions{MaxDepth: 2}, "schema.graphql")
	expectation := transactionNamed(t, result, "Query > viewer").GraphQL

	if expectation == nil {
		t.Fatal("a GraphQL transaction carries no expectation, so nothing downstream can judge it")
	}
	if expectation.Operation != "query" || expectation.Field != "viewer" {
		t.Errorf("expectation names %s %s", expectation.Operation, expectation.Field)
	}
	if len(expectation.Data) != 1 || expectation.Data[0].Key != "viewer" {
		t.Fatalf("data shape = %+v", expectation.Data)
	}

	viewer := expectation.Data[0]
	if !viewer.NonNull || viewer.Type != "User!" {
		t.Errorf("viewer: nonNull = %v, type = %q; the schema says User!", viewer.NonNull, viewer.Type)
	}

	byKey := map[string]GraphQLSelection{}
	for _, selection := range viewer.Fields {
		byKey[selection.Key] = selection
	}
	if friends := byKey["friends"]; friends.ListDepth != 1 || !friends.ElementNonNull || !friends.NonNull {
		t.Errorf("friends = %+v; the schema says [User!]!", friends)
	}
	if name := byKey["name"]; name.NonNull {
		t.Errorf("name is nullable in the schema and the expectation demands a value")
	}

	// A field that came from an inline fragment is only there when the object
	// turned out to be that type, so its absence must not be a finding.
	var barks GraphQLSelection
	for _, selection := range byKey["pet"].Fields {
		if selection.Key == "barks" {
			barks = selection
		}
	}
	if barks.Key == "" || !barks.Conditional {
		t.Errorf("`barks` came from `... on Dog` and is not marked conditional: %+v", byKey["pet"].Fields)
	}
	if petName := findSelection(byKey["pet"].Fields, "name"); petName.Conditional {
		t.Errorf("`name` is the interface's own field and must be required, not conditional")
	}
}

func findSelection(selections []GraphQLSelection, key string) GraphQLSelection {
	for _, selection := range selections {
		if selection.Key == key {
			return selection
		}
	}
	return GraphQLSelection{}
}

// A subscription is a stream, so there is no transaction to send it as — and
// that is reported rather than passed over, because a schema's subscriptions
// are part of what it offers and a run that covered none of them in silence
// reads as a run that covered the schema.
func TestASubscriptionIsReportedAsSomethingVertragCannotSend(t *testing.T) {
	schema := testSchema()
	schema.SubscriptionType = "Subscription"
	schema.Types["Subscription"] = &graphql.Type{
		Name: "Subscription", Kind: graphql.KindObject,
		Fields: []graphql.Field{{Name: "userChanged", Type: named("User", true)}},
	}

	result := CompileGraphQL(schema, GraphQLOptions{Mutations: true}, "schema.graphql")

	for _, transaction := range result.Transactions {
		if strings.HasPrefix(transaction.Name, "Subscription > ") {
			t.Errorf("a subscription was compiled into an HTTP transaction: %s", transaction.Name)
		}
	}
	var reason string
	for _, withheld := range result.Withheld {
		if withheld.Name == "Subscription > userChanged" {
			reason = withheld.Reason
		}
	}
	if reason != WithheldSubscription {
		t.Errorf("the subscription was withheld for %q, want the stream reason", reason)
	}
}

// A schema is not a run's only input, and an empty one has to say so rather
// than produce a run of nothing that reads as an API with nothing in it.
func TestAnEmptySchemaIsReportedRatherThanRunAsNothing(t *testing.T) {
	result := CompileGraphQL(nil, GraphQLOptions{}, "schema.graphql")
	if len(result.Annotations) != 1 || result.Annotations[0].Type != "error" {
		t.Errorf("annotations = %+v, want one error", result.Annotations)
	}

	missing := CompileGraphQL(&graphql.Schema{QueryType: "Query", Types: map[string]*graphql.Type{}},
		GraphQLOptions{}, "schema.graphql")
	if len(missing.Annotations) != 1 || missing.Annotations[0].Type != "error" {
		t.Errorf("a schema whose query root it never declares produced %+v", missing.Annotations)
	}
}
