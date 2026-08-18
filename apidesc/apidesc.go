// Package apidesc turns an API description document into API Elements.
//
// This is the one format-specific stage. Everything downstream — naming
// transactions, expanding URIs, running them, reporting — works on API Elements
// and neither knows nor cares which format the description was written in.
package apidesc

import (
	"regexp"

	"github.com/antimatter-studios/vertrag/apidesc/openapi2"
	"github.com/antimatter-studios/vertrag/apidesc/openapi3"
	"github.com/antimatter-studios/vertrag/refract"
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
//
// The patch part is optional here where the reference requires it. The
// specification does say `openapi` MUST be major.minor.patch, so `openapi:
// 3.0` is a malformed document — but it appears in the wild, and the reference
// pattern's answer to it is to fall through to API Blueprint and report "API
// Blueprint is not supported". That is the least useful thing a tool can say
// about an OpenAPI 3 file. It is read as OpenAPI 3 and the malformed version is
// reported for what it is: see openapi3's version check.
var (
	openAPI3Pattern = regexp.MustCompile(
		`(?:openapi|"openapi"|'openapi')\s*:\s*(?:3\.\d+(?:\.\d+)?|"3\.\d+(?:\.\d+)?"|'3\.\d+(?:\.\d+)?')`)
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
	return mediaType == MediaTypeOpenAPI3 || mediaType == MediaTypeOpenAPI2
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

	case MediaTypeOpenAPI2:
		elements, err := openapi2.Parse(source)
		if err != nil {
			return Result{}, err
		}
		return Result{MediaType: mediaType, Elements: elements}, nil
	}

	// API Blueprint is deliberately unsupported rather than unfinished. The
	// format is archived, as is its only parser, which is 2 MB of
	// Emscripten-compiled C++ — linking it would end the static binary, and
	// reimplementing a Markdown-based format is not worth doing for a format
	// nobody is writing any more.
	//
	// Reporting that through the parse result rather than as an error means the
	// caller shows it the way it shows any other unusable document.
	reason := "API Blueprint is not supported; vertrag reads OpenAPI 3 and OpenAPI 2"
	if !recognised {
		reason = "Could not recognize the API description format"
	}
	result := refract.Named("parseResult", annotationElement("error", reason))
	return Result{MediaType: mediaType, Elements: result}, nil
}

func annotationElement(class, message string) *refract.Element {
	annotation := refract.Text("annotation", message)
	annotation.AddClass(class)
	return annotation
}
