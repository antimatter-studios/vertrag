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
	switch {
	case strings.Contains(declared, "2020-12"):
		return jsonschema.Draft2020
	case strings.Contains(declared, "2019-09"):
		return jsonschema.Draft2019
	case strings.Contains(declared, "draft-07"):
		return jsonschema.Draft7
	case strings.Contains(declared, "draft-06"):
		return jsonschema.Draft6
	default:
		return jsonschema.Draft4
	}
}

func validateAgainstSchema(schema json.RawMessage, body string) FieldResult {
	field := FieldResult{Valid: true, Kind: kind("json"), Errors: []string{}}

	var document any
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		// A body that claims to be JSON but will not parse is its own kind of
		// failure, reported with no kind at all: there was no structure to
		// compare, so calling it a JSON comparison would misdescribe what
		// happened.
		field.Valid = false
		field.Kind = nil
		field.Errors = append(field.Errors,
			fmt.Sprintf("the response declares JSON but the body does not parse: %s", err))
		return field
	}

	compiled, err := compileSchema(schema)
	if err != nil {
		// A schema vertrag cannot read says nothing about the response. Failing
		// here would blame the server for the description's problem.
		return field
	}

	if err := compiled.Validate(document); err != nil {
		field.Valid = false
		field.Errors = append(field.Errors, describe(err)...)
	}
	return field
}

// compileSchema prepares a schema for repeated use.
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
func describe(err error) []string {
	failure, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []string{err.Error()}
	}

	messages := leaves(failure)
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
func leaves(failure *jsonschema.ValidationError) []string {
	if len(failure.Causes) > 0 {
		var messages []string
		for _, cause := range failure.Causes {
			messages = append(messages, leaves(cause)...)
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
	return []string{fmt.Sprintf("%s: %s", location(pointerOf(failure.InstanceLocation)), reason)}
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

// location renders where in the body a failure occurred, in a form a reader can
// follow back into the payload.
func location(pointer string) string {
	if pointer == "" || pointer == "/" {
		return "the response body"
	}
	return pointer
}
