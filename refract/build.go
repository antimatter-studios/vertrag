package refract

// Constructors for building API Elements.
//
// The format parsers produce elements; the compiler consumes them. Keeping the
// construction here means a parser never has to know how an element is
// represented, only what shape the API Elements specification asks for.
//
// Parent pointers are maintained as elements are assembled, because the
// compiler walks upwards from a transaction to the resource and category that
// name it. A tree built without them compiles to transactions with empty names.

// SchemaAttribute names the attribute a parser hangs a parameter's JSON Schema
// from.
//
// It is vertrag's own extension rather than part of API Elements, and it is
// named so that a reader of an element tree can tell at a glance. The format has
// nowhere to say what a URI or header parameter's value must look like beyond
// its primitive type and its enumerations, which is enough to substitute one
// example into a URI and not enough to generate from: the length, range and
// pattern a handler forgets to check are all written in the parameter's schema
// and would otherwise be dropped at the parse stage.
const SchemaAttribute = "vertragSchema"

// OmittedAttribute marks a header parameter that has no value to send. Its
// schema still travels for generation, but it is absent from the wire headers.
const OmittedAttribute = "vertragOmitted"

// StyleAttribute carries an OpenAPI parameter's serialisation style
// ("form", "spaceDelimited", "pipeDelimited", "deepObject", "simple") on an
// hrefVariables member, so the compiler and generation render a list or an
// object the way the description said it would be sent. Dredd has no such
// attribute — like the schema, it travels for vertrag's own use.
const StyleAttribute = "vertragStyle"

// CookiesAttribute carries an operation's cookie parameters on an httpRequest
// element, as a member per cookie holding its demonstrated value and, through
// SchemaAttribute, the schema its value must satisfy.
//
// They cannot travel in the header list they end up in, because they are not
// one header each: several cookie parameters share a single Cookie line, and
// flattening them into it at the parse stage would lose which byte of that
// line belongs to which parameter — so nothing downstream could vary one
// cookie, or say which one a finding is about. Dredd has no such attribute;
// like the schema and the style, it travels for vertrag's own use.
const CookiesAttribute = "vertragCookies"

// ExcludeAttribute carries the reason an operation's own `x-vertrag-exclude`
// extension gives for leaving it out of a run's surface figures, on the
// transition. It names no request and changes nothing that is sent: what it
// decides is whether an operation a run did not reach counts against the run.
// Dredd has no such attribute.
const ExcludeAttribute = "vertragExclude"

// ExampleAttribute marks a parameter value carried on an httpRequest itself, in
// its own hrefVariables or headers, as the value a Parameter Object's
// `examples` map gives under the request's example name. The request is the
// exchange that name stands for, so the value is part of what it tests: a link
// fills a parameter from an earlier response, but not one the request names
// this way, or a "missing" id would be replaced by the one just created. Its
// content is the example's name. Dredd has no such attribute.
const ExampleAttribute = "vertragExample"

// PairAttribute carries, on an httpTransaction, the example name its request
// and response were paired by: the response's name, or the request's when only
// the request is named. Empty for an exchange no name chose. It is what
// `exclude-example` matches; it is not part of the transaction's name, which
// hooks address and which stays as the reference spells it. Dredd has no such
// attribute.
const PairAttribute = "vertragPair"

// RequiresAttribute carries, on an httpTransaction, what proving the exchange needs:
// the `x-requires` its Example Objects give, merged across the request and the
// response, as a JSON object of kind to value (#105). Absent for an exchange that
// needs nothing. Dredd has no such attribute.
const RequiresAttribute = "vertragRequires"

// DeclaredAttribute marks a messageBody asset whose content the description
// wrote out, as an example, rather than one generated from a schema. Only a
// declared body can be an explicitly empty expectation (#90): an empty value
// generated from a `type: string` schema is something to send, and expecting
// it would fail every text response with no example. Dredd has no such
// attribute.
const DeclaredAttribute = "vertragDeclared"

