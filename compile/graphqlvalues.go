package compile

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/antimatter-studios/vertrag/apidesc/graphql"
)

// An argument value is generated from the argument's OWN type, and that is the
// whole of the design here.
//
// Every other format vertrag probes hands generation a JSON Schema of the
// request body, so there is something to draw from. A GraphQL request body is
// `{query, variables}` — a document and a bag — and a JSON Schema of THAT shape
// would set the generator to writing GraphQL documents at random, every one of
// which the server refuses as a syntax error: a run full of findings about
// vertrag's own queries and none about the API. What a GraphQL schema does
// describe, exactly and per value, is the type of each argument. So each
// argument becomes one JSON Schema, the value travels in `variables`, and the
// server coerces it against the very type the schema was built from.
//
// The mapping is deliberately faithful rather than convenient, because a
// generous schema produces false findings in both directions — a value the
// server rightly refuses is reported as a disagreement, and a value it should
// have refused is reported as nothing at all:
//
//   - Int is a 32-BIT signed integer by specification, so the bounds are
//     stated. They are not decoration: they are what makes the coverage phase
//     send 2147483647 and 2147483648, which is exactly where a server backed by
//     a 64-bit column and no validation gets it wrong.
//   - Float is `number`, which admits integers, because GraphQL's Float accepts
//     an Int input value. The reverse does not hold, and that asymmetry is why
//     the two are kept apart at all: a server rejects 1.0 where an Int belongs.
//   - ID accepts a string OR an integer, again by specification. Typing it as a
//     string alone would have the invalid mode draw 12345 as a "wrong type" and
//     report a validation bypass against a server doing precisely what the
//     specification tells it to.
//   - A nullable position admits null, so `null` is in the type list. `[User]`
//     and `[User!]` differ in exactly that, and a server that rejects a null
//     element of a `[User]` list is a finding worth having.
//
// A custom scalar is the one thing here that cannot be mapped. Its value space
// is defined outside the schema — that is what makes it custom — so anything
// generated for one is a guess, and a server's rejection of a guess is a
// finding about vertrag. Those arguments are refused rather than guessed at,
// and the field that requires one is withheld and named.

// The bounds of GraphQL's Int, which is a signed 32-bit integer and not the
// language's own int.
const (
	graphqlIntMinimum = -2147483648
	graphqlIntMaximum = 2147483647
)

// graphqlValues maps a type reference to the JSON Schema a value must satisfy
// and to one concrete value that satisfies it.
type graphqlValues struct {
	schema *graphql.Schema

	// visiting counts how often each input-object type appears on the current
	// path.
	//
	// Input objects refer to one another — `input Filter { and: [Filter!] }` is
	// the ordinary way to write a boolean filter — and a walk that did not
	// notice would expand forever. Stopping at the bound is always safe: the
	// repeat is reached through a NULLABLE field, because the specification
	// forbids an input object to require itself, so dropping the field there
	// leaves a value the server still accepts.
	visiting map[string]int

	// possessed records that an ID was reached while building the value in
	// hand. See GraphQLArgument.Possessed.
	possessed bool

	// refused explains, in the words a report would use, why a type could not
	// be produced. It is the schema's business rather than the run's, so it
	// becomes an annotation naming the type once.
	refused []string
}

// graphqlInputRepeats is how often one input-object type may appear on the path
// from an argument down into its own value.
//
// Two rather than one, so a self-referential input is exercised through its
// recursion rather than only at the top: `{term: "x", and: [{term: "y"}]}` is
// what reaches the server's filter compiler, and `{term: "x"}` is not. Two
// rather than more because the size of a generated value grows with the type's
// fan-out at every repeat, and nothing a server does wrong at three levels it
// does not already do at two.
const graphqlInputRepeats = 2

func newGraphQLValues(schema *graphql.Schema) *graphqlValues {
	return &graphqlValues{schema: schema, visiting: map[string]int{}}
}

