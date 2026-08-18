package compile

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/antimatter-studios/vertrag/apidesc/graphql"
)

// GraphQL reaches this package without passing through API Elements, and that
// is a decision rather than an oversight.
//
// Every other format vertrag reads is a set of resources, each with a URI and
// a method, which is exactly what API Elements describes — so OpenAPI 2, 3 and
// Blueprint all become API Elements and one compiler serves all three. A
// GraphQL schema has none of that. It has one endpoint, one method, and its
// operations are fields of a type. Expressing it as API Elements would mean
// inventing a resource per field and an href for each, and those hrefs would
// then decide the transaction names — which are what hook files and `--only`
// address, and what the report prints. Names derived from a URI nothing can be
// requested at is precisely the sort of lie that makes a tool unusable at the
// point somebody needs to act on its output.
//
// So the schema comes straight here and produces the same Transaction values
// the API Elements path produces. Everything downstream — filters, hooks,
// auth, transport, the runner, every reporter — is unchanged and untouched,
// because all of it works on Transaction and none of it works on API Elements.
// What is skipped is one intermediate representation that would have carried
// no GraphQL information; what is reused is the whole of the pipe that matters.

// The defaults a GraphQL run starts from.
const (
	// DefaultGraphQLPath is where a GraphQL server almost always listens. It
	// is configurable because "almost always" is not always: `/api/graphql`
	// and `/v1/graphql` are both common.
	DefaultGraphQLPath = "/graphql"

	// DefaultGraphQLDepth is how many levels of selection a generated query
	// nests by default.
	//
	// There has to be a bound at all because a schema with a cycle — and every
	// real schema has one, `User.friends: [User!]!` being the canonical
	// example — otherwise expands forever. Four is chosen rather than two
	// because the Relay connection pattern spends two levels on `edges` and
	// `node` before reaching anything the API is about, and rather than six
	// because a selection set's size grows with the fan-out at every level: on
	// a wide schema the difference between four and six is a query nobody can
	// read and a server that spends a second answering it.
	DefaultGraphQLDepth = 4
)

// GraphQLOptions are the run's decisions about how a schema becomes
// transactions.
type GraphQLOptions struct {
	// Path is where the queries are POSTed, relative to the endpoint the run
	// is aimed at. Empty means DefaultGraphQLPath.
	//
	// It is `Path` rather than `Endpoint` because a run already has an
	// endpoint — the server's base URL — and two settings a letter apart that
	// mean different things are read wrong at a glance and mistyped forever.
	Path string

	// MaxDepth bounds how deeply a selection set nests. Zero means
	// DefaultGraphQLDepth. A field whose expansion is cut by this bound is
	// dropped from the selection entirely — see selectionFor.
	MaxDepth int

	// Mutations includes the mutation root's fields. It is off by default and
	// that is the whole point of it; see CompileGraphQL.
	Mutations bool
}

func (o GraphQLOptions) path() string {
	if o.Path == "" {
		return DefaultGraphQLPath
	}
	// A path written without its leading slash is what somebody plainly meant,
	// and spending a run on saying so would be pedantry.
	if !strings.HasPrefix(o.Path, "/") {
		return "/" + o.Path
	}
	return o.Path
}

func (o GraphQLOptions) maxDepth() int {
	if o.MaxDepth <= 0 {
		return DefaultGraphQLDepth
	}
	return o.MaxDepth
}

