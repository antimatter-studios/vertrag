package refract

// ToValue is the Go equivalent of minim's toValue(): it collapses an element
// tree into plain data.
//
// The mapping follows minim exactly, including the cases that look odd out of
// context. A member yields {"key":…,"value":…} rather than a single-entry map,
// because that is the shape Dredd's header compilation destructures. An element
// whose content is one nested element unwraps to that element's value, which is
// what makes enum elements collapse to their sample.
func (e *Element) ToValue() any {
	if e == nil {
		return nil
	}
	switch e.Kind {
	case ContentPrimitive:
		return e.Primitive
	case ContentElement:
		return e.Nested.ToValue()
	case ContentMember:
		m := map[string]any{"key": e.Key.ToValue()}
		// A member with no value stays absent rather than nil: minim omits the
		// property entirely, and header compilation relies on the distinction.
		if e.Value != nil {
			m["value"] = e.Value.ToValue()
		}
		return m
	case ContentArray:
		out := make([]any, 0, len(e.Children))
		for _, child := range e.Children {
			out = append(out, child.ToValue())
		}
		return out
	default:
		return nil
	}
}

// StringValue returns the element's content as a string, and false if the
// element is absent or does not hold a string.
func (e *Element) StringValue() (string, bool) {
	if e == nil || e.Kind != ContentPrimitive {
		return "", false
	}
	s, ok := e.Primitive.(string)
	return s, ok
}

// String returns the element's string content, or "" when there is none. It
// stands in for the `x ? x.toValue() : undefined || ”` chains in the original.
func (e *Element) String() string {
	s, _ := e.StringValue()
	return s
}

// Attr returns the named attribute, or nil.
func (e *Element) Attr(name string) *Element {
	if e == nil || e.Attributes == nil {
		return nil
	}
	return e.Attributes[name]
}

// MetaValue returns the named meta property as a string, or "".
//
// It is the analogue of meta.getValue(name), used for element titles.
func (e *Element) MetaValue(name string) string {
	if e == nil || e.Meta == nil {
		return ""
	}
	return e.Meta[name].String()
}

// Classes returns the element's meta classes.
func (e *Element) Classes() []string {
	if e == nil || e.Meta == nil {
		return nil
	}
	classes := e.Meta["classes"]
	if classes == nil || classes.Kind != ContentArray {
		return nil
	}
	out := make([]string, 0, len(classes.Children))
	for _, c := range classes.Children {
		if s, ok := c.StringValue(); ok {
			out = append(out, s)
		}
	}
	return out
}

// HasClass reports whether the element carries the given meta class.
func (e *Element) HasClass(name string) bool {
	for _, c := range e.Classes() {
		if c == name {
			return true
		}
	}
	return false
}

// ContentChildren returns the element's direct children, matching minim's
// `children` getter: array items, a single nested element, or a member's key
// and value.
func (e *Element) ContentChildren() []*Element {
	if e == nil {
		return nil
	}
	switch e.Kind {
	case ContentArray:
		return e.Children
	case ContentElement:
		return []*Element{e.Nested}
	case ContentMember:
		out := []*Element{e.Key}
		if e.Value != nil {
			out = append(out, e.Value)
		}
		return out
	default:
		return nil
	}
}

// Child returns the first direct child with the given element name, or nil.
func (e *Element) Child(name string) *Element {
	for _, child := range e.ContentChildren() {
		if child.Name == name {
			return child
		}
	}
	return nil
}

// ChildWithClass returns the first direct child with the given element name
// that also carries the given meta class, or nil.
//
// Message bodies and body schemas are both `asset` elements distinguished only
// by their class, so name alone cannot select between them.
func (e *Element) ChildWithClass(name, class string) *Element {
	for _, child := range e.ContentChildren() {
		if child.Name == name && child.HasClass(class) {
			return child
		}
	}
	return nil
}

// Parents returns the element's ancestors, nearest first.
func (e *Element) Parents() []*Element {
	var out []*Element
	for p := e.Parent; p != nil; p = p.Parent {
		out = append(out, p)
	}
	return out
}

// FindParent returns the nearest ancestor with the given element name, or nil.
func (e *Element) FindParent(name string) *Element {
	for _, p := range e.Parents() {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// FindParentWithClass returns the nearest ancestor carrying the given meta
// class, or nil.
func (e *Element) FindParentWithClass(class string) *Element {
	for _, p := range e.Parents() {
		if p.HasClass(class) {
			return p
		}
	}
	return nil
}

// FindRecursive collects descendants named by the final entry in names, keeping
// only those whose ancestry contains the preceding names in document order.
//
// It reproduces minim's findRecursive, including its traversal order: an
// element is recorded before its own descendants are searched, and meta and
// attributes are never entered. Order matters because the sequence of
// transactions it returns becomes the order tests run in.
func (e *Element) FindRecursive(names ...string) []*Element {
	if len(names) == 0 {
		return nil
	}
	target := names[len(names)-1]
	chain := names[:len(names)-1]

	var found []*Element
	var check func(el *Element)
	check = func(el *Element) {
		if el == nil {
			return
		}
		if el.Name == target {
			found = append(found, el)
		}
		// Descend through content only. A member's key and value are reached
		// through the explicit branch below, mirroring minim, where the plain
		// recursion does not handle key/value pairs.
		switch el.Kind {
		case ContentArray:
			for _, child := range el.Children {
				check(child)
			}
		case ContentElement:
			check(el.Nested)
		case ContentMember:
			check(el.Key)
			if el.Value != nil {
				check(el.Value)
			}
		}
	}

	switch e.Kind {
	case ContentArray:
		for _, child := range e.Children {
			check(child)
		}
	case ContentElement:
		check(e.Nested)
	}

	if len(chain) == 0 {
		return found
	}

	// Each name in the chain must appear among the element's ancestors, and
	// each subsequent name must sit nearer to the element than the previous
	// one — which is what constrains the names to document order.
	kept := found[:0]
	for _, el := range found {
		ancestors := el.Parents()
		matched := true
		for _, name := range chain {
			idx := -1
			for i, a := range ancestors {
				if a.Name == name {
					idx = i
					break
				}
			}
			if idx == -1 {
				matched = false
				break
			}
			ancestors = ancestors[:idx]
		}
		if matched {
			kept = append(kept, el)
		}
	}
	return kept
}
