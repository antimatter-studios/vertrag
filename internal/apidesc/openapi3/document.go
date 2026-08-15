// Package openapi3 parses OpenAPI 3 documents into API Elements.
package openapi3

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// node wraps a YAML node with the accessors this parser needs.
//
// The document is walked as YAML rather than decoded into structs because every
// diagnostic has to point at a line and column in the source. Decoding into Go
// types would discard exactly the information the annotations are made of.
type node struct {
	*yaml.Node
}

// document is a parsed OpenAPI 3 document plus the lookup it needs to resolve
// internal references.
type document struct {
	root node
}

// parseDocument reads YAML (or JSON, which YAML is a superset of).
func parseDocument(source []byte) (*document, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(source, &root); err != nil {
		return nil, err
	}

	// A document node wraps the real content; unwrap so callers see the mapping.
	content := &root
	if content.Kind == yaml.DocumentNode && len(content.Content) > 0 {
		content = content.Content[0]
	}
	return &document{root: node{content}}, nil
}

func (n node) valid() bool { return n.Node != nil }

func (n node) isMapping() bool { return n.valid() && n.Kind == yaml.MappingNode }

func (n node) isSequence() bool { return n.valid() && n.Kind == yaml.SequenceNode }

func (n node) isScalar() bool { return n.valid() && n.Kind == yaml.ScalarNode }

// get returns the value for a mapping key, or an invalid node.
func (n node) get(key string) node {
	if !n.isMapping() {
		return node{}
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return node{n.Content[i+1]}
		}
	}
	return node{}
}

// keyNode returns the key node for a mapping entry, which is what diagnostics
// about that entry point at.
func (n node) keyNode(key string) node {
	if !n.isMapping() {
		return node{}
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return node{n.Content[i]}
		}
	}
	return node{}
}

// entry is one key/value pair of a mapping, kept in document order.
type entry struct {
	key   node
	value node
}

// entries returns a mapping's members in document order.
//
// Order is preserved because it decides the order transactions are created in,
// which is the order tests run in.
func (n node) entries() []entry {
	if !n.isMapping() {
		return nil
	}
	out := make([]entry, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, entry{key: node{n.Content[i]}, value: node{n.Content[i+1]}})
	}
	return out
}

// items returns a sequence's elements.
func (n node) items() []node {
	if !n.isSequence() {
		return nil
	}
	out := make([]node, 0, len(n.Content))
	for _, item := range n.Content {
		out = append(out, node{item})
	}
	return out
}

// str returns a scalar's string value, or "".
func (n node) str() string {
	if !n.isScalar() {
		return ""
	}
	return n.Value
}

// boolValue returns a scalar's boolean value.
func (n node) boolValue() bool {
	return n.isScalar() && n.Value == "true"
}

// resolve follows a $ref within this document.
//
// Only local references are followed. An external one is left unresolved, which
// downstream shows up as a missing schema rather than a fetch: resolving it
// would mean reading files or making network requests during what is supposed
// to be a pure parse.
func (d *document) resolve(n node) node {
	seen := 0
	for n.isMapping() {
		ref := n.get("$ref")
		if !ref.isScalar() || !strings.HasPrefix(ref.Value, "#/") {
			return n
		}
		// A reference cycle would otherwise spin here; the document is at fault,
		// and stopping leaves the caller with the last node it could reach.
		seen++
		if seen > 100 {
			return n
		}
		n = d.pointer(ref.Value)
		if !n.valid() {
			return node{}
		}
	}
	return n
}

// pointer walks a JSON Pointer such as #/components/schemas/Pet.
func (d *document) pointer(ref string) node {
	current := d.root
	for _, segment := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		if segment == "" {
			continue
		}
		segment = strings.ReplaceAll(segment, "~1", "/")
		segment = strings.ReplaceAll(segment, "~0", "~")
		current = current.get(segment)
		if !current.valid() {
			return node{}
		}
	}
	return current
}

// httpMethods are the operation keys of a Path Item Object, in the order the
// specification lists them. A Path Item's other keys are not operations.
var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

func isHTTPMethod(key string) bool {
	for _, method := range httpMethods {
		if key == method {
			return true
		}
	}
	return false
}