// Why an operation the schema declares did not become a transaction.
//
// These are phrased as the whole of the sentence a report prints, because the
// count on its own is the failure mode this repo cares most about: a run that
// tested eleven of a schema's nineteen operations and said only that eleven
// passed is a run that reads as success.
const (
	// WithheldMutation is the safety interlock, and it is the same argument as
	// fuzz.Pins: the destructive thing is as reachable from the description as
	// the harmless one, and nothing in the description marks it. In OpenAPI a
	// caller can at least read `DELETE` in the method; in GraphQL every
	// operation is a POST to one path, and `deleteAccount` is a field of the
	// mutation root exactly as `viewer` is a field of the query root. Sending
	// mutations because they were there is how a contract test empties a
	// staging database, so they are sent only when asked for by name.
	WithheldMutation = "a mutation changes the server's state, so vertrag does not send one unless it is asked to; " +
		"set `graphql: {mutations: true}` in the config, or pass --graphql-mutations, to include them"

	// WithheldArguments is the boundary of this round: a field whose arguments
	// are required cannot be queried without values for them, and vertrag does
	// not generate argument values yet. Withholding it is the honest answer —
	// sending it without its arguments would produce a query the server
	// refuses, and a failure report about vertrag's own query rather than
	// about the API.
	WithheldArguments = "it requires arguments, and this version of vertrag does not generate argument values"

	// WithheldNothingSelectable covers the two ways an otherwise fine field
	// yields no query: everything under it needs arguments, or everything
	// under it lies deeper than the depth bound.
	WithheldNothingSelectable = "nothing could be selected from it: the fields of its type either require arguments " +
		"or lie deeper than the depth bound"

	// WithheldUnknownType is a schema that refers to a type it does not
	// declare. It is the schema's problem rather than the run's, and it is
	// also reported as an annotation.
	WithheldUnknownType = "its type is not declared in the schema"

	// WithheldSubscription is not a setting and never will be: a subscription
	// is a stream over a websocket or server-sent events, not a request and a
	// response, so there is nothing for a transaction to be. It is reported
	// rather than passed over in silence because a schema's subscriptions are
	// part of what it offers, and a run that covered none of them without
	// saying so reads as a run that covered the schema.
	WithheldSubscription = "a subscription is a stream rather than a request and a response, " +
		"so vertrag has no transaction to send it as"
)

// GraphQLResult is what a schema compiled to.
//
// It is a Result — the same value the API Elements path produces, so the
// command emits and runs it identically — plus what was left out, which has no
// equivalent on the other path because no OpenAPI operation is ever withheld.
type GraphQLResult struct {
	Result

	// Withheld names the schema's operations that did not become
	// transactions, and why.
	Withheld []GraphQLWithheld

	// Trimmed counts the fields left out of selection sets below the top
	// level, for the same reason Withheld exists: a query that quietly stops
	// asking for half of a type still passes.
	Trimmed int

	// Depth is the bound the selections were built to, so a report explaining
	// what was trimmed can say what to change.
	Depth int
}

// GraphQLWithheld is one operation that was not turned into a transaction.
type GraphQLWithheld struct {
	// Name is the transaction name the operation would have had, so that
	// enabling it and then addressing it with `--only` needs no translation.
	Name   string
	Reason string
}

// Notes describes what was left out, one line per reason.
//
// Grouped by reason rather than listed per operation because the reason is
// what the reader acts on — one line saying "seven mutations, here is the
// switch" is read, and seven lines each carrying the same paragraph are not.
func (r GraphQLResult) Notes() []string {
	byReason := map[string][]string{}
	var order []string
	for _, withheld := range r.Withheld {
		if _, seen := byReason[withheld.Reason]; !seen {
			order = append(order, withheld.Reason)
		}
		byReason[withheld.Reason] = append(byReason[withheld.Reason], withheld.Name)
	}

	var notes []string
	for _, reason := range order {
		names := byReason[reason]
		subject := fmt.Sprintf("%d of the schema's operations were", len(names))
		if len(names) == 1 {
			subject = "one of the schema's operations was"
		}
		notes = append(notes, fmt.Sprintf("%s not sent — %s: %s",
			subject, reason, strings.Join(names, ", ")))
	}
	if r.Trimmed > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d field(s) were left out of the selection sets below the top level: they need arguments, "+
				"or lie deeper than the depth bound of %d (raise `graphql: {max-depth: N}` to reach further)",
			r.Trimmed, r.Depth))
	}
	return notes
}

