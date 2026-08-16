package validate

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Response header schemas.
//
// An OpenAPI Response Object may give each of its headers a full JSON Schema,
// and neither Dredd nor Gavel ever looks at one: Dredd checks that a declared
// header is present and compares no value but the content type's. A server
// answering `X-Rate-Limit: banana` where the description promised a
// non-negative integer passes.
//
// The difficulty is that a header is text and a schema describes JSON, so the
// text has to be read back into a value before a schema means anything at all.
// Applying the schema to the raw string instead would fail every correct server
// on earth, since "42" is not a JSON integer. Reading it wrongly is barely
// better: a contract test that fails servers doing exactly what they were told
// gets its assertions deleted, and then it is checking nothing while looking as
// though it checks everything.
//
// So the reading here is deliberately narrow. OpenAPI gives a header the
// `simple` style by default, which renders a primitive as its plain text and an
// array as those renderings separated by commas. Only that is decoded, only for
// the types it renders unambiguously, and anything else — an object, a
// composition, a schema with no type — is left entirely alone. Checking nothing
// is the safe failure mode; guessing is not.

// AgainstHeaderSchemas reports where a response's headers contradict the schemas
// the description gave them.
//
// Presence is not checked here. Gavel already demands every header a description
// declares, so a missing one — required or not — is reported by ordinary
// validation, and saying it a second time would only make a report harder to
// read.
func AgainstHeaderSchemas(schemas map[string]json.RawMessage, headers map[string]string) []string {
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}
	// Sorted so two runs of the same failure read the same way.
	sort.Strings(names)

	var findings []string
	for _, name := range names {
		value, present := lookupHeader(headers, name)
		if !present {
			continue
		}
		findings = append(findings, checkHeaderValue(name, value, schemas[name])...)
	}
	return findings
}

func checkHeaderValue(name, raw string, schema json.RawMessage) []string {
	var declared map[string]any
	if err := json.Unmarshal(schema, &declared); err != nil {
		return nil
	}

	value, wellFormed, readable := simpleValue(declared, raw)
	if !readable {
		return nil
	}
	if !wellFormed {
		return []string{fmt.Sprintf("the %s header is %q, which is not %s the description promises",
			name, raw, subject(declared))}
	}

	field := validateValue(schema, value, "")
	if field.Valid {
		return nil
	}

	findings := make([]string, 0, len(field.Errors))
	for _, reason := range field.Errors {
		findings = append(findings, fmt.Sprintf("the %s header is %q: %s", name, raw, reason))
	}
	return findings
}

// simpleValue decodes a header's text as the type its schema declares, following
// the `simple` style OpenAPI gives headers by default.
//
// The three outcomes are distinct and all three matter. `readable` false means
// the schema describes something this does not know how to read off the wire, in
// which case the header must be left alone: any verdict would be a guess.
// `readable` true with `wellFormed` false means the text cannot be the declared
// type however it is read, which is the violation this check exists to catch.
func simpleValue(declared map[string]any, raw string) (value any, wellFormed, readable bool) {
	switch schemaType(declared) {
	case "string":
		// The only type that needs no decoding at all, and so the only one where
		// every constraint in the schema — pattern, length, enum — applies to
		// exactly the bytes the server sent.
		return raw, true, true

	case "integer":
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, false, true
		}
		// A float64, because that is what the schema library sees when it reads
		// a number out of JSON, and draft-04 counts an integral float as an
		// integer.
		return float64(n), true, true

	case "number":
		n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		// Go parses "NaN" and "Inf" happily; JSON has no way to write either, so
		// a header containing one is not a number any schema could describe.
		if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
			return nil, false, true
		}
		return n, true, true

	case "boolean":
		// Case-insensitively, because the strict form buys nothing: a server
		// sending `True` has communicated the value perfectly well, and failing
		// it would be pedantry a reader cannot act on.
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true":
			return true, true, true
		case "false":
			return false, true, true
		}
		return nil, false, true

	case "array":
		return simpleList(declared, raw)

	default:
		// An object, a composition, or a schema with no type at all. `simple`
		// style can express an object, but only by a comma convention almost
		// nothing implements, and inferring a type from the constraints beside
		// it would be inventing what the document did not say.
		return nil, false, false
	}
}

// simpleList decodes the comma-separated form `simple` style gives an array.
//
// Only a list of primitives is read. An array of objects has no unambiguous
// rendering in this style, and splitting on the comma anyway would invent items
// the server never sent.
func simpleList(declared map[string]any, raw string) (value any, wellFormed, readable bool) {
	items, ok := declared["items"].(map[string]any)
	if !ok {
		return nil, false, false
	}
	switch schemaType(items) {
	case "string", "integer", "number", "boolean":
	default:
		return nil, false, false
	}

	// An empty header is an empty list, not a list holding one empty value.
	// Splitting "" on the comma produces the latter, which fails a `minItems` the
	// server in fact satisfied and an `items` type it never violated.
	if strings.TrimSpace(raw) == "" {
		return []any{}, true, true
	}

	values := []any{}
	for _, part := range strings.Split(raw, ",") {
		// The space around a comma is HTTP's, not the value's: a list header is
		// written `a, b` at least as often as `a,b`, and the runner's own joining
		// of a repeated header produces the spaced form.
		item, itemWellFormed, _ := simpleValue(items, strings.TrimSpace(part))
		if !itemWellFormed {
			return nil, false, true
		}
		values = append(values, item)
	}
	return values, true, true
}

// schemaType reads the single type a schema declares.
//
// OpenAPI 3.1 allows a list, where the null stands for the header being absent —
// a case this never sees, since only headers the response carried get here. Any
// other list leaves the value genuinely ambiguous on the wire, so it yields no
// type and the header goes unchecked.
func schemaType(declared map[string]any) string {
	switch value := declared["type"].(type) {
	case string:
		return value
	case []any:
		var names []string
		for _, item := range value {
			if name, ok := item.(string); ok && name != "null" {
				names = append(names, name)
			}
		}
		if len(names) == 1 {
			return names[0]
		}
	}
	return ""
}

// subject names what the description said a header would be, for a finding that
// reads as a sentence.
func subject(declared map[string]any) string {
	name := schemaType(declared)
	switch {
	case name == "array":
		items, _ := declared["items"].(map[string]any)
		return "the comma-separated list of " + schemaType(items) + " values"
	case name == "":
		return "the value"
	case strings.ContainsRune("aeiou", rune(name[0])):
		return "an " + name
	default:
		return "a " + name
	}
}

func lookupHeader(headers map[string]string, name string) (string, bool) {
	// Case-insensitively, because HTTP does not distinguish header names and the
	// runner records what came off the wire in lower case.
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
}
