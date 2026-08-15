// Package apidesc turns an API description document into API Elements.
//
// This is the one format-specific stage. Everything downstream — naming
// transactions, expanding URIs, running them, reporting — works on API Elements
// and neither knows nor cares which format the description was written in.
package apidesc

import (
	"regexp"

	"github.com/antimatter-studios/vertrag/internal/apidesc/openapi3"
	"github.com/antimatter-studios/vertrag/internal/refract"
)

// Media types identifying the supported description formats. They are the same
// strings Dredd uses, because they reach the compiler, which branches on them,
// and they appear in its output.
const (
	MediaTypeOpenAPI3 = "application/vnd.oai.openapi"
	// Both YAML and JSON Swagger documents report the JSON media type: the
	// reference takes the first media type its adapter declares, and does not
	// distinguish the serialisation.
	MediaTypeOpenAPI2     = "application/swagger+json"
	MediaTypeAPIBlueprint = "text/vnd.apiblueprint"
)

// Result is a parsed description document.
type Result struct {
	MediaType string
	Elements  *refract.Element
}

// Detection patterns, matching the reference adapters' own.
//
// OpenAPI 3 is recognised only by a full three-part version, so `openapi: 3.0`
// is not detected — the reference is equally strict, and loosening it here
// would make vertrag accept documents Dredd rejects.
//
// The reference writes the OpenAPI 3 pattern with backreferences, to require
// that the quotes around a token match. Go's regexp engine has none, so the
// quoted and unquoted forms are spelled out instead; enumerating them is
// exactly equivalent, since a backreference to an optional quote can only ever
// match the same quote or none.
var (
	openAPI3Pattern = regexp.MustCompile(
		`(?:openapi|"openapi"|'openapi')\s*:\s*(?:3\.\d+\.\d+|"3\.\d+\.\d+"|'3\.\d+\.\d+')`)
	openAPI2Pattern = regexp.MustCompile(`"?swagger"?\s*:\s*["']2\.0["']`)
)

// Detect identifies a document's format.
//
// A document nothing recognises is assumed to be API Blueprint, which is what
// the reference does; the caller is told through the second return value so it
// can attach the warning that assumption deserves.
func Detect(source []byte) (mediaType string, recognised bool) {
	switch {
	case openAPI3Pattern.Match(source):
		return MediaTypeOpenAPI3, true
	case openAPI2Pattern.Match(source):
		return MediaTypeOpenAPI2, true
	default:
		return MediaTypeAPIBlueprint, false
	}
}

// Implemented reports whether a format has a parser.
//
// It exists so the differential tests can say plainly which formats are not
// covered yet, rather than reporting a wall of failures that all mean the same
// thing.
func Implemented(mediaType string) bool {
	return mediaType == MediaTypeOpenAPI3
}

// Parse reads a description document into API Elements.
func Parse(source []byte, filename string) (Result, error) {
	mediaType, recognised := Detect(source)

	switch mediaType {
	case MediaTypeOpenAPI3:
		elements, err := openapi3.Parse(source)
		if err != nil {
			return Result{}, err
		}
		return Result{MediaType: mediaType, Elements: elements}, nil

	default:
		// The remaining formats have no parser yet. An empty parse result
		// carrying the reason is returned rather than an error, so the caller
		// reports it the way it reports any other unusable document instead of
		// crashing.
		reason := "OpenAPI 2 documents are not supported yet"
		if !recognised {
			reason = "Could not recognize API description format, assuming API Blueprint"
		}
		result := refract.Named("parseResult", annotationElement("error", reason))
		return Result{MediaType: mediaType, Elements: result}, nil
	}
}

func annotationElement(class, message string) *refract.Element {
	annotation := refract.Text("annotation", message)
	annotation.AddClass(class)
	return annotation
}