// CompileGraphQL turns a schema into the transactions to test.
//
// One transaction per field of the query root, and one per field of the
// mutation root when the run asked for mutations. Fields, not operations the
// caller wrote: a schema is a description of what can be asked, and the
// smallest honest unit of it is one root field with everything reachable from
// it that can be selected.
func CompileGraphQL(schema *graphql.Schema, options GraphQLOptions, filename string) GraphQLResult {
	result := GraphQLResult{
		Result: Result{
			MediaType:    MediaTypeGraphQL,
			Transactions: []Transaction{},
			Annotations:  []Annotation{},
		},
		Depth: options.maxDepth(),
	}
	if schema == nil {
		result.Annotations = append(result.Annotations, Annotation{
			Type: "error", Component: "apiDescription",
			Message: "the GraphQL schema is empty, so there is nothing to test",
		})
		return result
	}

	builder := &graphqlBuilder{schema: schema, seen: map[string]bool{}}

	// Queries first, then mutations, then subscriptions, each in the schema's
	// own field order, so two runs of the same schema produce the same report
	// and a diff between them is about the API rather than about map
	// iteration.
	//
	// `withheld` is the reason this root's fields are not sent, empty when
	// they are. Subscriptions always carry one: a subscription is a stream
	// over a websocket or server-sent events rather than a request and a
	// response, so there is nothing for a transaction to be.
	for _, root := range []struct {
		operation string
		typeName  string
		withheld  string
	}{
		{graphqlQuery, schema.QueryType, ""},
		{graphqlMutation, schema.MutationType, mutationReason(options)},
		{graphqlSubscription, schema.SubscriptionType, WithheldSubscription},
	} {
		declared, ok := schema.Types[root.typeName]
		if !ok {
			// A schema with no mutation root, or none named, is ordinary and
			// says nothing. A schema with no QUERY root describes nothing
			// that can be asked for, so the run stops rather than reporting
			// an API with no operations in it — which is indistinguishable
			// from a description vertrag simply failed to read.
			//
			// The reader says the same thing as a warning, at more length and
			// about the document. This is the half that decides the run.
			if root.operation == graphqlQuery {
				result.Annotations = append(result.Annotations, Annotation{
					Type: "error", Component: "apiDescription",
					Message: "this schema has no query root type, so there is nothing to test",
				})
			}
			continue
		}

		for _, field := range declared.Fields {
			name := graphqlTransactionName(root.operation, field.Name)

			// The mutation gate is here, before the query is even built,
			// rather than in a filter the caller applies afterwards. A
			// safety interlock that a later stage can forget to apply is one
			// that will eventually be forgotten — the same reasoning that put
			// fuzz's pins between the draw and the wire.
			if root.withheld != "" {
				result.Withheld = append(result.Withheld,
					GraphQLWithheld{Name: name, Reason: root.withheld})
				continue
			}

			node, reason := builder.field(field, options.maxDepth())
			if reason != "" {
				result.Withheld = append(result.Withheld, GraphQLWithheld{Name: name, Reason: reason})
				continue
			}
			result.Transactions = append(result.Transactions,
				graphqlTransaction(root.operation, field, node, options, filename))
		}
	}

	result.Annotations = append(result.Annotations, builder.annotations...)
	result.Trimmed = builder.trimmed
	return result
}

// The operation names, which are both GraphQL keywords and the first half of
// every transaction name.
const (
	graphqlQuery        = "Query"
	graphqlMutation     = "Mutation"
	graphqlSubscription = "Subscription"
)

// mutationReason is the interlock in one place: mutations are withheld unless
// the run asked for them.
func mutationReason(options GraphQLOptions) string {
	if options.Mutations {
		return ""
	}
	return WithheldMutation
}

// MediaTypeGraphQL is what a compiled GraphQL schema reports as its media
// type. It is spelled out here as well as in apidesc for the same reason
// MediaTypeAPIBlueprint is: this package branches on media types, and
// importing the stage that detects them to read one string would point the
// dependency the wrong way round.
const MediaTypeGraphQL = "application/graphql"

// GraphQL is what a GraphQL response to a transaction must satisfy.
//
// It travels on the transaction because nothing downstream can work it out for
// itself: every GraphQL request is a POST of an opaque string to one path, so
// a runner holding only the request cannot tell which fields were asked for,
// which of them the schema said could never be null, or even that this was a
// GraphQL exchange at all.
type GraphQL struct {
	// Operation is "query" or "mutation", for a report to say which.
	Operation string

	// Field is the root field this transaction exercises.
	Field string

	// Data is the shape `data` must have — one entry, the root field, with the
	// selection under it.
	Data []GraphQLSelection
}

// GraphQLSelection is one key the query asked for, and what the schema said
// about the value behind it.
type GraphQLSelection struct {
	// Key is the name the value arrives under.
	Key string

	// Type is how the schema writes the field's type, e.g. "[User!]!". It is
	// carried for the finding to quote: "null, but the schema declares it
	// String!" is actionable where "null" is not.
	Type string

	// NonNull is the schema's promise that this value is never null.
	NonNull bool

	// ListDepth is how many list wrappers the value has, and ElementNonNull
	// whether the innermost elements may be null.
	ListDepth      int
	ElementNonNull bool

	// Conditional marks a key that came from an inline fragment, so its
	// absence is not a finding: it is there only when the object turned out to
	// be that concrete type.
	Conditional bool

	// Fields is the selection asked for inside this one.
	Fields []GraphQLSelection
}

