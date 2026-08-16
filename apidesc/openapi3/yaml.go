package openapi3

import (
	"strconv"
	"strings"

	"github.com/antimatter-studios/vertrag/yamldoc"
)

// The document reader is shared with the other format parser. These aliases
// keep this package reading in its own terms — a parser talks about nodes and
// entries, not about which package they came from.
type (
	node  = yamldoc.Node
	entry = yamldoc.Entry
	// orderedMap preserves key order, which matters because generated bodies
	// and schemas are compared as strings.
	orderedMap = yamldoc.OrderedMap
)

var newOrderedMap = yamldoc.NewOrderedMap

// document is a parsed description document: the shared reader, plus this
// format's own walk over it.
type document struct {
	*yamldoc.Document

	// modernSchemas records that this is OpenAPI 3.1 or later, where a Schema
	// Object IS JSON Schema 2020-12 rather than the modified subset 3.0
	// defined. The difference is not cosmetic: `nullable` no longer exists,
	// `type` may be a list, `const` is available, and `exclusiveMinimum` is a
	// number where 3.0 had a boolean beside `minimum`. Reading a 3.1 document
	// under 3.0's rules produces bodies the document never described, and
	// validates them against a dialect it was not written in.
	modernSchemas bool
}

// jsonSchemaDialect is the dialect this document's schemas are written in, and
// the one a validator should read them under.
func (d *document) jsonSchemaDialect() string {
	if d.modernSchemas {
		return "https://json-schema.org/draft/2020-12/schema"
	}
	return "http://json-schema.org/draft-04/schema#"
}

// isModernVersion reports whether a version is 3.1 or later.
func isModernVersion(version string) bool {
	major, rest, found := strings.Cut(version, ".")
	if !found || major != "3" {
		return false
	}
	minor, _, _ := strings.Cut(rest, ".")
	value, err := strconv.Atoi(minor)
	return err == nil && value >= 1
}

func parseDocument(source []byte) (*document, error) {
	parsed, err := yamldoc.New(source)
	if err != nil {
		return nil, err
	}
	doc := &document{Document: parsed}
	doc.modernSchemas = isModernVersion(doc.Root.Get("openapi").Str())
	return doc, nil
}

func isHTTPMethod(key string) bool { return yamldoc.IsHTTPMethod(key) }

func rawScalarWidth(n node) int { return yamldoc.ScalarWidth(n) }
