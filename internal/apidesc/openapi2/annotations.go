package openapi2

import (
	"fmt"
	"strings"

	"github.com/antimatter-studios/vertrag/internal/refract"
)

// annotation is a diagnostic about the description document.
//
// Positions are 0-based here, unlike the OpenAPI 3 parser's 1-based ones. That
// is not a mistake in either: the two reference parsers were written separately
// and report differently, and the numbers reach users, so each is reproduced as
// it is rather than made consistent.
type annotation struct {
	class   string
	message string
	line    int
	column  int
	endLine int
	endCol  int
	located bool
}

// validate walks the document for the diagnostics the reference raises.
func (d *document) validate() []annotation {
	var out []annotation

	root := d.root
	if !root.isMapping() {
		return out
	}

	// Only one scheme can become a hostname, so listing several means all but
	// the first are ignored.
	if schemes := root.get("schemes"); len(schemes.items()) > 1 {
		out = append(out, d.at(annotation{
			class:   "warning",
			message: "Only the first scheme will be used to create a hostname",
		}, root.keyNode("schemes"), schemes))
	}

	for _, path := range root.get("paths").entries() {
		pathItem := path.value
		if !pathItem.isMapping() {
			continue
		}

		out = append(out, d.validateParameters(path.key.str(), "", pathItem.get("parameters"))...)

		for _, member := range pathItem.entries() {
			method := member.key.str()
			if !isHTTPMethod(method) {
				continue
			}
			operation := member.value

			out = append(out, d.validateParameters(path.key.str(), method, operation.get("parameters"))...)

			// A response with no status code says nothing about what to
			// assert, so it is skipped — and saying so is the difference
			// between a deliberate choice and silently dropping it.
			responses := operation.get("responses")
			if key := responses.keyNode("default"); key.valid() {
				out = append(out, d.at(annotation{
					class:   "warning",
					message: "Default response is not yet supported",
				}, key, responses.get("default")))
			}
		}
	}

	return out
}

// validateParameters reports path parameters the path template has no place for.
//
// A declared `in: path` parameter with no matching {name} cannot be substituted
// anywhere, so the document is describing a request it has not said how to
// build. That is an error, not a warning: there is no sensible URI to fall back
// to.
func (d *document) validateParameters(path, method string, n node) []annotation {
	var out []annotation

	for _, item := range n.items() {
		resolved := d.resolve(item)
		if resolved.get("in").str() != "path" {
			continue
		}
		name := resolved.get("name").str()
		if name == "" || strings.Contains(path, "{"+name+"}") {
			continue
		}

		location := "/paths" + path
		if method != "" {
			location += "/" + method
		}
		out = append(out, annotation{
			class: "error",
			message: fmt.Sprintf(
				"Validation failed. %s has a path parameter named %q, but there is no corresponding {%s} in the path string",
				location, name, name),
		})
	}
	return out
}

// at attaches the source range of a whole mapping entry, converted to the
// 0-based positions this format's diagnostics use.
//
// The range runs from the key to the end of its value, not just across the key:
// a diagnostic about `schemes` covers the list beneath it, which is what a
// reader needs to see highlighted.
func (d *document) at(a annotation, key, value node) annotation {
	if !key.valid() {
		return a
	}

	endLine, endCol := key.Line, key.Column+rawScalarWidth(key)
	if value.valid() {
		endLine, endCol = d.endOf(value)
	}

	a.line, a.column = key.Line-1, key.Column-1
	a.endLine, a.endCol = endLine-1, endCol-1
	a.located = true
	return a
}

// errorsOnly selects the error-class diagnostics.
func errorsOnly(annotations []annotation) []annotation {
	var out []annotation
	for _, a := range annotations {
		if a.class == "error" {
			out = append(out, a)
		}
	}
	return out
}

// annotationElements renders diagnostics as API Elements annotations.
func annotationElements(annotations []annotation) []*refract.Element {
	out := make([]*refract.Element, 0, len(annotations))
	for _, a := range annotations {
		element := refract.Text("annotation", a.message)
		element.AddClass(a.class)
		if a.located {
			element.SetSourceMap(a.line, a.column, a.endLine, a.endCol)
		}
		out = append(out, element)
	}
	return out
}

// yamlErrorMessage renders a YAML failure the way the reference's parser does.
//
// The two use different YAML libraries, so their wording differs in general.
// The one case worth translating is an unterminated flow collection, which is
// what an unclosed `{` or `[` produces and by far the most common way a hand-
// edited document fails to parse. Anything else keeps Go's message: a
// recognisable diagnostic in the wrong words beats a wrong one in the right
// words.
func yamlErrorMessage(source []byte, err error) string {
	if hasUnterminatedFlowCollection(source) {
		return "unexpected end of the stream within a flow collection"
	}
	return strings.TrimPrefix(err.Error(), "yaml: ")
}

// hasUnterminatedFlowCollection reports whether a `{` or `[` is left open,
// ignoring anything inside quotes or a comment.
func hasUnterminatedFlowCollection(source []byte) bool {
	depth := 0
	inSingle, inDouble, inComment := false, false, false

	for i := 0; i < len(source); i++ {
		c := source[i]

		if inComment {
			if c == '\n' {
				inComment = false
			}
			continue
		}
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case inDouble:
			if c == '\\' {
				i++
			} else if c == '"' {
				inDouble = false
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '#':
			inComment = true
		case c == '{', c == '[':
			depth++
		case c == '}', c == ']':
			depth--
		}
	}
	return depth > 0
}