// graphqlTransactionName is the name hooks and `--only` address the
// transaction by: "Query > user", "Mutation > createUser".
//
// It is built from the OPERATION KIND and the field, not from the root type's
// name and not from the endpoint path. Both of those are things a schema can
// change without changing what any operation does — someone renames `Query` to
// `RootQuery`, or moves the server to `/api/graphql` — and a name that moved
// then would silently break every hook and every `--only` in a project that
// had nothing to do with the change.
func graphqlTransactionName(operation, field string) string {
	return compileTransactionName(graphqlOrigin("", operation, field))
}

func graphqlOrigin(filename, operation, field string) Origin {
	return Origin{
		Filename: filename,
		// A GraphQL schema has no title, so there is no API name to strip and
		// no example to distinguish: one root field is one exchange, and the
		// name is the whole of what identifies it.
		ResourceName: operation,
		ActionName:   field,
	}
}

// graphqlTransaction assembles the request and the expectation for one root
// field.
func graphqlTransaction(
	operation string,
	field graphql.Field,
	node graphqlNode,
	options GraphQLOptions,
	filename string,
) Transaction {
	document := graphqlDocument(operation, field.Name, node)

	// The body is built by the JSON encoder rather than by string
	// concatenation, because the query contains newlines and quotes and a
	// hand-assembled body would be invalid for exactly the schemas whose
	// selection sets are worth reading.
	//
	// `variables` is present and empty. Nothing fills it yet — this round
	// sends no argument values — but it is where generated arguments will go,
	// and a server that rejects an empty variables object rejects a legal
	// request.
	payload, _ := json.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}{Query: document, Variables: map[string]any{}})

	origin := graphqlOrigin(filename, operation, field.Name)
	return Transaction{
		Request: Request{
			Method: "POST",
			URI:    options.path(),
			Headers: []Header{
				{Name: "Content-Type", Value: "application/json"},
				{Name: "Accept", Value: "application/json"},
			},
			Body: string(payload),
			// No request body schema, deliberately. It is what the fuzz and
			// coverage phases generate bodies from, and a schema describing
			// `{query, variables}` would have them generating GraphQL
			// documents at random — every one of which the server rejects as
			// a syntax error, which says nothing about the API. Generating
			// ARGUMENT values is the useful version of that idea and is the
			// next round's job.
		},
		Response: Response{
			// 200 and nothing else. A GraphQL endpoint answers 200 to very
			// nearly everything, errors included: a query naming a field that
			// does not exist, a resolver that panicked, and a perfectly served
			// request are all 200 with different bodies. So the status is
			// worth stating — a 500 or a 404 here is still a real finding, and
			// the server-error check still fires — but it proves almost
			// nothing on its own, and a run that checked only the status would
			// pass against a server that answered every query with an error.
			// The body check in runner/graphql.go is where the judgement is.
			Status:  "200",
			Headers: []Header{},
			// No Content-Type expectation. Both `application/json` and
			// `application/graphql-response+json` are correct — the GraphQL
			// over HTTP specification names the second as the one new servers
			// should use — so demanding either would fail servers for being
			// right. What the response must actually be is a JSON document of
			// a particular shape, and that is checked directly.
		},
		Name:   graphqlTransactionName(operation, field.Name),
		Origin: origin,
		// Tags mirror the operation kind so a run that has enabled mutations
		// can still narrow itself with `--exclude-tag mutation`. They do not
		// open the gate: `--tag mutation` selects among the transactions that
		// were built, and a withheld mutation was never built.
		Tags:    []string{strings.ToLower(operation)},
		GraphQL: graphqlExpectation(operation, field.Name, node),
	}
}

// graphqlExpectation is the shape the response's `data` must have, derived
// from the selection set that was asked for rather than from the schema at
// large: what the server owes is an answer to the question that was put.
func graphqlExpectation(operation, field string, node graphqlNode) *GraphQL {
	return &GraphQL{
		Operation: strings.ToLower(operation),
		Field:     field,
		Data:      graphqlShape([]graphqlNode{node}, false),
	}
}

// graphqlBuilder walks the schema building selection sets.
type graphqlBuilder struct {
	schema *graphql.Schema

	// annotations collects diagnostics about the schema itself.
	annotations []Annotation
	// seen deduplicates them: one broken type reached from four fields is one
	// problem in the document, and saying it four times reads as four.
	seen map[string]bool

	// trimmed counts the fields left out of nested selection sets.
	trimmed int
}