// build returns the JSON Schema for one type reference, a value satisfying it,
// and whether the type could be produced at all.
func (v *graphqlValues) build(ref graphql.TypeRef) (map[string]any, any, bool) {
	if ref.List != nil {
		items, element, ok := v.build(*ref.List)
		if !ok {
			return nil, nil, false
		}
		schema := map[string]any{
			"type":  graphqlJSONType("array", ref.NonNull),
			"items": items,
		}
		// One element rather than none. An empty list satisfies the type and
		// exercises nothing: the element type is where the server's coercion
		// lives, and a list that never carries one never asks about it.
		return schema, []any{element}, true
	}

	named, declared := v.schema.Types[ref.Named]
	if !declared {
		// Reported by the caller as WithheldUnknownType, which already says
		// this about the field's own type; saying it again here would be the
		// same document fault counted twice.
		return nil, nil, false
	}
	if strings.HasPrefix(named.Name, "__") {
		// Introspection's own types. They describe the schema rather than the
		// API, and a request built to interrogate them tests the server's
		// introspection support and nothing a caller of this API would ever do.
		v.refuse(fmt.Sprintf(
			"the type %q is one of GraphQL's introspection types, so vertrag does not build values of it",
			named.Name))
		return nil, nil, false
	}

	switch named.Kind {
	case graphql.KindScalar:
		return v.scalar(named, ref.NonNull)
	case graphql.KindEnum:
		return v.enum(named, ref.NonNull)
	case graphql.KindInputObject:
		return v.input(named, ref.NonNull)
	}

	// An object, interface or union in an argument position is a schema that
	// does not typecheck: only input types can be arguments.
	v.refuse(fmt.Sprintf(
		"the type %q is %s, which cannot be an argument's type, so no value can be built for it",
		named.Name, named.Kind))
	return nil, nil, false
}

func (v *graphqlValues) scalar(named *graphql.Type, nonNull bool) (map[string]any, any, bool) {
	switch named.Name {
	case "Int":
		return map[string]any{
			"type":    graphqlJSONType("integer", nonNull),
			"minimum": graphqlIntMinimum,
			"maximum": graphqlIntMaximum,
		}, int64(1), true

	case "Float":
		return map[string]any{"type": graphqlJSONType("number", nonNull)}, 1.5, true

	case "String":
		return map[string]any{"type": graphqlJSONType("string", nonNull)}, "vertrag", true

	case "Boolean":
		// false rather than true, because a Boolean argument that does
		// something is far more often the one that turns something ON —
		// `force`, `confirm`, `sendEmail` — and whatever this picks is sent on
		// every run of the examples phase.
		return map[string]any{"type": graphqlJSONType("boolean", nonNull)}, false, true

	case "ID":
		types := []any{"string", "integer"}
		if !nonNull {
			types = append(types, "null")
		}
		v.possessed = true
		// "1" rather than something unmistakably invented: an id is a value the
		// caller must POSSESS, so whatever is sent probably names nothing — and
		// on a server whose fixtures start at 1, this one occasionally does,
		// which turns an exempted probe into a real end-to-end check.
		return map[string]any{"type": types}, "1", true
	}

	specified := ""
	if named.SpecifiedBy != "" {
		specified = ", whose @specifiedBy points at " + named.SpecifiedBy
	}
	v.refuse(fmt.Sprintf(
		"the custom scalar %q%s has no value space this can generate from — that is what makes it custom — "+
			"so a generated value would be a guess, and a server's rejection of a guess is a finding about "+
			"vertrag rather than about the API",
		named.Name, specified))
	return nil, nil, false
}

func (v *graphqlValues) enum(named *graphql.Type, nonNull bool) (map[string]any, any, bool) {
	// A deprecated member is still a legal value, so it is dropped only while
	// something else remains: an enum every one of whose members is deprecated
	// still has to be sent as one of them.
	deprecated := map[string]bool{}
	for _, name := range named.DeprecatedEnumValues {
		deprecated[name] = true
	}
	var preferred []string
	for _, value := range named.EnumValues {
		if !deprecated[value] {
			preferred = append(preferred, value)
		}
	}
	if len(preferred) == 0 {
		preferred = named.EnumValues
	}
	if len(preferred) == 0 {
		v.refuse(fmt.Sprintf("the enum %q declares no values, so no value of it can be sent", named.Name))
		return nil, nil, false
	}

	// In `variables` an enum member is a JSON STRING, and that is correct here
	// — the coercion from JSON to the enum is the server's. The place the
	// distinction bites is query TEXT, where the same member is written
	// unquoted; see graphqlLiteral.
	values := make([]any, 0, len(preferred)+1)
	for _, value := range preferred {
		values = append(values, value)
	}
	if !nonNull {
		values = append(values, nil)
	}
	return map[string]any{
		"type": graphqlJSONType("string", nonNull),
		"enum": values,
	}, preferred[0], true
}

