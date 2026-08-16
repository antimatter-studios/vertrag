// Package refract implements the API Elements element model (the "minim"
// object model) that Dredd's compiler operates on.
//
// Dredd's pipeline is parse -> API Elements -> compile. The parse stage is
// format-specific (API Blueprint, OpenAPI 2, OpenAPI 3); everything after it is
// not. Modelling API Elements faithfully is therefore what lets one compiler
// serve every input format, and it is why this package exists rather than the
// compiler walking format-specific structs.
//
// The serialised form is Refract JSON. An element is a name, optional meta and
// attributes maps (whose values are themselves elements), and content that is
// one of: absent, a primitive, an array of elements, a single element, or a
// key/value member.
package refract

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ContentKind discriminates the five shapes an element's content can take.
type ContentKind int

const (
	// ContentNone is an element with no content at all (JSON null or absent).
	ContentNone ContentKind = iota
	// ContentPrimitive is a string, number or boolean.
	ContentPrimitive
	// ContentArray is an ordered list of child elements.
	ContentArray
	// ContentElement is a single nested element.
	ContentElement
	// ContentMember is a key/value pair, both elements.
	ContentMember
)

// Element is a node in an API Elements document.
//
// Parent is populated by the loader rather than by the wire format, because the
// compiler needs to walk upwards from an httpTransaction to the resource and
// category elements that give it its name.
type Element struct {
	Name       string
	Meta       map[string]*Element
	Attributes map[string]*Element

	Kind      ContentKind
	Primitive any        // string, float64 or bool when Kind is ContentPrimitive
	Children  []*Element // when Kind is ContentArray
	Nested    *Element   // when Kind is ContentElement
	Key       *Element   // when Kind is ContentMember
	Value     *Element   // when Kind is ContentMember

	Parent *Element
}

// rawElement mirrors the Refract wire format. Content is held back as raw JSON
// so it can be dispatched on its JSON token type, which is the only thing that
// distinguishes an array of elements from a member from a primitive.
type rawElement struct {
	Element    string                     `json:"element"`
	Meta       map[string]json.RawMessage `json:"meta,omitempty"`
	Attributes map[string]json.RawMessage `json:"attributes,omitempty"`
	Content    json.RawMessage            `json:"content,omitempty"`
}

// UnmarshalJSON decodes an element and, recursively, everything under it.
func (e *Element) UnmarshalJSON(data []byte) error {
	var raw rawElement
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	e.Name = raw.Element

	var err error
	if e.Meta, err = decodeElementMap(raw.Meta); err != nil {
		return fmt.Errorf("meta of %q: %w", e.Name, err)
	}
	if e.Attributes, err = decodeElementMap(raw.Attributes); err != nil {
		return fmt.Errorf("attributes of %q: %w", e.Name, err)
	}
	if err := e.decodeContent(raw.Content); err != nil {
		return fmt.Errorf("content of %q: %w", e.Name, err)
	}
	e.link(nil)
	return nil
}

func decodeElementMap(raw map[string]json.RawMessage) (map[string]*Element, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]*Element, len(raw))
	for k, v := range raw {
		child := new(Element)
		if err := json.Unmarshal(v, child); err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		out[k] = child
	}
	return out, nil
}

// decodeContent dispatches on the JSON token that opens the content value.
func (e *Element) decodeContent(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		e.Kind = ContentNone
		return nil
	}

	switch trimmed[0] {
	case '[':
		var children []*Element
		if err := json.Unmarshal(trimmed, &children); err != nil {
			return err
		}
		e.Kind = ContentArray
		e.Children = children
		return nil

	case '{':
		// Two object shapes share this branch: a nested element, which carries
		// an "element" key, and a member, which carries "key" (and usually
		// "value"). Probing for "element" is what tells them apart.
		var probe struct {
			Element string          `json:"element"`
			Key     json.RawMessage `json:"key"`
			Value   json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(trimmed, &probe); err != nil {
			return err
		}
		if probe.Element != "" {
			nested := new(Element)
			if err := json.Unmarshal(trimmed, nested); err != nil {
				return err
			}
			e.Kind = ContentElement
			e.Nested = nested
			return nil
		}
		if len(probe.Key) == 0 {
			return fmt.Errorf("object content is neither an element nor a member")
		}
		e.Kind = ContentMember
		e.Key = new(Element)
		if err := json.Unmarshal(probe.Key, e.Key); err != nil {
			return fmt.Errorf("member key: %w", err)
		}
		if len(probe.Value) > 0 && !bytes.Equal(bytes.TrimSpace(probe.Value), []byte("null")) {
			e.Value = new(Element)
			if err := json.Unmarshal(probe.Value, e.Value); err != nil {
				return fmt.Errorf("member value: %w", err)
			}
		}
		return nil

	default:
		var primitive any
		if err := json.Unmarshal(trimmed, &primitive); err != nil {
			return err
		}
		e.Kind = ContentPrimitive
		e.Primitive = primitive
		return nil
	}
}

// link threads Parent pointers through the tree.
//
// Only content is linked, not meta and attributes: the compiler walks parents
// to find enclosing resources and categories, and an element sitting in an
// attribute is not inside its host in that structural sense. Treating it as
// such would let a parent search escape into unrelated branches.
func (e *Element) link(parent *Element) {
	e.Parent = parent
	switch e.Kind {
	case ContentArray:
		for _, child := range e.Children {
			child.link(e)
		}
	case ContentElement:
		e.Nested.link(e)
	case ContentMember:
		if e.Key != nil {
			e.Key.link(e)
		}
		if e.Value != nil {
			e.Value.link(e)
		}
	}
}

// Load decodes a Refract JSON document.
func Load(data []byte) (*Element, error) {
	root := new(Element)
	if err := json.Unmarshal(data, root); err != nil {
		return nil, err
	}
	return root, nil
}
