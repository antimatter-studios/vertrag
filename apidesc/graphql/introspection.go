package graphql

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// An introspection result is the other form a schema arrives in, and for a
// great many APIs it is the only one: the SDL is generated from code at build
// time and never checked in, or the schema is composed at run time from several
// services and exists in one piece nowhere but the gateway's memory.
//
// It carries the same information as SDL in a different shape, and two of the
// differences matter enough to state:
//
//   - Wrapping is a chain of types rather than syntax. `[Foo!]!` arrives as
//     NON_NULL(LIST(NON_NULL(Foo))), three nested objects deep.
//   - A default value is a STRING containing GraphQL source text —
//     `"defaultValue": "[1, 2]"`, not a JSON array. So the literal parser the
//     SDL reader uses is run over it, and both forms produce the same Go value.

type introspectionDocument struct {
	Schema *introspectionSchema `json:"__schema"`
	Data   *struct {
		Schema *introspectionSchema `json:"__schema"`
	} `json:"data"`
	// Errors is what a server answers with when introspection is switched off,
	// which is common in production and worth saying plainly: the document is
	// well-formed JSON and contains no schema at all.
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type introspectionSchema struct {
	Description      string             `json:"description"`
	QueryType        *introspectionName `json:"queryType"`
	MutationType     *introspectionName `json:"mutationType"`
	SubscriptionType *introspectionName `json:"subscriptionType"`
	Types            []introspectionType
	Directives       []struct {
		Name string `json:"name"`
	} `json:"directives"`
}

type introspectionName struct {
	Name string `json:"name"`
}

type introspectionType struct {
	Kind          string                    `json:"kind"`
	Name          string                    `json:"name"`
	Description   string                    `json:"description"`
	Fields        []introspectionField      `json:"fields"`
	InputFields   []introspectionInputValue `json:"inputFields"`
	Interfaces    []introspectionName       `json:"interfaces"`
	EnumValues    []introspectionEnumValue  `json:"enumValues"`
	PossibleTypes []introspectionName       `json:"possibleTypes"`
	IsOneOf       bool                      `json:"isOneOf"`
	SpecifiedBy   string                    `json:"specifiedByURL"`
}

type introspectionField struct {
	Name              string                    `json:"name"`
	Description       string                    `json:"description"`
	Args              []introspectionInputValue `json:"args"`
	Type              *introspectionTypeRef     `json:"type"`
	IsDeprecated      bool                      `json:"isDeprecated"`
	DeprecationReason string                    `json:"deprecationReason"`
}

type introspectionInputValue struct {
	Name              string                `json:"name"`
	Description       string                `json:"description"`
	Type              *introspectionTypeRef `json:"type"`
	DefaultValue      *string               `json:"defaultValue"`
	IsDeprecated      bool                  `json:"isDeprecated"`
	DeprecationReason string                `json:"deprecationReason"`
}

type introspectionEnumValue struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	IsDeprecated      bool   `json:"isDeprecated"`
	DeprecationReason string `json:"deprecationReason"`
}

type introspectionTypeRef struct {
	Kind   string                `json:"kind"`
	Name   string                `json:"name"`
	OfType *introspectionTypeRef `json:"ofType"`
}