// graphqlNode is one entry of a selection set: a field, or an inline fragment
// over one concrete type.
type graphqlNode struct {
	// field is the field's name; empty for an inline fragment.
	field string
	// condition is the type an inline fragment applies to.
	condition string

	// The field's type, as the schema writes it and as the response check
	// needs it: the rendering is for the message, the rest is the judgement.
	typeText       string
	nonNull        bool
	listDepth      int
	elementNonNull bool

	children []graphqlNode
}

// field builds the selection for one field, or returns the reason it cannot be
// selected.
//
// budget is how many levels of selection this field may still use: a field
// that needs a sub-selection spends one and its children get the rest.
func (b *graphqlBuilder) field(field graphql.Field, budget int) (graphqlNode, string) {
	if graphqlRequiresArguments(field) {
		return graphqlNode{}, WithheldArguments
	}

	target, listDepth, ok := b.schema.Resolve(field.Type)
	if !ok {
		b.annotate(fmt.Sprintf(
			"the schema uses the type %q, which it does not declare, so the field %q cannot be queried",
			graphqlNamedType(field.Type), field.Name))
		return graphqlNode{}, WithheldUnknownType
	}

	node := graphqlNode{
		field: field.Name,
		// The schema's own rendering, for a finding to quote: "[User!]!".
		typeText:       field.Type.String(),
		nonNull:        field.Type.NonNull,
		listDepth:      listDepth,
		elementNonNull: graphqlElementNonNull(field.Type),
	}

	// A scalar or an enum is a leaf: it has no fields, and asking for a
	// sub-selection on one is a syntax error in the other direction.
	if target.Kind == graphql.KindScalar || target.Kind == graphql.KindEnum {
		return node, ""
	}

	// Everything else is composite and MUST carry a sub-selection. That is why
	// a field cut by the depth bound is dropped rather than emitted bare: `{
	// user }` where user is an object is not a thinner query, it is a document
	// the server refuses to parse, and it takes the whole request with it —
	// including the fields that were expandable.
	if budget < 1 {
		return graphqlNode{}, WithheldNothingSelectable
	}

	node.children = b.selectionSet(target, budget-1)
	if len(node.children) == 0 {
		return graphqlNode{}, WithheldNothingSelectable
	}
	return node, ""
}

// selectionSet builds the entries inside one pair of braces.
func (b *graphqlBuilder) selectionSet(target *graphql.Type, budget int) []graphqlNode {
	switch target.Kind {
	case graphql.KindObject:
		return b.fields(target.Fields, budget)

	case graphql.KindInterface:
		// An interface's own fields can be selected directly, which is the
		// readable and cheap half. The inline fragments then add only what
		// each concrete type declares BEYOND the interface — repeating the
		// common fields inside every fragment would multiply the query by the
		// number of implementations and ask for the same values twice.
		nodes := []graphqlNode{graphqlTypename()}
		nodes = append(nodes, b.fields(target.Fields, budget)...)
		return append(nodes, b.fragments(target, graphqlKeys(nodes), budget)...)

	case graphql.KindUnion:
		// A union has no fields of its own, so a selection on one that carries
		// no inline fragments is a syntax error. __typename is always valid on
		// a union and is what keeps the selection non-empty when every member
		// turns out to be unexpandable.
		nodes := []graphqlNode{graphqlTypename()}
		return append(nodes, b.fragments(target, nil, budget)...)
	}

	// Input objects reach here only from a malformed schema: they are argument
	// types and cannot be a field's result.
	return nil
}

// fields keeps the fields that can be selected and counts the rest.
func (b *graphqlBuilder) fields(fields []graphql.Field, budget int) []graphqlNode {
	var nodes []graphqlNode
	for _, field := range fields {
		node, reason := b.field(field, budget)
		if reason != "" {
			b.trimmed++
			continue
		}
		nodes = append(nodes, node)
	}
	return nodes
}

