package compile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/antimatter-studios/vertrag/apidesc/graphql"
)

// GraphQLArgument is one argument the query passes, and the variable carrying
// its value.
//
// Everything is passed as a VARIABLE rather than written into the query text,
// and that decision is what makes the probing phases possible at all. A value
// inlined in the document could only be replaced by editing GraphQL source —
// re-quoting strings, remembering that an enum member is unquoted and that an
// Int is not a Float — and every one of those is a way to send something the
// schema never described and then judge the server on it. A variable moves the
// whole question into JSON, where a value and its bytes are the same thing, and
// the server coerces it against the very type the schema was built from.
type GraphQLArgument struct {
	// Variable is the name the value travels under in `variables`, without the
	// leading `$`. It is the argument's own name where that is free, and
	// numbered where two fields of one document declare the same one.
	Variable string

	// Name is the argument as the schema declares it. It is what `fuzz.pin`
	// names, so it is the argument's own name and never the variable's.
	Name string

	// Field is where the argument sits, as a path from the root field:
	// "userById", or "viewer.posts". A finding quotes it, because "argument
	// `first`" on a schema with nine paginated fields names nothing.
	Field string

	// Type is how the schema writes the argument's type — "ID!" — for a
	// finding to quote.
	Type string

	// Schema is the JSON Schema a value must satisfy, built from Type. See
	// graphqlvalues.go for the mapping and for why it is the argument's type
	// rather than the request body's shape.
	Schema string

	// Possessed marks an argument whose value must NAME something that already
	// exists rather than merely being well formed.
	//
	// It is the GraphQL spelling of the exemption a path parameter's 404
	// already gets, and of the one the login operation gets: generation can
	// produce anything the caller must SHAPE and nothing they must POSSESS. An
	// `ID` is the clearest case there is — every legal id is well formed, and
	// which ones exist is the server's data rather than its contract — so a
	// generated one resolving to nothing is not a finding.
	Possessed bool

	// Value is what the compiled request carries, and HasValue separates an
	// argument absent from `variables` from one whose value is null. The
	// difference is not cosmetic: GraphQL reads an undefined variable as the
	// argument not having been written at all, so the server applies its
	// default, where an explicit null overrides it.
	Value    any
	HasValue bool

	// declaration is the variable definition as the operation writes it,
	// "$id: ID!". It is unexported because only this package writes one: it is
	// query text, and the reason everything else here is not.
	declaration string
}

// Describe names the argument the way a sentence about it would.
func (a GraphQLArgument) Describe() string {
	if a.Field == "" {
		return fmt.Sprintf("argument %q", a.Name)
	}
	return fmt.Sprintf("argument %q of %s", a.Name, a.Field)
}

// SetGraphQLArgument returns a copy of the request carrying a different value
// for one argument.
//
// The body is decoded, one entry of `variables` replaced, and the body encoded
// again — the query text is not touched, and that is the point. The generated
// request then differs from the compiled one in exactly one argument, which is
// the property that makes a finding attributable at all; a substitution done in
// the document would differ in the document too.
//
// Numbers are decoded through json.Number so a value survives the round trip.
// Re-encoding an Int argument of 2147483647 as a float64 hands the server
// 2.147483647e+09, which it rejects — a finding about the encoder rather than
// about the API, and one that would appear only at the boundary the coverage
// phase exists to send.
func (r Request) SetGraphQLArgument(argument GraphQLArgument, value any) (Request, error) {
	if argument.Variable == "" {
		return r, fmt.Errorf("the argument %q carries no variable to substitute into", argument.Name)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
		return r, fmt.Errorf("re-reading the GraphQL request body: %w", err)
	}

	variables := map[string]any{}
	if raw, present := body["variables"]; present && len(raw) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&variables); err != nil {
			return r, fmt.Errorf("re-reading the GraphQL variables: %w", err)
		}
	}
	variables[argument.Variable] = value

	encoded, err := json.Marshal(variables)
	if err != nil {
		return r, fmt.Errorf("encoding the GraphQL variables: %w", err)
	}
	body["variables"] = encoded

	rendered, err := json.Marshal(body)
	if err != nil {
		return r, fmt.Errorf("encoding the GraphQL request body: %w", err)
	}
	r.Body = string(rendered)

	// The argument list is updated too, so that a request rebuilt from it — by
	// a reporter, or by the pin holding a second argument — sees the value that
	// is really on the wire rather than the compiled example.
	updated := make([]GraphQLArgument, len(r.GraphQLArguments))
	copy(updated, r.GraphQLArguments)
	for i := range updated {
		if updated[i].Variable == argument.Variable {
			updated[i].Value = value
			updated[i].HasValue = true
		}
	}
	r.GraphQLArguments = updated
	return r, nil
}