func (v *graphqlValues) input(named *graphql.Type, nonNull bool) (map[string]any, any, bool) {
	if v.visiting[named.Name] >= graphqlInputRepeats {
		return nil, nil, false
	}
	v.visiting[named.Name]++
	defer func() { v.visiting[named.Name]-- }()

	if named.OneOf {
		return v.oneOf(named, nonNull)
	}

	properties := map[string]any{}
	example := map[string]any{}
	var required []string
	for _, field := range named.Fields {
		_, hasDefault := field.DefaultValue()
		mandatory := field.Type.NonNull && !hasDefault

		schema, value, ok := v.build(field.Type)
		if !ok {
			if mandatory {
				// Nothing legal can be constructed: the field must be present,
				// and no value of its type can be produced.
				return nil, nil, false
			}
			// Optional and unproducible, so it is simply left out — which is a
			// value the server accepts.
			continue
		}
		properties[field.Name] = schema
		if mandatory {
			required = append(required, field.Name)
			example[field.Name] = value
		}
	}

	schema := map[string]any{
		"type":       graphqlJSONType("object", nonNull),
		"properties": properties,
		// GraphQL refuses an input object carrying a field the type does not
		// declare, so the schema says so too. Without it a drawn value could
		// never be judged invalid for the one reason the server cares most
		// about.
		"additionalProperties": false,
	}
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
	}
	return schema, example, true
}

// oneOf builds the schema for an `@oneOf` input object, which must carry
// exactly one of its fields.
//
// It is an enum of one-field objects rather than an object with properties,
// and that is the point rather than a shortcut. Generation fills an object in
// field by field, including each optional one on a coin toss — which produces
// the empty object and the two-field object, and those are precisely the two
// values a @oneOf input exists to reject. Enumerating the legal SHAPES instead
// means every value drawn is one the server must accept, and the invalid mode
// draws something that is none of them.
func (v *graphqlValues) oneOf(named *graphql.Type, nonNull bool) (map[string]any, any, bool) {
	var shapes []any
	for _, field := range named.Fields {
		_, value, ok := v.build(field.Type)
		if !ok {
			continue
		}
		shapes = append(shapes, map[string]any{field.Name: value})
	}
	if len(shapes) == 0 {
		v.refuse(fmt.Sprintf(
			"no value can be built for any field of the @oneOf input object %q, so nothing legal can be sent for it",
			named.Name))
		return nil, nil, false
	}
	first := shapes[0]
	if !nonNull {
		shapes = append(shapes, nil)
	}
	return map[string]any{"type": graphqlJSONType("object", nonNull), "enum": shapes}, first, true
}

func (v *graphqlValues) refuse(message string) {
	v.refused = append(v.refused, message)
}

// graphqlJSONType states a JSON Schema type, admitting null where the GraphQL
// type does.
func graphqlJSONType(name string, nonNull bool) any {
	if nonNull {
		return name
	}
	return []any{name, "null"}
}

// graphqlLiteral renders a value as GraphQL SOURCE TEXT, for a variable
// definition's default.
//
// The enum case comes before the string case and must stay there. An enum
// member is written unquoted and a string quoted, and both arrive here as text
// — so a default of `status: ACTIVE` handled by the string branch is emitted as
// `"ACTIVE"`, which is a different value the server rejects. The apidesc
// package keeps the two apart in the TYPE precisely so this cannot be got wrong
// by accident, and reading graphql.EnumValue first is the other half of that.
func graphqlLiteral(value any) (string, bool) {
	switch typed := value.(type) {
	case graphql.EnumValue:
		return string(typed), true

	case string:
		// A GraphQL string literal is a JSON string literal, escapes included.
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "", false
		}
		return string(encoded), true

	case bool:
		return strconv.FormatBool(typed), true

	case int64:
		return strconv.FormatInt(typed, 10), true

	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), true

	case nil:
		return "null", true

	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := graphqlLiteral(item)
			if !ok {
				return "", false
			}
			parts = append(parts, text)
		}
		return "[" + strings.Join(parts, ", ") + "]", true

	case map[string]any:
		// Sorted, because an input object literal is unordered and a document
		// that differed between runs would make every report undiffable.
		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)

		parts := make([]string, 0, len(names))
		for _, name := range names {
			text, ok := graphqlLiteral(typed[name])
			if !ok {
				return "", false
			}
			parts = append(parts, name+": "+text)
		}
		return "{" + strings.Join(parts, ", ") + "}", true
	}
	return "", false
}
