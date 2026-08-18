package openapi3

import (
	"strings"

	"github.com/antimatter-studios/vertrag/refract"
)

// parameter is one Parameter Object.
type parameter struct {
	name     string
	in       string
	required bool
	explode  bool
	style    string
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
			schema:   d.Resolve(resolved.Get("schema")),
			example:  resolved.Get("example"),
		}
		p.style, p.explode = serialisation(in, resolved)

		// Where the document demonstrates a value, in any of the three places
		// it is allowed to put one. See parameterExample.
		if value, example, found := d.parameterExample(resolved); found {
			p.value = value
			p.example = example
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

// parameterExample finds the value a document demonstrates for a parameter.
//
// There are three places it may be, and vertrag read only the first — which
// meant that for a required path parameter it usually found nothing, could
// not build a URI, and produced NO TRANSACTION at all. That is the failure
// direction that flatters a tester: an untestable route is not reported as a
// gap, it is simply absent, and coverage reads as complete.
//
//  1. The Parameter Object's own `example`.
//  2. Its `examples`, a MAP of Example Objects, each with a `value` — or, since
//     3.2, a `dataValue` saying the same thing unambiguously.
//  3. The schema's `examples`, an ARRAY — JSON Schema 2020-12's keyword, and
//     the form OpenAPI 3.1 documents use.
//
// The last is the one that matters in practice: it is what FastAPI emits, and
// its only non-deprecated spelling, so every document that generator produces
// depended on vertrag reading a keyword it did not. Two keywords share the
// name `examples` with different shapes — a map at the parameter, an array in
// the schema — and are told apart by where they sit, which is the whole
// reason to read them separately rather than guessing.
func (d *document) parameterExample(parameter node) (value any, source node, found bool) {
	if example := parameter.Get("example"); example.Valid() {
		return scalarValue(example), example, true
	}

	// A map of Example Objects; the first in document order stands in, the
	// way the first enum value does elsewhere.
	for _, member := range parameter.Get("examples").Entries() {
		if inner := exampleValue(d.Resolve(member.Value)); inner.Valid() {
			return scalarValue(inner), inner, true
		}
	}

	// The schema's own array. Resolved, because a parameter's schema is
	// frequently a reference.
	schema := d.Resolve(parameter.Get("schema"))
	for _, item := range schema.Get("examples").Items() {
		return scalarValue(item), item, true
	}

	return nil, node{}, false
}

// serialisation resolves a parameter's style and explode, applying the
// specification's defaults where the document is silent: query and cookie
// parameters are `form` and exploded (a list is `a=1&a=2`), path and header
// parameters are `simple` and not (a list is `1,2`). Reading `explode` as a
// bare boolean, as this once did, made every absent explode false — which is
// the wrong default for the commonest case, a query list.
func serialisation(in string, resolved node) (style string, explode bool) {
	style = resolved.Get("style").Str()
	if style == "" {
		switch in {
		case "query", "cookie":
			style = "form"
		default:
			style = "simple"
		}
	}
	// The default for explode is "true for form, false otherwise", per spec.
	explode = style == "form"
	if e := resolved.Get("explode"); e.Valid() {
		explode = e.Bool()
	}
	// deepObject is only defined exploded — `x[a]=1&x[b]=2` IS the explosion
	// — so it is exploded whatever the document says, and the template's `*`
	// is what lets the expander lay the object out as pairs.
	if style == "deepObject" {
		explode = true
	}
	return style, explode
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

// sendableParameterLocation reports whether vertrag puts a parameter of this
// location into the request it builds. Everything else is warned about, since
// the alternative is a documented input that silently never travels.
func sendableParameterLocation(in string) bool {
	switch in {
	case "path", "query", "header", "cookie":
		return true
	}
	return false
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

// cookies returns the parameters that travel in the Cookie header, in
// declaration order.
//
// They are kept as a list of their own rather than folded straight into the
// header list, because they are not one header each: they SHARE a header, and
// the pieces downstream need them separately — the compiler to record each as
// a parameter a generator can vary, the request builder to lay them out as one
// line. A parameter the document gave no value is left out of the line and
// still listed here, exactly as an example-less query parameter is absent from
// the URI but present in the parameter list.
func (p *parameters) cookies() []header {
	var out []header
	for _, param := range p.in("cookie") {
		value := ""
		if param.hasValue {
			value = stringifyScalar(param.value)
		}
		out = append(out, header{name: param.name, value: value, schema: param.converted})
	}
	return out
}

// cookieLine renders the cookie parameters as a Cookie header value.
//
// RFC 6265: `name=value` pairs separated by "; ". A parameter with no
// demonstrated value contributes nothing rather than an empty pair — `x=` says
// the cookie was sent and is blank, which is a different request from the one
// a document that named no value described.
func cookieLine(cookies []header) (string, bool) {
	pairs := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie.value == "" {
			continue
		}
		pairs = append(pairs, cookie.name+"="+cookie.value)
	}
	if len(pairs) == 0 {
		return "", false
	}
	return strings.Join(pairs, "; "), true
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
	// The style travels only when it is not the RFC 6570 default the template
	// already expresses, so a document that never mentions style produces
	// exactly the elements it did before.
	if p.style != "form" && p.style != "simple" {
		value.SetAttr(refract.StyleAttribute, refract.String(p.style))
	}

	member := refract.Member(p.name, value)
	if p.required {
		member.SetAttr("typeAttributes", refract.Array(refract.String("required")))
	}
	return member
}
