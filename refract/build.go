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

// StyleAttribute carries an OpenAPI parameter's serialisation style
// ("form", "spaceDelimited", "pipeDelimited", "deepObject", "simple") on an
// hrefVariables member, so the compiler and generation render a list or an
// object the way the description said it would be sent. Dredd has no such
// attribute — like the schema, it travels for vertrag's own use.
const StyleAttribute = "vertragStyle"

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
