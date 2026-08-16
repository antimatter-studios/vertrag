package openapi2

import (
	"github.com/antimatter-studios/vertrag/refract"
	"gopkg.in/yaml.v3"
)

// parameter is one Swagger Parameter Object.
//
// Swagger puts the type on the parameter itself rather than in a nested schema,
// except for `in: body`, which carries a full schema.
type parameter struct {
	name     string
	in       string
	required bool
	node     node
	schema   node
	hasValue bool
	value    any

	// converted is the parameter's constraints as a JSON Schema of their own,
	// for the parameters that are not `in: body`. Swagger writes those inline on
	// the Parameter Object, so there is no schema to hand on until one is made.
	converted string
}

// parameters holds an operation's parameters in declaration order.
type parameters struct {
	ordered []parameter
}

func (d *document) parseParameters(n node) *parameters {
	params := &parameters{}
	for _, item := range n.Items() {
		resolved := d.Resolve(item)
		if !resolved.IsMapping() {
			continue
		}
		name, in := resolved.Get("name").Str(), resolved.Get("in").Str()
		if name == "" || in == "" {
			continue
		}

		p := parameter{
			name:     name,
			in:       in,
			required: resolved.Get("required").Bool(),
			node:     resolved,
			schema:   resolved.Get("schema"),
		}
		// Only `x-example` supplies the value to send. A `default` describes
		// what the server assumes when the parameter is omitted, which is a
		// different claim — it is carried through as an attribute, and the URI
		// falls back to the first enum value instead.
		if example := resolved.Get("x-example"); example.Valid() {
			p.value, p.hasValue = scalarValue(example), true
		} else if example := resolved.Get("example"); example.Valid() {
			p.value, p.hasValue = scalarValue(example), true
		}

		if constraints := parameterConstraints(resolved); constraints.Valid() {
			if converted, ok := d.convertSchema(constraints); ok {
				p.converted = converted
			}
		}

		params.ordered = append(params.ordered, p)
	}
	return params
}

// parameterConstraints lifts the JSON Schema keywords out of a Parameter Object.
//
// Swagger puts them directly on the parameter for everything but `in: body`, so
// a parameter reads as a schema with a few extra keys — and one of those keys is
// actively harmful. Swagger's `required` is a boolean saying the parameter must
// be given, while JSON Schema's is a list of property names; left in place it
// makes a schema no validator will compile, and a schema that will not compile
// silently constrains nothing.
//
// The result shares the document's own nodes rather than copying them, so a
// source position still points where a reader would look.
func parameterConstraints(n node) node {
	if !n.IsMapping() {
		return node{}
	}

	constraints := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, member := range n.Entries() {
		switch member.Key.Str() {
		// Where the parameter goes and how it is spelled on the wire says
		// nothing about which values are allowed.
		case "name", "in", "description", "required", "allowEmptyValue", "collectionFormat":
			continue
		}
		constraints.Content = append(constraints.Content, member.Key.Node, member.Value.Node)
	}

	if len(constraints.Content) == 0 {
		return node{}
	}
	return node{Node: constraints}
}

// merge layers operation parameters over path-level ones, matching on name and
// location together.
func (p *parameters) merge(other *parameters) *parameters {
	if p == nil {
		return other
	}
	if other == nil {
		return p
	}

	merged := &parameters{ordered: append([]parameter(nil), p.ordered...)}
	for _, override := range other.ordered {
		replaced := false
		for i, existing := range merged.ordered {
			if existing.name == override.name && existing.in == override.in {
				merged.ordered[i] = override
				replaced = true
				break
			}
		}
		if !replaced {
			merged.ordered = append(merged.ordered, override)
		}
	}
	return merged
}

func (p *parameters) in(location string) []parameter {
	if p == nil {
		return nil
	}
	var out []parameter
	for _, param := range p.ordered {
		if param.in == location {
			out = append(out, param)
		}
	}
	return out
}

func (p *parameters) queryNames() []string {
	var names []string
	for _, param := range p.in("query") {
		names = append(names, param.name)
	}
	return names
}

func (p *parameters) headers() []header {
	var out []header
	for _, param := range p.in("header") {
		value := ""
		if param.hasValue {
			value = stringifyScalar(param.value)
		}
		out = append(out, header{name: param.name, value: value, schema: param.converted})
	}
	return out
}

// hrefVariables renders the path and query parameters the URI is expanded from.
func (p *parameters) hrefVariables() *refract.Element {
	path, query := p.in("path"), p.in("query")
	if len(path) == 0 && len(query) == 0 {
		return nil
	}

	hrefVariables := refract.Named("hrefVariables")
	for _, param := range append(append([]parameter{}, path...), query...) {
		hrefVariables.Append(param.member())
	}
	return hrefVariables
}

func (p parameter) member() *refract.Element {
	value := refract.New(elementName(p.node.Get("type").Str()))
	if p.hasValue {
		setPrimitive(value, p.value)
	}

	if def := p.node.Get("default"); def.Valid() {
		value.SetAttr("default", primitiveElement(scalarValue(def)))
	}
	if enum := p.node.Get("enum"); enum.IsSequence() {
		enumerations := refract.Array()
		for _, item := range enum.Items() {
			enumerations.Append(primitiveElement(scalarValue(item)))
		}
		value.SetAttr("enumerations", enumerations)
	}

	// The constraints travel whole as well as in pieces: the element name and
	// the enumerations are what the compiler needs to pick an example, and the
	// bounds and pattern beside them are what generation needs to produce
	// anything else.
	if p.converted != "" {
		value.SetAttr(refract.SchemaAttribute, refract.String(p.converted))
	}

	member := refract.Member(p.name, value)
	if p.required {
		member.SetAttr("typeAttributes", refract.Array(refract.String("required")))
	}
	return member
}

func elementName(declared string) string {
	switch declared {
	case "integer", "number":
		return "number"
	case "boolean":
		return "boolean"
	case "array":
		return "array"
	case "object":
		return "object"
	default:
		return "string"
	}
}

func primitiveElement(value any) *refract.Element {
	switch v := value.(type) {
	case string:
		return refract.String(v)
	case float64:
		return refract.Number(v)
	case bool:
		return refract.Bool(v)
	default:
		return refract.Null()
	}
}

func setPrimitive(element *refract.Element, value any) {
	switch v := value.(type) {
	case string:
		element.Kind = refract.ContentPrimitive
		element.Primitive = v
	case float64:
		element.Kind = refract.ContentPrimitive
		element.Primitive = v
	case bool:
		element.Kind = refract.ContentPrimitive
		element.Primitive = v
	}
}