// New returns an empty element with the given name.
func New(name string) *Element {
	return &Element{Name: name, Kind: ContentNone}
}

// String returns a string element.
func String(value string) *Element {
	return &Element{Name: "string", Kind: ContentPrimitive, Primitive: value}
}

// Number returns a number element.
func Number(value float64) *Element {
	return &Element{Name: "number", Kind: ContentPrimitive, Primitive: value}
}

// Bool returns a boolean element.
func Bool(value bool) *Element {
	return &Element{Name: "boolean", Kind: ContentPrimitive, Primitive: value}
}

// Null returns a null element.
func Null() *Element {
	return &Element{Name: "null", Kind: ContentNone}
}

// Array returns an array element holding the given children.
func Array(children ...*Element) *Element {
	e := &Element{Name: "array", Kind: ContentArray}
	for _, child := range children {
		e.Append(child)
	}
	return e
}

// Member returns a key/value member element.
func Member(key string, value *Element) *Element {
	e := &Element{Name: "member", Kind: ContentMember}
	e.Key = String(key)
	e.Key.Parent = e
	if value != nil {
		e.Value = value
		value.Parent = e
	}
	return e
}

// Object returns an object element holding the given members.
func Object(members ...*Element) *Element {
	e := &Element{Name: "object", Kind: ContentArray}
	for _, member := range members {
		e.Append(member)
	}
	return e
}

// Named returns an element with the given name and array content.
//
// Most API Elements types — resource, transition, httpTransaction — are
// containers whose meaning comes from their name rather than their structure.
func Named(name string, children ...*Element) *Element {
	e := &Element{Name: name, Kind: ContentArray}
	for _, child := range children {
		e.Append(child)
	}
	return e
}

// Text returns an element with the given name and string content, used for
// assets such as message bodies.
func Text(name, value string) *Element {
	return &Element{Name: name, Kind: ContentPrimitive, Primitive: value}
}

// Append adds a child, switching the element to array content if needed.
func (e *Element) Append(child *Element) *Element {
	if child == nil {
		return e
	}
	if e.Kind != ContentArray {
		e.Kind = ContentArray
		e.Children = nil
	}
	child.Parent = e
	e.Children = append(e.Children, child)
	return e
}

// SetMeta sets a meta property.
func (e *Element) SetMeta(name string, value *Element) *Element {
	if e.Meta == nil {
		e.Meta = map[string]*Element{}
	}
	e.Meta[name] = value
	return e
}

// SetTitle sets the element's title, which the compiler reads when naming
// transactions.
func (e *Element) SetTitle(title string) *Element {
	return e.SetMeta("title", String(title))
}

// AddClass appends a meta class, creating the classes array if absent.
func (e *Element) AddClass(class string) *Element {
	if e.Meta == nil {
		e.Meta = map[string]*Element{}
	}
	classes, ok := e.Meta["classes"]
	if !ok || classes.Kind != ContentArray {
		classes = Array()
		e.Meta["classes"] = classes
	}
	classes.Append(String(class))
	return e
}

// SetAttr sets an attribute.
//
// Attributes are deliberately not given parent pointers: the compiler walks
// parents to find enclosing resources, and an element sitting in an attribute
// is not inside its host in that structural sense.
func (e *Element) SetAttr(name string, value *Element) *Element {
	if value == nil {
		return e
	}
	if e.Attributes == nil {
		e.Attributes = map[string]*Element{}
	}
	e.Attributes[name] = value
	return e
}

// SetSourceMap records where in the source document this element came from, as
// a [line, column] start and end. It is what parser annotations point at.
func (e *Element) SetSourceMap(startLine, startColumn, endLine, endColumn int) *Element {
	start := Number(0)
	start.SetAttr("line", Number(float64(startLine)))
	start.SetAttr("column", Number(float64(startColumn)))

	end := Number(0)
	end.SetAttr("line", Number(float64(endLine)))
	end.SetAttr("column", Number(float64(endColumn)))

	return e.SetAttr("sourceMap", Array(Named("sourceMap", Array(start, end))))
}