// fragments builds one inline fragment per concrete type, skipping the fields
// the caller has already selected on the abstract type.
func (b *graphqlBuilder) fragments(target *graphql.Type, already map[string]bool, budget int) []graphqlNode {
	var nodes []graphqlNode
	for _, name := range target.Possible {
		concrete, ok := b.schema.Types[name]
		if !ok {
			b.annotate(fmt.Sprintf(
				"the schema says %q can be a %q, which it does not declare, so that case cannot be queried",
				target.Name, name))
			continue
		}

		var extra []graphql.Field
		for _, field := range concrete.Fields {
			if already[field.Name] {
				continue
			}
			extra = append(extra, field)
		}

		children := b.fields(extra, budget)
		if len(children) == 0 {
			// An empty fragment body is a syntax error too, so a concrete type
			// that adds nothing selectable gets no fragment rather than an
			// empty one.
			continue
		}
		nodes = append(nodes, graphqlNode{condition: name, children: children})
	}
	return nodes
}

func (b *graphqlBuilder) annotate(message string) {
	if b.seen[message] {
		return
	}
	b.seen[message] = true
	b.annotations = append(b.annotations, Annotation{
		Type: "warning", Component: "apiDescription", Message: message,
	})
}

// graphqlTypename is the meta field every composite type answers.
//
// It is asked for on interfaces and unions because it is what says which case
// came back — and, incidentally, because it guarantees such a selection is
// never empty.
func graphqlTypename() graphqlNode {
	return graphqlNode{field: "__typename", typeText: "String!", nonNull: true}
}

func graphqlKeys(nodes []graphqlNode) map[string]bool {
	keys := map[string]bool{}
	for _, node := range nodes {
		if node.field != "" {
			keys[node.field] = true
		}
	}
	return keys
}

// graphqlRequiresArguments reports whether a field cannot be asked for without
// argument values.
//
// Non-null AND no default: the specification makes an argument required only
// when both hold, so a `first: Int! = 10` is satisfied by leaving it out and
// the server applies the default.
func graphqlRequiresArguments(field graphql.Field) bool {
	for _, argument := range field.Args {
		if argument.Type.NonNull && !argument.HasDefault {
			return true
		}
	}
	return false
}

// graphqlNamedType is the name at the bottom of a reference's wrappers.
func graphqlNamedType(ref graphql.TypeRef) string {
	for ref.List != nil {
		ref = *ref.List
	}
	return ref.Named
}

// graphqlElementNonNull reports whether the innermost element of a list may be
// null. For `[User!]!` that is true and for `[User]!` it is false — a
// distinction the schema makes and a server routinely gets wrong.
func graphqlElementNonNull(ref graphql.TypeRef) bool {
	if ref.List == nil {
		return false
	}
	for ref.List != nil {
		ref = *ref.List
	}
	return ref.NonNull
}

// graphqlDocument renders the query document.
//
// The operation is named after the root field. An anonymous operation would be
// just as valid, but the name is what a server's logs, its APM traces and its
// persisted-query allow-lists key on, so a failing transaction can be found on
// the other side by the name vertrag gave it.
func graphqlDocument(operation, field string, node graphqlNode) string {
	var out strings.Builder
	out.WriteString(strings.ToLower(operation))
	out.WriteString(" ")
	out.WriteString(field)
	out.WriteString(" {\n")
	graphqlRender(&out, []graphqlNode{node}, 1)
	out.WriteString("}")
	return out.String()
}

func graphqlRender(out *strings.Builder, nodes []graphqlNode, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, node := range nodes {
		out.WriteString(indent)
		if node.field != "" {
			out.WriteString(node.field)
		} else {
			out.WriteString("... on ")
			out.WriteString(node.condition)
		}
		if len(node.children) == 0 {
			out.WriteString("\n")
			continue
		}
		out.WriteString(" {\n")
		graphqlRender(out, node.children, depth+1)
		out.WriteString(indent)
		out.WriteString("}\n")
	}
}

// graphqlShape turns the selection into the expectation the response is judged
// against.
//
// A fragment's fields become conditional entries of the enclosing selection,
// because that is how they arrive: `... on Dog { barks }` puts `barks` next to
// the interface's own fields when the object is a Dog and leaves it out
// entirely when it is a Cat. Requiring it would fail every response that
// happened to return the other implementation.
func graphqlShape(nodes []graphqlNode, conditional bool) []GraphQLSelection {
	var out []GraphQLSelection
	for _, node := range nodes {
		if node.field == "" {
			out = append(out, graphqlShape(node.children, true)...)
			continue
		}
		out = append(out, GraphQLSelection{
			Key:            node.field,
			Type:           node.typeText,
			NonNull:        node.nonNull,
			ListDepth:      node.listDepth,
			ElementNonNull: node.elementNonNull,
			Conditional:    conditional,
			Fields:         graphqlShape(node.children, conditional),
		})
	}
	return out
}
