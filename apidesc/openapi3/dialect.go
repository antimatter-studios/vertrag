package openapi3

import (
	"fmt"
	"strings"

	"github.com/antimatter-studios/vertrag/validate"
)

// The dialect a document's Schema Objects are written in, and the one the
// emitted JSON Schema is stamped with.
//
// Every schema vertrag produces carries a `$schema`, and the validator picks
// its rules from that and nothing else. So this is not bookkeeping: it decides
// whether `const` is a constraint or an annotation, whether `exclusiveMinimum`
// is a number or a flag beside `minimum`, and whether a sibling of `$ref` is
// honoured or ignored. Getting it wrong does not fail loudly — it validates a
// body under rules the document was not written in, and the result is a pass.
const (
	// dialect2020 is what OpenAPI 3.1 aligned its Schema Object with, and the
	// default for a 3.1-or-later document that names none.
	dialect2020 = "https://json-schema.org/draft/2020-12/schema"

	// dialectDraft4 is 3.0's, whose Schema Object is a modified subset of it.
	dialectDraft4 = "http://json-schema.org/draft-04/schema#"
)

// resolveDialect decides what `$schema` this document's schemas are stamped
// with, and reports the declaration it could not honour.
//
// `jsonSchemaDialect` is 3.1's, and it exists because 3.1 made the Schema
// Object real JSON Schema: a document may say which dialect that is, and the
// specification's own default is the OAS base dialect — 2020-12 plus a
// vocabulary of OpenAPI's annotations, which assert nothing a body can
// violate. That default is what vertrag already did, so a document declaring
// it changes nothing.
//
// A document declaring something else is the interesting case, and there are
// two kinds. One vertrag can honour: the validator implements draft-04 through
// 2020-12, so a 3.1 document written in draft-07 is read as draft-07 rather
// than as the 2020-12 it never claimed — which is the point of acting on the
// field at all. The other it cannot: a custom meta-schema naming vocabularies
// nobody here implements. That one is warned about rather than passed through,
// because passing it through is worse than ignoring it — the validator reads a
// URI it does not recognise as draft-04, so a 2020-12 document with a bespoke
// dialect line would have every one of its constraints read under the oldest
// rules and quietly under-enforced.
func (d *document) resolveDialect(declared string) (string, *annotation) {
	fallback := dialectDraft4
	if d.modernSchemas {
		fallback = dialect2020
	}

	declared = strings.TrimSpace(declared)
	switch {
	case declared == "":
		return fallback, nil

	case !d.modernSchemas:
		// The field is 3.1's, and acting on it earlier would be worse than
		// refusing to: a 3.0 Schema Object is not JSON Schema but a subset with
		// its own spellings — `nullable`, `exclusiveMinimum` as a flag — and
		// stamping 2020-12 on the conversion of one produces a schema the
		// validator cannot compile, so the body it described goes unchecked.
		return fallback, &annotation{
			class: "warning",
			message: fmt.Sprintf(
				"'OpenAPI Object' 'jsonSchemaDialect' is OpenAPI 3.1's, and this document "+
					"declares %q; its schemas are read as draft-04, which is the dialect "+
					"OpenAPI 3.0's Schema Object is a subset of", d.Root.Get("openapi").Str()),
		}

	case isOASDialect(declared):
		// The specification's own default, and every revision's spelling of it.
		// It is 2020-12 with OpenAPI's vocabulary layered on, and that
		// vocabulary is `discriminator`, `xml`, `externalDocs` and `example` —
		// annotations, not assertions, and already reported as unsupported
		// where they change nothing.
		return dialect2020, nil

	case validate.KnownDialect(declared):
		return declared, nil

	default:
		return fallback, &annotation{
			class: "warning",
			message: fmt.Sprintf(
				"'OpenAPI Object' 'jsonSchemaDialect' names %q, which vertrag does not "+
					"implement; the schemas in this document are read as %s, so a constraint "+
					"that dialect defines and 2020-12 does not is not enforced",
				declared, fallback),
		}
	}
}

// isOASDialect reports whether a URI names the OAS base dialect of some
// revision.
//
// Matched by shape rather than by a list of URIs, because the URI carries the
// revision — 3.1's is `.../oas/3.1/dialect/base` — and a list would have to
// grow with every revision to keep saying the same thing. The revisions agree
// about what it means: 2020-12, plus OpenAPI's own annotation vocabulary.
func isOASDialect(uri string) bool {
	return strings.Contains(uri, "spec.openapis.org/oas/") && strings.Contains(uri, "/dialect/")
}