func parseIntrospection(source []byte) (*Schema, []string, error) {
	var document introspectionDocument
	if err := json.Unmarshal(source, &document); err != nil {
		return nil, nil, fmt.Errorf("this is not readable JSON: %w", err)
	}

	// Both placements are accepted because both are what people have: a raw
	// GraphQL response is wrapped in `data`, and every tool that saves one to a
	// file unwraps it.
	root := document.Schema
	if root == nil && document.Data != nil {
		root = document.Data.Schema
	}
	if root == nil {
		if len(document.Errors) > 0 {
			messages := make([]string, 0, len(document.Errors))
			for _, failure := range document.Errors {
				messages = append(messages, failure.Message)
			}
			return nil, nil, fmt.Errorf(
				"the introspection query failed rather than returning a schema, which is what a "+
					"server with introspection disabled answers: %s", strings.Join(messages, "; "))
		}
		return nil, nil, errors.New(
			"this JSON document has no `__schema`: vertrag reads the result of the standard " +
				"introspection query, either at the top level or wrapped in `data`")
	}

	w := &warnings{}
	s := &Schema{Types: map[string]*Type{}, Description: root.Description}

	for _, declared := range root.Types {
		named := convertIntrospectionType(declared, w)
		if named == nil {
			continue
		}
		if _, defined := s.Types[named.Name]; defined {
			w.add("the introspection result lists the type %s twice; the first is used", named.Name)
			continue
		}
		s.Types[named.Name] = named
	}

	// possibleTypes is the authority where it is given, and objects' own
	// interface lists fill in the rest: a schema stitched together by a gateway
	// sometimes reports one and not the other.
	linkInterfaces(s, w)

	for _, directive := range root.Directives {
		if isBuiltInDirective(directive.Name) {
			continue
		}
		w.add("the schema defines the directive @%s; vertrag does not model it, so whatever it "+
			"changes about this API goes untested", directive.Name)
	}

	if root.QueryType != nil {
		s.QueryType = root.QueryType.Name
	}
	if root.MutationType != nil {
		s.MutationType = root.MutationType.Name
	}
	if root.SubscriptionType != nil {
		s.SubscriptionType = root.SubscriptionType.Name
	}

	finalise(s, w)
	return s, w.list(), nil
}

func convertIntrospectionType(declared introspectionType, w *warnings) *Type {
	if declared.Name == "" {
		w.add("the introspection result contains a %s type with no name, which nothing can refer "+
			"to; it is ignored", strings.ToLower(declared.Kind))
		return nil
	}

	named := &Type{
		Name:        declared.Name,
		Description: declared.Description,
		OneOf:       declared.IsOneOf,
		SpecifiedBy: declared.SpecifiedBy,
	}

	switch declared.Kind {
	case "SCALAR":
		named.Kind = KindScalar
	case "OBJECT":
		named.Kind = KindObject
	case "INTERFACE":
		named.Kind = KindInterface
	case "UNION":
		named.Kind = KindUnion
	case "ENUM":
		named.Kind = KindEnum
	case "INPUT_OBJECT":
		named.Kind = KindInputObject
	case "LIST", "NON_NULL":
		// A wrapper appearing where a named type belongs means the document was
		// assembled by hand or by something confused; it cannot be a type.
		w.add("the introspection result lists %s as a %s, which wraps a type rather than being "+
			"one; it is ignored", declared.Name, declared.Kind)
		return nil
	default:
		w.add("the introspection result gives %s the kind %q, which GraphQL does not define; "+
			"the type is ignored", declared.Name, declared.Kind)
		return nil
	}

	switch named.Kind {
	case KindObject, KindInterface:
		if len(declared.Fields) == 0 {
			// Two causes, both worth knowing about: a truncated introspection
			// query, or a type whose every field is deprecated and the query
			// asked for `fields` without includeDeprecated. Either way nothing
			// can be asked of this type.
			w.add("the introspection result gives the %s type %s no fields, so nothing can be "+
				"selected from it", named.Kind, named.Name)
		}
		for _, field := range declared.Fields {
			named.Fields = append(named.Fields, convertIntrospectionField(named.Name, field, w))
		}

	case KindInputObject:
		for _, input := range declared.InputFields {
			named.Fields = append(named.Fields, convertIntrospectionInputField(named.Name, input, w))
		}

	case KindEnum:
		for _, value := range declared.EnumValues {
			named.EnumValues = append(named.EnumValues, value.Name)
			if value.IsDeprecated {
				named.DeprecatedEnumValues = append(named.DeprecatedEnumValues, value.Name)
			}
			if value.Description != "" {
				if named.EnumValueDescriptions == nil {
					named.EnumValueDescriptions = map[string]string{}
				}
				named.EnumValueDescriptions[value.Name] = value.Description
			}
		}
	}

	for _, iface := range declared.Interfaces {
		if iface.Name == "" {
			continue
		}
		named.Interfaces = append(named.Interfaces, iface.Name)
	}
	for _, possible := range declared.PossibleTypes {
		if possible.Name == "" {
			continue
		}
		named.Possible = append(named.Possible, possible.Name)
	}

	return named
}

