package openapi2

import "github.com/antimatter-studios/vertrag/internal/refract"

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
}

// parameters holds an operation's parameters in declaration order.
type parameters struct {
	ordered []parameter
}

func (d *document) parseParameters(n node) *parameters {
	params := &parameters{}
	for _, item := range n.items() {
		resolved := d.resolve(item)
		if !resolved.isMapping() {
			continue
		}
		name, in := resolved.get("name").str(), resolved.get("in").str()
		if name == "" || in == "" {
			continue
		}

		p := parameter{
			name:     name,
			in:       in,
			required: resolved.get("required").boolValue(),
			node:     resolved,
			schema:   resolved.get("schema"),
		}
		// Only `x-example` supplies the value to send. A `default` describes
		// what the server assumes when the parameter is omitted, which is a
		// different claim — it is carried through as an attribute, and the URI
		// falls back to the first enum value instead.
		if example := resolved.get("x-example"); example.valid() {
			p.value, p.hasValue = scalarValue(example), true
		} else if example := resolved.get("example"); example.valid() {
			p.value, p.hasValue = scalarValue(example), true
		}
		params.ordered = append(params.ordered, p)
	}
	return params
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
		out = append(out, header{name: param.name, value: value})
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
	value := refract.New(elementName(p.node.get("type").str()))
	if p.hasValue {
		setPrimitive(value, p.value)
	}

	if def := p.node.get("default"); def.valid() {
		value.SetAttr("default", primitiveElement(scalarValue(def)))
	}
	if enum := p.node.get("enum"); enum.isSequence() {
		enumerations := refract.Array()
		for _, item := range enum.items() {
			enumerations.Append(primitiveElement(scalarValue(item)))
		}
		value.SetAttr("enumerations", enumerations)
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
