package openapi3

import "github.com/antimatter-studios/vertrag/refract"

// parameter is one Parameter Object.
type parameter struct {
	name     string
	in       string
	required bool
	explode  bool
	schema   node
	example  node
	hasValue bool
	value    any

	// converted is the parameter's schema as JSON Schema, which is what a
	// consumer can act on. The node it came from is kept too, because value
	// generation reads the enum from it and does not want to parse the JSON
	// back to do so.
	converted string
}

// parameters holds an operation's parameters in declaration order, grouped by
// where they appear in the request.
type parameters struct {
	ordered []parameter
}

// parseParameters reads a Parameter Object array.
func (d *document) parseParameters(n node) *parameters {
	params := &parameters{}
	for _, item := range n.Items() {
		resolved := d.Resolve(item)
		if !resolved.IsMapping() {
			continue
		}
		name := resolved.Get("name").Str()
		in := resolved.Get("in").Str()
		if name == "" || in == "" {
			continue
		}

		p := parameter{
			name:     name,
			in:       in,
			required: resolved.Get("required").Bool(),
			explode:  resolved.Get("explode").Bool(),
			schema:   d.Resolve(resolved.Get("schema")),
			example:  resolved.Get("example"),
		}

		// Only the Parameter Object's own `example` supplies a value. A schema
		// sitting inside a parameter contributes its `enum` and nothing else:
		// the reference does not read `example` or `default` from there, and a
		// parameter that relied on one would be expanded by vertrag into a URI
		// Dredd never requests.
		if p.example.Valid() {
			p.value = scalarValue(p.example)
			p.hasValue = true
		}

		// The schema is converted here rather than where it is used, because
		// conversion needs the document to resolve references against and
		// nothing downstream of the parse stage has one.
		if converted, ok := d.convertSchema(p.schema); ok {
			p.converted = converted
		}

		params.ordered = append(params.ordered, p)
	}
	return params
}

// merge layers operation parameters over path parameters.
//
// A parameter is identified by name and location together, so a query `id` and
// a path `id` are different parameters and both survive.
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

// queryNames lists the query parameters for the href template, marking the
// exploded ones so the expander renders them as repeated keys.
func (p *parameters) queryNames() []string {
	var names []string
	for _, param := range p.in("query") {
		if param.explode {
			names = append(names, param.name+"*")
			continue
		}
		names = append(names, param.name)
	}
	return names
}

// headers returns the parameters that travel as HTTP headers.
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

// hrefVariables renders the path and query parameters as the element the
// compiler expands the URI from.
//
// Path parameters come before query ones, matching the reference; the order
// reaches the compiler's diagnostics, which are compared against it.
func (p *parameters) hrefVariables() *refract.Element {
	path := p.in("path")
	query := p.in("query")
	if len(path) == 0 && len(query) == 0 {
		return nil
	}

	hrefVariables := refract.Named("hrefVariables")
	for _, param := range append(append([]parameter{}, path...), query...) {
		hrefVariables.Append(param.member())
	}
	return hrefVariables
}

// member renders one parameter as an hrefVariables member.
//
// The value element's content is the example the URI will be built from; its
// enumerations attribute lets the compiler fall back to the first allowed value
// when no example was given, and reject an example that is not one of them.
func (p parameter) member() *refract.Element {
	value := refract.New(schemaElementName(p.schema))
	if p.hasValue {
		setPrimitive(value, p.value)
	}

	if p.schema.Valid() {
		if enum := p.schema.Get("enum"); enum.IsSequence() {
			enumerations := refract.Array()
			for _, item := range enum.Items() {
				enumerations.Append(primitiveElement(scalarValue(item)))
			}
			value.SetAttr("enumerations", enumerations)
		}
	}

	// The whole schema travels alongside the enumerations, because they are not
	// the same statement: enumerations list the values the compiler may fall
	// back to for an example, while the schema also carries the bounds and the
	// pattern a generated value has to respect and a server has to enforce.
	if p.converted != "" {
		value.SetAttr(refract.SchemaAttribute, refract.String(p.converted))
	}

	member := refract.Member(p.name, value)
	if p.required {
		member.SetAttr("typeAttributes", refract.Array(refract.String("required")))
	}
	return member
}
