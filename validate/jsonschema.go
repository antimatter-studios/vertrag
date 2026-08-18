package validate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// englishPrinter renders the library's error kinds. Diagnostics are English
// because the rest of vertrag's output is; the library supports localisation if
// that ever changes.
var englishPrinter = message.NewPrinter(language.English)

// JSON Schema validation.
//
// The checking itself is santhosh-tekuri/jsonschema, which passes the official
// JSON-Schema-Test-Suite across draft-4 through 2020-12. An earlier version of
// this file hand-rolled a subset — type, required, properties, items, enum —
// and silently accepted bodies that violated `pattern`, `minLength`, `maximum`,
// `format`, `additionalProperties` and every composition keyword. A contract
// tester that quietly under-checks is worse than one that does not check at
// all, because the passing result is believed.
//
// What is written here is the mapping from the library's errors to the messages
// a user reads. Those are deliberately vertrag's own rather than a reproduction
// of Dredd's: Dredd inherits its wording from two different validators and
// reports the same class of problem two different ways depending on the
// keyword. There is no reason to carry that across.

// draftFor selects the dialect a schema is read under.
//
// OpenAPI 3.0 documents yield draft-04 schemas and usually say so; 3.1 aligns
// with 2020-12. A schema that declares neither is read as draft-04, which is
// what the OpenAPI 3.0 path produces and therefore the safer assumption.
func draftFor(schema map[string]any) *jsonschema.Draft {
	declared, _ := schema["$schema"].(string)
	if draft, known := draftNamed(declared); known {
		return draft
	}
	return jsonschema.Draft4
}

// draftNamed reads a `$schema` URI, and says whether it named anything.
//
// Split from draftFor so that the answer to "would this be recognised?" comes
// from the same table as the answer to "read it as what?". A parser choosing
// the `$schema` it stamps has to know which dialects survive the trip: an
// unrecognised URI is not refused here, it is silently read as draft-04, so a
// description declaring a dialect nobody implements would have its 2020-12
// schemas validated under draft-04 rules and pass bodies that violate them.
func draftNamed(declared string) (*jsonschema.Draft, bool) {
	switch {
	case strings.Contains(declared, "2020-12"):
		return jsonschema.Draft2020, true
	case strings.Contains(declared, "2019-09"):
		return jsonschema.Draft2019, true
	case strings.Contains(declared, "draft-07"):
		return jsonschema.Draft7, true
	case strings.Contains(declared, "draft-06"):
		return jsonschema.Draft6, true
	case strings.Contains(declared, "draft-04"):
		return jsonschema.Draft4, true
	default:
		return nil, false
	}
}

// KnownDialect reports whether a `$schema` URI names a dialect this validator
// implements.
//
// Exported for the description parsers, which decide what `$schema` to stamp on
// the schemas they emit. OpenAPI 3.1 lets a document name its own dialect, and
// passing that name through unexamined is the failure this answers: the
// validator has no way to say "I do not know this one" at validation time —
// it reads an unfamiliar URI as draft-04 and carries on — so the question has
// to be asked while there is still a document to warn about.
func KnownDialect(uri string) bool {
	_, known := draftNamed(uri)
	return known
}

// AgainstSchema reports whether a body satisfies a JSON Schema.
//
// Exported for generation, which has to know whether the value it just drew is
// really valid or really invalid before it can judge what the server did with
// it. Without that check a generator bug reads as a server bug: a body meant to
// be invalid that is in fact valid, accepted with a 201, looks exactly like a
// server ignoring its own constraints.
func AgainstSchema(schema json.RawMessage, body string) FieldResult {
	return validateAgainstSchema(schema, body)
}

func validateAgainstSchema(schema json.RawMessage, body string) FieldResult {
	var document any
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		// A body that claims to be JSON but will not parse is its own kind of
		// failure, reported with no kind at all: there was no structure to
		// compare, so calling it a JSON comparison would misdescribe what
		// happened.
		return FieldResult{
			Kind: nil,
			Errors: []string{
				fmt.Sprintf("the response declares JSON but the body does not parse: %s", err)},
		}
	}
	return validateValue(schema, document, "the response body")
}

