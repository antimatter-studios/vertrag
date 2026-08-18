// Package apidesc reads an API description document.
//
// This is the one format-specific stage. OpenAPI 2 and 3 become API Elements,
// and everything downstream of them — naming transactions, expanding URIs,
// running them, reporting — neither knows nor cares which of the two the
// description was written in.
//
// GraphQL is the exception, and deliberately: a schema has no resources, no
// URIs and no methods, so API Elements has nowhere honest to put it. It is
// read into a schema instead and compiled straight to transactions, which is
// the same output the API Elements path produces — so everything downstream of
// THAT is shared, which is the part that matters. The reasoning is set out in
// full at the top of compile/graphql.go.
package apidesc

import (
	"regexp"

	"github.com/antimatter-studios/vertrag/apidesc/graphql"
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

	// MediaTypeGraphQL is a GraphQL schema. No media type for one is
	// registered — `application/graphql` is what the ecosystem's tooling uses
	// for a GraphQL document and is what a `.graphql` file is served as — and
	// none needs to be: this string is only ever compared against itself,
	// since it is what the compiler branches on and what the report prints.
	MediaTypeGraphQL = "application/graphql"

	// MediaTypeUnknown is what a document nothing recognises is reported as.
	// It is deliberately not a real media type: naming it after some format
	// vertrag might have guessed is how the Blueprint fallback came to label
	// every unreadable file as Blueprint.
	MediaTypeUnknown = ""
)

// Result is a parsed description document.
//
// A document arrives as one of two things, and which one follows from its
// format. Everything that describes resources and methods becomes API
// Elements; a GraphQL schema becomes a schema, because it has neither and
// making it pretend to would decide the transaction names — see the reasoning
// at the top of compile/graphql.go. Exactly one of the two fields is set.
type Result struct {
	MediaType string
	Elements  *refract.Element

	// Schema is the GraphQL schema, when that is what was read.
	Schema *graphql.Schema

	// Warnings are what the GraphQL reader could not act on. The API Elements
	// path has no equivalent field because its parsers put the same thing in
	// the tree as annotation elements; the caller turns these into the same
	// diagnostics.
	Warnings []string
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

	// The API Blueprint version line, which its own tooling writes at the top
	// of the file. Matching it is not a step towards supporting the format —
	// it is how a Blueprint document gets an answer about Blueprint instead of
	// the generic "this is not a description vertrag reads".
	blueprintPattern = regexp.MustCompile(`(?m)^FORMAT:\s*1A`)
)

// Detect identifies a document's format.
//
// A document nothing recognises is reported as unrecognised, and used to be
// assumed to be API Blueprint — which is what the reference does, because
// Blueprint is a format it supports and the likeliest thing an unlabelled
// document was. vertrag does not support Blueprint and never will, so that
// assumption bought nothing and cost accuracy: every unreadable file, of any
// kind, was labelled Blueprint on the way to being rejected.
//
// Blueprint is still detected, by the version line its own tooling writes,
// so that a document that really is one gets told why it cannot be read
// rather than the generic answer.
func Detect(source []byte) (mediaType string, recognised bool) {
	switch {
	case openAPI3Pattern.Match(source):
		return MediaTypeOpenAPI3, true
	case openAPI2Pattern.Match(source):
		return MediaTypeOpenAPI2, true
	case blueprintPattern.Match(source):
		// Recognised, and unsupported: two different things, which is the
		// distinction this return value exists to carry.
		return MediaTypeAPIBlueprint, true
	// Asked last of the four, and asked of the parser rather than answered
	// here. SDL has no version line to match on, so its patterns are the
	// loosest vertrag has and the loosest question is the one to ask last —
	// and they belong to the reader that has to make sense of what they
	// matched, not to a second copy in this file that could drift from it.
	case graphql.Detect(source):
		return MediaTypeGraphQL, true
	default:
		return MediaTypeUnknown, false
	}
}

// Implemented reports whether a format has a parser.
//
// It exists so the differential tests can say plainly which formats are not
// covered yet, rather than reporting a wall of failures that all mean the same
// thing.
func Implemented(mediaType string) bool {
	return mediaType == MediaTypeOpenAPI3 ||
		mediaType == MediaTypeOpenAPI2 ||
		mediaType == MediaTypeGraphQL
}

// Parse reads a description document into whichever of the two forms its
// format has: API Elements, or a GraphQL schema.
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

	case MediaTypeGraphQL:
		// The one format that does not become API Elements. It is routed
		// straight to its own compiler instead, for the reason set out at the
		// top of compile/graphql.go: a schema has no resources, no URIs and no
		// methods, and inventing them would decide the transaction names —
		// which are what hooks and `--only` address.
		//
		// The reader's warnings are carried out with it rather than dropped.
		// They are everything the schema says that testing cannot act on — an
		// unmodelled directive, a root type that is not an object, a field
		// whose type is never defined — and each one is a hole in a run that
		// would otherwise look exactly like a pass.
		schema, warnings, err := graphql.Parse(source)
		if err != nil {
			return Result{}, err
		}
		return Result{MediaType: mediaType, Schema: schema, Warnings: warnings}, nil
	}

	// Two different answers, because they call for two different actions.
	//
	// A document that IS API Blueprint cannot be read and never will be: the
	// format is archived, as is its only parser, which is 2 MB of
	// Emscripten-compiled C++ — linking it would end the static binary, and
	// reimplementing a Markdown-based format is not worth doing for a format
	// nobody writes any more. Nothing the author can do to the file will help,
	// so the message says so plainly.
	//
	// A document nothing recognises is a different problem, and usually a
	// smaller one: a typo in the version line, a file that is not a
	// description at all, the wrong path. So it says what vertrag looked for.
	//
	// Reporting either through the parse result rather than as an error means
	// the caller shows it the way it shows any other unusable document.
	reason := "the API description format could not be recognised; " +
		"vertrag reads OpenAPI 3 (`openapi: 3.x.x`), OpenAPI 2 (`swagger: \"2.0\"`) " +
		"and GraphQL schemas (a `schema { … }` block or a `type Query`)"
	if recognised && mediaType == MediaTypeAPIBlueprint {
		reason = "this is API Blueprint, which vertrag does not support: the format and " +
			"its only parser are archived. Convert it to OpenAPI, or keep using Dredd for it"
	}
	result := refract.Named("parseResult", annotationElement("error", reason))
	return Result{MediaType: mediaType, Elements: result}, nil
}

func annotationElement(class, message string) *refract.Element {
	annotation := refract.Text("annotation", message)
	annotation.AddClass(class)
	return annotation
}
