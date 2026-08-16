package validate

import (
	"encoding/json"
	"strconv"
	"testing"

	"pgregory.net/rapid"
)

// TestAValueTheSchemaPermitsIsNeverRejected is the soundness obligation of
// header validation, and the one that decides whether the check can be trusted
// at all.
//
// A header is text and a schema describes JSON, so the value has to be decoded
// before it can be checked — and a decoding that is even slightly wrong fails
// servers that are behaving. That failure mode is worse than the check being
// absent: a correct server reported as broken teaches people to switch the
// check off, and then it catches nothing ever again.
//
// So: draw a value, write it the way a server would put it on the wire, and
// require the check to accept it.
func TestAValueTheSchemaPermitsIsNeverRejected(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		schema, wire := drawHeaderCase(rt)

		raw, err := json.Marshal(schema)
		if err != nil {
			rt.Fatalf("schema does not encode: %v", err)
		}

		problems := AgainstHeaderSchemas(
			map[string]json.RawMessage{"x-probe": raw},
			map[string]string{"X-Probe": wire},
		)
		if len(problems) > 0 {
			rt.Fatalf("schema %s rejected %q, which a correct server would send: %v",
				raw, wire, problems)
		}
	})
}

// drawHeaderCase produces a schema and a value that satisfies it, written as a
// server would put it on the wire.
func drawHeaderCase(t *rapid.T) (map[string]any, string) {
	switch rapid.IntRange(0, 4).Draw(t, "kind") {
	case 0:
		value := rapid.IntRange(-100000, 100000).Draw(t, "integer")
		return map[string]any{"type": "integer"}, strconv.Itoa(value)

	case 1:
		// A number written the several ways a server might: plain, with a
		// decimal part, negative, and with the leading zero a formatter may
		// omit.
		value := float64(rapid.IntRange(-1000, 1000).Draw(t, "number"))
		text := rapid.SampledFrom([]string{
			strconv.FormatFloat(value, 'f', -1, 64),
			strconv.FormatFloat(value, 'f', 2, 64),
		}).Draw(t, "spelling")
		return map[string]any{"type": "number"}, text

	case 2:
		value := rapid.Bool().Draw(t, "boolean")
		// Case-insensitively, because a server sending `True` has communicated
		// the value and failing it is pedantry nobody can act on.
		text := strconv.FormatBool(value)
		if rapid.Bool().Draw(t, "capitalised") {
			text = map[bool]string{true: "True", false: "False"}[value]
		}
		return map[string]any{"type": "boolean"}, text

	case 3:
		value := rapid.StringOfN(rapid.RuneFrom([]rune("abcXYZ019 -_.")), 0, 20, -1).
			Draw(t, "string")
		return map[string]any{"type": "string"}, value

	default:
		// A list, in HTTP's own comma-separated form, with the optional space
		// after the separator that the grammar permits and servers emit.
		length := rapid.IntRange(0, 4).Draw(t, "length")
		items := make([]string, 0, length)
		for i := 0; i < length; i++ {
			items = append(items, rapid.SampledFrom([]string{"a", "bb", "ccc"}).Draw(t, "item"))
		}

		separator := rapid.SampledFrom([]string{",", ", "}).Draw(t, "separator")
		wire := ""
		for i, item := range items {
			if i > 0 {
				wire += separator
			}
			wire += item
		}
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, wire
	}
}

// TestAnUndecodableValueIsNotAFailure pins the other half of the rule: where
// the text cannot be read as the declared type, nothing is claimed.
//
// The temptation is to report it — a header declared an integer, and the server
// sent "banana". But that IS the finding the check exists for, and it is
// reported through the ordinary path. What must not happen is a schema vertrag
// cannot apply producing a failure about the server, which is why the decode
// has three states rather than two.
func TestAnUndecodableValueIsNotAFailure(t *testing.T) {
	for _, schema := range []map[string]any{
		// Shapes with no unambiguous wire form. The comma convention for an
		// object is almost never what a server implements, so checking one
		// would fail servers doing something reasonable.
		{"type": "object"},
		{"type": "object", "properties": map[string]any{"a": map[string]any{}}},
		{"allOf": []any{map[string]any{"type": "string"}}},
		{"description": "no type at all"},
	} {
		raw, err := json.Marshal(schema)
		if err != nil {
			t.Fatalf("schema does not encode: %v", err)
		}

		for _, value := range []string{"", "anything", "a,b", "42", "{}"} {
			problems := AgainstHeaderSchemas(
				map[string]json.RawMessage{"x-probe": raw},
				map[string]string{"X-Probe": value},
			)
			if len(problems) > 0 {
				t.Errorf("schema %s reported %q: %v — a schema that cannot be applied "+
					"must not produce a finding about the server", raw, value, problems)
			}
		}
	}
}