// validateValue checks an already-decoded value against a schema.
//
// `root` names what the value is, for the failures that occur at its very top
// rather than at some path inside it. A body says "the response body"; a header
// passes "" because the caller has already named the header and repeating it
// would only make the finding harder to read.
func validateValue(schema json.RawMessage, document any, root string) FieldResult {
	field := FieldResult{Valid: true, Kind: kind("json"), Errors: []string{}}

	compiled, err := compileSchema(schema)
	if err != nil {
		// A schema vertrag cannot read says nothing about the response, so
		// failing here would blame the server for the description's problem.
		// It is not silent, though: the compiler reports an unusable schema as
		// a warning about the document when it reads it — see Usable — which
		// is once, up front, and names the operation.
		return field
	}

	if err := compiled.Validate(document); err != nil {
		field.Valid = false
		field.Errors = append(field.Errors, describe(err, root)...)
	}
	return field
}

// compileSchema prepares a schema for repeated use.
// Usable reports whether a schema can be compiled, and why not when it
// cannot.
//
// It exists so a description can be told about an unusable schema ONCE, when
// it is read, rather than never. Validation itself cannot report it: a schema
// vertrag cannot read says nothing about the response, so failing the
// transaction would blame the server for the description's problem — but
// passing it silently tells the reader their body was checked when nothing
// checked it, and a green run nobody can trust is worse than a red one.
func Usable(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	_, err := compileSchema(raw)
	return err
}

func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(draftFor(schema))

	// The name is arbitrary but has to be stable: `definitions` entries are
	// referenced as "#/definitions/X" relative to it, which is how both
	// OpenAPI parsers emit a schema that points at its own definitions block.
	const name = "schema.json"
	if err := compiler.AddResource(name, any(schema)); err != nil {
		return nil, err
	}
	return compiler.Compile(name)
}

// describe turns a validation failure into one message per underlying problem.
//
// The library reports a tree — a failing `anyOf` carries the failure of every
// branch beneath it. The leaves are what a reader can act on, so those are what
// is reported, each prefixed with where in the body it occurred.
func describe(err error, root string) []string {
	failure, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []string{err.Error()}
	}

	messages := leaves(failure, root)
	if len(messages) == 0 {
		messages = append(messages, failure.Error())
	}
	return messages
}

// leaves collects the deepest failures in the tree.
//
// An intermediate node says only that something beneath it failed — a `$ref`
// reports "validation failed" and a `properties` node reports nothing a reader
// can act on. Descending to the leaves is what turns that into "missing
// property 'a'", which is the part worth printing.
func leaves(failure *jsonschema.ValidationError, root string) []string {
	if len(failure.Causes) > 0 {
		var messages []string
		for _, cause := range failure.Causes {
			messages = append(messages, leaves(cause, root)...)
		}
		if len(messages) > 0 {
			return messages
		}
	}

	if failure.ErrorKind == nil {
		return nil
	}
	reason := failure.ErrorKind.LocalizedString(englishPrinter)
	if reason == "" {
		return nil
	}

	where := location(pointerOf(failure.InstanceLocation), root)
	if where == "" {
		return []string{reason}
	}
	return []string{fmt.Sprintf("%s: %s", where, reason)}
}

// pointerOf renders the library's path segments as a JSON Pointer.
func pointerOf(segments []string) string {
	if len(segments) == 0 {
		return ""
	}
	var b strings.Builder
	for _, segment := range segments {
		b.WriteByte('/')
		segment = strings.ReplaceAll(segment, "~", "~0")
		b.WriteString(strings.ReplaceAll(segment, "/", "~1"))
	}
	return b.String()
}

// location renders where in the value a failure occurred, in a form a reader can
// follow back into it. A failure at the very top has no pointer to give, so the
// caller's name for the whole value stands in.
func location(pointer, root string) string {
	if pointer == "" || pointer == "/" {
		return root
	}
	return pointer
}
