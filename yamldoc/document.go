// Package yamldoc navigates a YAML or JSON document while keeping the source
// positions that diagnostics are made of.
//
// The parsing is gopkg.in/yaml.v3's; nothing here re-implements it. What this
// package adds is the layer above: ordered access to a mapping, JSON Pointer
// resolution for $ref, and the source span of a node.
//
// That layer exists because a typed decoder cannot serve these parsers. They
// have to report the keys a document contains that the specification does not,
// which decoding into structs discards; they have to point each diagnostic at a
// line and column, which decoding also discards; and they have to preserve the
// document's own key order, because it decides the order tests run in and the
// byte order of generated bodies.
package yamldoc

import (
	"bytes"
	"strings"

	"gopkg.in/yaml.v3"
)

// Node wraps a YAML node with the accessors the parsers need.
type Node struct {
	*yaml.Node
}

// Document is a parsed document plus the lookups needed to resolve references
// and report source positions.
type Document struct {
	Root Node

	// SelfURI is the URI the document assigns itself, and is empty for a
	// document that assigns itself none. A reference beginning with it is a
	// reference into THIS document, however absolute it looks, so recording it
	// is what stops such a reference being mistaken for one into a file that
	// would have to be fetched.
	//
	// Without it a document that names itself and then refers to itself
	// through that name loses every schema so named: the reference is not
	// local by spelling, so it is not followed, and what reaches the validator
	// is an empty schema that accepts anything. The run still passes, having
	// checked nothing — which is the one failure mode worth this much
	// machinery to avoid.
	SelfURI string

	// order is every node in document order, with subtree recording how many
	// nodes each one spans. Together they answer "what comes after this
	// subtree", which is how a diagnostic about a block value gets an end
	// position — YAML records where a node starts but not where it ends.
	order   []*yaml.Node
	index   map[*yaml.Node]int
	subtree map[*yaml.Node]int

	// lines is how many lines the source had, so a span running to the end of
	// the document has somewhere to end.
	lines int
}

// New reads a document. JSON parses too, being a subset of YAML.
func New(source []byte) (*Document, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(source, &root); err != nil {
		return nil, err
	}

	// A document node wraps the real content; unwrap so callers see the mapping.
	content := &root
	if content.Kind == yaml.DocumentNode && len(content.Content) > 0 {
		content = content.Content[0]
	}

	d := &Document{
		Root:    Node{content},
		index:   map[*yaml.Node]int{},
		subtree: map[*yaml.Node]int{},
		lines:   countLines(source),
	}
	d.indexNodes(content)
	return d, nil
}

// countLines counts the lines a source has, not counting a phantom empty one
// after a trailing newline — which is where a span running to the end of the
// document would otherwise land.
func countLines(source []byte) int {
	newlines := bytes.Count(source, []byte("\n"))
	if len(source) > 0 && !bytes.HasSuffix(source, []byte("\n")) {
		return newlines + 1
	}
	return newlines
}

// indexNodes records every node in pre-order, so a subtree occupies a
// contiguous run and its extent is a simple count.
func (d *Document) indexNodes(n *yaml.Node) int {
	if n == nil {
		return 0
	}
	position := len(d.order)
	d.index[n] = position
	d.order = append(d.order, n)

	size := 1
	for _, child := range n.Content {
		size += d.indexNodes(child)
	}
	d.subtree[n] = size
	return size
}

// Span returns the source range a diagnostic about this node covers.
//
// A scalar spans its own token, quotes included. A block node has no recorded
// end, so it runs to wherever the next token begins.
func (d *Document) Span(n Node) (startLine, startCol, endLine, endCol int) {
	if !n.Valid() {
		return 0, 0, 0, 0
	}
	startLine, startCol = n.Line, n.Column

	if n.Kind == yaml.ScalarNode {
		return startLine, startCol, startLine, startCol + ScalarWidth(n)
	}
	endLine, endCol = d.EndOf(n)
	return startLine, startCol, endLine, endCol
}

// EndOf is where a node's subtree stops: the start of whatever token follows
// it, or one past the end of the document when nothing does.
func (d *Document) EndOf(n Node) (line, column int) {
	if position, known := d.index[n.Node]; known {
		if next := position + d.subtree[n.Node]; next < len(d.order) {
			return d.order[next].Line, d.order[next].Column
		}
		return d.lines + 1, 1
	}
	return n.Line, n.Column
}

// ScalarWidth is the width of a scalar as written, counting the quotes a quoted
// style adds around it.
func ScalarWidth(n Node) int {
	width := len(n.Value)
	switch n.Style {
	case yaml.DoubleQuotedStyle, yaml.SingleQuotedStyle:
		width += 2
	}
	return width
}

// Valid reports whether the node is present.
func (n Node) Valid() bool { return n.Node != nil }

