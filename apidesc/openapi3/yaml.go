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

	// minor is the revision of OpenAPI 3 the document declares — the 2 in
	// `3.2.0` — and is -1 when the version line is absent or unreadable.
	//
	// Most of what 3.2 added is accepted whatever the document claims to be,
	// on the reasoning already applied to 3.1's `info.summary`: refusing a key
	// that harms nothing buys nothing. Two of its additions are different,
	// because they name operations vertrag then SENDS. `query` and
	// `additionalOperations` in a 3.0 document are a key in the wrong place,
	// and reading them there would turn a mistake worth reporting into
	// requests the document never described. So those two, and the response
	// `description` that 3.2 stopped requiring, are decided by the revision.
	minor int
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
func isModernVersion(version string) bool { return minorVersion(version) >= 1 }

// minorVersion reads the minor part of an OpenAPI 3 version, or -1 when there
// is no OpenAPI 3 version to read. A document whose version line is missing or
// malformed is still read — see validateVersion — so this has to answer for one
// rather than refuse it, and -1 is the answer that puts such a document on the
// oldest rules, where a key from a later revision is still reported.
func minorVersion(version string) int {
	major, rest, found := strings.Cut(version, ".")
	if !found || major != "3" {
		return -1
	}
	minor, _, _ := strings.Cut(rest, ".")
	value, err := strconv.Atoi(minor)
	if err != nil {
		return -1
	}
	return value
}

// atLeast reports whether the document declares OpenAPI 3.minor or later.
func (d *document) atLeast(minor int) bool { return d.minor >= minor }

func parseDocument(source []byte) (*document, error) {
	parsed, err := yamldoc.New(source)
	if err != nil {
		return nil, err
	}
	doc := &document{Document: parsed}
	version := doc.Root.Get("openapi").Str()
	doc.minor = minorVersion(version)
	doc.modernSchemas = isModernVersion(version)

	// 3.2's `$self` is the URI the document gives itself, and the reader needs
	// it to tell a reference into this document from one into another file. It
	// is taken whatever the declared revision, because a document that names
	// itself and refers to itself by that name means the same thing in every
	// revision — and the alternative is losing the schema it names.
	doc.SelfURI = doc.Root.Get("$self").Str()
	return doc, nil
}

func isHTTPMethod(key string) bool { return yamldoc.IsHTTPMethod(key) }

// additionalOperationsKey is where 3.2 put the operations whose methods the
// specification does not name a field for — LINK, PURGE, and whatever else a
// server answers. The map key is the method, spelled as it goes on the wire.
const additionalOperationsKey = "additionalOperations"

// isOperationKey reports whether a Path Item key names an operation.
//
// 3.2 added QUERY, a method with a request body that is nonetheless safe, and
// gave it a field of its own beside `get` and `post`. It is only an operation
// in a document that claims 3.2 or later: `query` under an older Path Item is a
// key the specification has no meaning for, and reporting it is more use to its
// author than quietly sending a request they did not ask for.
func (d *document) isOperationKey(key string) bool {
	return isHTTPMethod(key) || (key == "query" && d.atLeast(2))
}

func rawScalarWidth(n node) int { return yamldoc.ScalarWidth(n) }