// arguments builds the arguments one field will be asked with, or reports that
// the field cannot be asked for at all.
//
// The second return is false when an argument the field REQUIRES has a type no
// value can be built for. There is then no query to send: leaving the argument
// out is a document the server refuses, and inventing a value for a type whose
// values are defined outside the schema is a guess.
//
// Which arguments are declared depends on where the field sits, and the line is
// drawn deliberately. The ROOT field gets every argument it declares, because
// the root field is the operation under test and its optional arguments are as
// much of its input surface as its required ones. A field further down gets
// only the arguments it cannot be selected without: it is there so the root
// field has something to return, and declaring a variable for every `first`,
// `after` and `filter` in a four-level selection would put dozens of them on
// one query for inputs nobody asked about.
//
// Optional arguments are declared and left OUT of `variables`. That is not an
// oversight either: an undefined variable makes GraphQL treat the argument as
// not written at all, so the server applies its own default — while an explicit
// null would override it. Declaring them costs nothing, changes no request the
// examples phase sends, and is what lets a probing phase fill one in without
// rewriting the query.
func (b *graphqlBuilder) arguments(field graphql.Field, path string, root bool) ([]GraphQLArgument, bool) {
	var out []GraphQLArgument
	for _, argument := range field.Args {
		// The specification makes an argument required only when it is non-null
		// AND carries no default, so `first: Int! = 10` is satisfied by leaving
		// it out and letting the server apply the default.
		required := argument.Type.NonNull && !argument.HasDefault
		if !required && !root {
			continue
		}

		values := newGraphQLValues(b.schema)
		schema, example, ok := values.build(argument.Type)
		for _, refusal := range values.refused {
			b.annotate(fmt.Sprintf("%s; the argument %s(%s:) is therefore not sent",
				refusal, field.Name, argument.Name))
		}
		if !ok {
			if required {
				return nil, false
			}
			continue
		}

		encoded, err := json.Marshal(schema)
		if err != nil {
			if required {
				return nil, false
			}
			continue
		}

		built := GraphQLArgument{
			Name:      argument.Name,
			Field:     path,
			Type:      argument.Type.String(),
			Schema:    string(encoded),
			Possessed: values.possessed,
		}
		if required {
			built.Value = example
			built.HasValue = true
		}
		built.declaration = graphqlDeclaration(argument, required)
		out = append(out, built)
	}
	return out, true
}

// graphqlDeclaration writes the variable definition for one argument.
//
// A non-null argument carrying a default is the case that forces the shape of
// this. Its variable cannot be declared bare and then left out of `variables`:
// the specification makes an undefined non-null variable a coercion error, so
// the request would fail before the server looked at anything. Repeating the
// argument's own default ON THE VARIABLE is what the specification provides for
// exactly this, and it keeps the argument optional in the only sense that
// matters — the examples phase sends nothing for it and the server uses the
// default it documented.
//
// A default that cannot be written back as source text falls back to a bare
// declaration and a supplied value. That is worse — it sends a value where the
// schema meant a default — but it is a document the server accepts, which the
// alternative is not.
func graphqlDeclaration(argument graphql.Argument, required bool) string {
	declaration := "$" + argument.Name + ": " + argument.Type.String()
	if required || !argument.HasDefault || !argument.Type.NonNull {
		return declaration
	}
	literal, ok := graphqlLiteral(argument.Default)
	if !ok {
		return declaration
	}
	return declaration + " = " + literal
}

// graphqlBind gives every argument in a finished selection the variable name it
// travels under, and collects them in the order the document writes them.
//
// Names are assigned after the tree is complete rather than while it is built,
// because a field can still be dropped after its arguments are known — cut by
// the depth bound, or found to have nothing selectable under it — and a
// document declaring a variable no field uses is one the server refuses to
// validate. What survives the walk is exactly what the document mentions.
func graphqlBind(node *graphqlNode, taken map[string]bool, collected *[]GraphQLArgument) {
	for i := range node.arguments {
		name := node.arguments[i].Name
		// Two fields of one document may declare the same argument name —
		// `first` on every paginated field is the ordinary case — and one
		// variable holding both values would silently tie them together.
		for suffix := 2; taken[name]; suffix++ {
			name = fmt.Sprintf("%s%d", node.arguments[i].Name, suffix)
		}
		taken[name] = true

		node.arguments[i].Variable = name
		node.arguments[i].declaration = "$" + name +
			strings.TrimPrefix(node.arguments[i].declaration, "$"+node.arguments[i].Name)
		*collected = append(*collected, node.arguments[i])
	}
	for i := range node.children {
		graphqlBind(&node.children[i], taken, collected)
	}
}

// graphqlVariableValues is what `variables` carries: the arguments a value was
// built for, and nothing else. See GraphQLArgument.HasValue.
func graphqlVariableValues(arguments []GraphQLArgument) map[string]any {
	values := map[string]any{}
	for _, argument := range arguments {
		if argument.HasValue {
			values[argument.Variable] = argument.Value
		}
	}
	return values
}

// graphqlPossessed names the arguments that were given a generated value which
// must name something that already exists. See GraphQL.Possessed.
func graphqlPossessed(arguments []GraphQLArgument) []string {
	var out []string
	for _, argument := range arguments {
		if argument.Possessed && argument.HasValue {
			out = append(out, argument.Describe())
		}
	}
	return out
}

// graphqlDeclarations renders the operation's variable definitions, "($id: ID!,
// $first: Int! = 10)", or nothing when the operation takes none.
func graphqlDeclarations(arguments []GraphQLArgument) string {
	if len(arguments) == 0 {
		return ""
	}
	parts := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		parts = append(parts, argument.declaration)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// graphqlArgumentList renders the arguments as one field passes them,
// "(id: $id)".
func graphqlArgumentList(arguments []GraphQLArgument) string {
	if len(arguments) == 0 {
		return ""
	}
	parts := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		parts = append(parts, argument.Name+": $"+argument.Variable)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}