// IsMapping reports whether the node is a mapping.
func (n Node) IsMapping() bool { return n.Valid() && n.Kind == yaml.MappingNode }

// IsSequence reports whether the node is a sequence.
func (n Node) IsSequence() bool { return n.Valid() && n.Kind == yaml.SequenceNode }

// IsScalar reports whether the node is a scalar.
func (n Node) IsScalar() bool { return n.Valid() && n.Kind == yaml.ScalarNode }

// Get returns the value for a mapping key, or an absent node.
func (n Node) Get(key string) Node {
	if !n.IsMapping() {
		return Node{}
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return Node{n.Content[i+1]}
		}
	}
	return Node{}
}

// KeyNode returns the key node for a mapping entry, which is what a diagnostic
// about that entry points at.
func (n Node) KeyNode(key string) Node {
	if !n.IsMapping() {
		return Node{}
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return Node{n.Content[i]}
		}
	}
	return Node{}
}

// Entry is one key/value pair of a mapping.
type Entry struct {
	Key   Node
	Value Node
}

// Entries returns a mapping's members in document order.
//
// Order is preserved because it decides the order transactions are created in,
// which is the order tests run in.
func (n Node) Entries() []Entry {
	if !n.IsMapping() {
		return nil
	}
	out := make([]Entry, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, Entry{Key: Node{n.Content[i]}, Value: Node{n.Content[i+1]}})
	}
	return out
}

// Items returns a sequence's elements.
func (n Node) Items() []Node {
	if !n.IsSequence() {
		return nil
	}
	out := make([]Node, 0, len(n.Content))
	for _, item := range n.Content {
		out = append(out, Node{item})
	}
	return out
}

// Str returns a scalar's string value, or "".
func (n Node) Str() string {
	if !n.IsScalar() {
		return ""
	}
	return n.Value
}

// Bool returns a scalar's boolean value.
func (n Node) Bool() bool { return n.IsScalar() && n.Value == "true" }

// Resolve follows a $ref within this document.
//
// Only local references are followed. An external one is left unresolved, which
// downstream shows up as a missing schema rather than a fetch: resolving it
// would mean reading files or making network requests during what is supposed
// to be a pure parse.
func (d *Document) Resolve(n Node) Node {
	seen := 0
	for n.IsMapping() {
		ref := n.Get("$ref")
		if !ref.IsScalar() {
			return n
		}
		pointer, local := d.Local(ref.Value)
		if !local {
			return n
		}
		// A reference cycle would otherwise spin here; the document is at
		// fault, and stopping leaves the caller with the last node it reached.
		seen++
		if seen > 100 {
			return n
		}
		n = d.Pointer(pointer)
		if !n.Valid() {
			return Node{}
		}
	}
	return n
}

// Local reduces a reference to the pointer this document can answer, reporting
// false for one that points somewhere else.
//
// The two spellings mean the same thing whenever the URI part is the document's
// own: `#/components/schemas/Pet` and
// `https://example.com/api.yaml#/components/schemas/Pet` name the same schema
// in a document that calls itself `https://example.com/api.yaml`. Callers that
// judge a reference — following it, or reporting it as dangling — have to agree
// about that, so the judgement lives here rather than in each of them.
func (d *Document) Local(ref string) (pointer string, local bool) {
	if strings.HasPrefix(ref, "#/") {
		return ref, true
	}
	if d.SelfURI == "" {
		return "", false
	}
	fragment, found := strings.CutPrefix(ref, d.SelfURI+"#")
	if !found || !strings.HasPrefix(fragment, "/") {
		return "", false
	}
	return "#" + fragment, true
}

// Pointer walks a JSON Pointer such as #/components/schemas/Pet.
//
// A reference written through the document's own URI is reduced to the pointer
// part first, so the two ways of naming the same place arrive here as one. Only
// that rewriting is done: whatever else a caller passes is walked as it always
// was, and reaches an invalid node if it names nothing.
func (d *Document) Pointer(ref string) Node {
	if pointer, local := d.Local(ref); local {
		ref = pointer
	}
	current := d.Root
	for _, segment := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		if segment == "" {
			continue
		}
		segment = strings.ReplaceAll(segment, "~1", "/")
		segment = strings.ReplaceAll(segment, "~0", "~")
		current = current.Get(segment)
		if !current.Valid() {
			return Node{}
		}
	}
	return current
}

// HTTPMethods are the operation keys of a Path Item Object, in the order the
// specification lists them. A Path Item's other keys are not operations.
var HTTPMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// IsHTTPMethod reports whether a Path Item key names an operation.
func IsHTTPMethod(key string) bool {
	for _, method := range HTTPMethods {
		if key == method {
			return true
		}
	}
	return false
}