func convertIntrospectionField(owner string, declared introspectionField, w *warnings) Field {
	where := owner + "." + declared.Name

	field := Field{
		Name:              declared.Name,
		Description:       declared.Description,
		Type:              convertIntrospectionRef(declared.Type, "the field "+where, w),
		Deprecated:        declared.IsDeprecated,
		DeprecationReason: declared.DeprecationReason,
	}
	for _, arg := range declared.Args {
		field.Args = append(field.Args, convertIntrospectionArgument(
			fmt.Sprintf("the argument %s(%s:)", where, arg.Name), arg, w))
	}
	return field
}

func convertIntrospectionInputField(owner string, declared introspectionInputValue, w *warnings) Field {
	where := "the input field " + owner + "." + declared.Name

	field := Field{
		Name:              declared.Name,
		Description:       declared.Description,
		Type:              convertIntrospectionRef(declared.Type, where, w),
		Deprecated:        declared.IsDeprecated,
		DeprecationReason: declared.DeprecationReason,
	}
	if declared.DefaultValue != nil {
		if value, ok := literalDefault(*declared.DefaultValue, where, w); ok {
			field.Default, field.HasDefault = value, true
		}
	}
	attachInputDefault(&field)
	return field
}

func convertIntrospectionArgument(where string, declared introspectionInputValue, w *warnings) Argument {
	arg := Argument{
		Name:              declared.Name,
		Description:       declared.Description,
		Type:              convertIntrospectionRef(declared.Type, where, w),
		Deprecated:        declared.IsDeprecated,
		DeprecationReason: declared.DeprecationReason,
	}
	if declared.DefaultValue != nil {
		if value, ok := literalDefault(*declared.DefaultValue, where, w); ok {
			arg.Default, arg.HasDefault = value, true
		}
	}
	return arg
}

// literalDefault reads a default reported as GraphQL source text.
//
// A default that cannot be read is dropped rather than kept as the string it
// was written as: `"[1, 2]"` held as a string would be sent as a string, and a
// server rejecting it would be reported as the failure. Dropping it means the
// value is generated instead, which is at least a value of the right type — and
// the warning says which field lost its default.
func literalDefault(literal, where string, w *warnings) (any, bool) {
	value, err := parseLiteral(literal)
	if err != nil {
		w.add("%s has the default `%s`, which is not a value GraphQL can express (%v); "+
			"it is ignored", where, literal, err)
		return nil, false
	}
	return value, true
}

// convertIntrospectionRef turns the wrapper chain into a TypeRef.
//
// NON_NULL applies to whatever it wraps, so it is folded into the reference the
// recursion returns rather than becoming a level of its own — otherwise
// `[Foo!]!` would be four references deep for a type that is two wrappers.
func convertIntrospectionRef(declared *introspectionTypeRef, where string, w *warnings) TypeRef {
	if declared == nil {
		w.add("%s has no type in the introspection result, so nothing is known about what it "+
			"carries", where)
		return TypeRef{}
	}

	switch declared.Kind {
	case "NON_NULL":
		if declared.OfType == nil {
			w.add("%s is a NON_NULL wrapping nothing; the wrapper is ignored", where)
			return TypeRef{}
		}
		inner := convertIntrospectionRef(declared.OfType, where, w)
		inner.NonNull = true
		return inner

	case "LIST":
		if declared.OfType == nil {
			w.add("%s is a LIST wrapping nothing; the wrapper is ignored", where)
			return TypeRef{}
		}
		inner := convertIntrospectionRef(declared.OfType, where, w)
		return TypeRef{List: &inner}

	default:
		if declared.Name == "" {
			w.add("%s refers to a %s type with no name", where, strings.ToLower(declared.Kind))
			return TypeRef{}
		}
		return TypeRef{Named: declared.Name}
	}
}
