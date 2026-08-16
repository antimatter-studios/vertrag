// Package generate produces values from a JSON Schema, for testing an API with
// inputs its description permits rather than only the one example it shows.
//
// A description illustrates one request per operation. That checks the happy
// path and nothing else: whether the server copes with a string at the length
// limit, an empty array, a number at the boundary, or an enum's last member is
// simply not asked. This package asks.
//
// Generation is pgregory.net/rapid, which supplies the part that makes
// generated tests usable: when a case fails, it shrinks the input to the
// smallest one that still fails. Without that a failure is a wall of random
// bytes and nobody can act on it.
package generate

import (
	"encoding/json"
	"math"
	"sort"
	"strings"

	"pgregory.net/rapid"
)

// Schema is a JSON Schema, decoded far enough to generate from.
//
// It is deliberately loose: a description in the wild is often not quite valid,
// and refusing to generate for a schema with an odd keyword would mean testing
// less than a stricter tool would. Anything not understood simply constrains
// nothing.
type Schema map[string]any

// Mode selects what kind of value to produce.
type Mode int

const (
	// Valid produces values the schema permits. A server should accept them,
	// and answer with something the description documents.
	Valid Mode = iota
	// Invalid produces values the schema forbids. A server should reject them
	// with a 4xx — quietly accepting one is a validation bypass, and answering
	// 5xx means it crashed on input it should have refused.
	Invalid
)

// Value builds a generator for a schema.
func Value(schema Schema, mode Mode) *rapid.Generator[any] {
	return rapid.Custom(func(t *rapid.T) any {
		return draw(t, schema, mode, 0)
	})
}

// maxDepth stops a recursive schema from generating forever. A structure this
// deep is not a case anyone will debug anyway.
const maxDepth = 6

func draw(t *rapid.T, schema Schema, mode Mode, depth int) any {
	if depth > maxDepth {
		return nil
	}

	// An enum lists every value there is, so nothing else is worth trying —
	// and for the invalid case, anything NOT in the list is a violation.
	if values, ok := list(schema["enum"]); ok && len(values) > 0 {
		if mode == Valid {
			return rapid.SampledFrom(values).Draw(t, "enum")
		}
		return notAmong(t, values)
	}
	if fixed, ok := schema["const"]; ok {
		if mode == Valid {
			// Still draw, so the case is recorded and can be shrunk: a
			// generator that consumes nothing produces the same case forever.
			rapid.Bool().Draw(t, "const")
			return fixed
		}
		return notAmong(t, []any{fixed})
	}

	switch declaredType(schema, t) {
	case "string":
		return drawString(t, schema, mode)
	case "integer":
		return drawInteger(t, schema, mode)
	case "number":
		return drawNumber(t, schema, mode)
	case "boolean":
		if mode == Invalid {
			// A string where a boolean belongs is the mistake a client
			// actually makes, rather than an exotic value.
			return "vertrag-not-a-boolean"
		}
		return rapid.Bool().Draw(t, "boolean")
	case "array":
		return drawArray(t, schema, mode, depth)
	case "object":
		return drawObject(t, schema, mode, depth)
	case "null":
		return nil
	default:
		// No usable type. A string exercises most handlers without being
		// nonsense.
		return rapid.StringN(0, 16, -1).Draw(t, "untyped")
	}
}

// declaredType reads the schema's type, choosing one when several are allowed.
func declaredType(schema Schema, t *rapid.T) string {
	switch declared := schema["type"].(type) {
	case string:
		return declared
	case []any:
		var names []string
		for _, item := range declared {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			return ""
		}
		return rapid.SampledFrom(names).Draw(t, "type")
	default:
		return ""
	}
}

func drawString(t *rapid.T, schema Schema, mode Mode) any {
	min, hasMin := integerAt(schema, "minLength")
	max, hasMax := integerAt(schema, "maxLength")

	if mode == Invalid {
		// Break the tightest stated bound, drawing HOW far past it so the
		// boundary itself and values well beyond it both get tried. A length
		// violation is the one a server is most likely to mishandle, because it
		// usually checks the type and forgets the size.
		switch {
		case hasMin && min > 0:
			return strings.Repeat("x", rapid.IntRange(0, min-1).Draw(t, "too-short"))
		case hasMax:
			return strings.Repeat("x", rapid.IntRange(max+1, max+8).Draw(t, "too-long"))
		default:
			// Without a length bound, the wrong type is the only violation
			// available.
			return rapid.SampledFrom([]any{12345, true, []any{}}).Draw(t, "wrong-type")
		}
	}

	low, high := 0, 24
	if hasMin {
		low = min
	}
	if hasMax {
		high = max
	}
	if high < low {
		high = low
	}
	// Printable ASCII: a generated value should be something a person can read
	// in a failure report and paste into a terminal.
	return rapid.StringOfN(rapid.RuneFrom([]rune(printable)), low, high, -1).Draw(t, "string")
}

const printable = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 -_."

func drawInteger(t *rapid.T, schema Schema, mode Mode) any {
	min, hasMin := numberAt(schema, "minimum")
	max, hasMax := numberAt(schema, "maximum")

	if mode == Invalid {
		// Something that is not a number at all violates the schema whether or
		// not it states a bound, and it is the violation that finds the handler
		// which never parsed the value — the one that answers `abc` with a 500
		// rather than a 400. Which violation to draw is therefore a choice
		// rather than a fallback: breaking only the bound would leave the type
		// question unasked of every bounded field, and most fields are bounded.
		wrongType := !hasMin && !hasMax
		if !wrongType {
			wrongType = rapid.Bool().Draw(t, "wrong-type")
		}

		switch {
		case wrongType:
			return rapid.SampledFrom([]any{"vertrag-not-a-number", true}).Draw(t, "not-a-number")
		case hasMin:
			return rapid.Int64Range(int64(min)-100, int64(min)-1).Draw(t, "too-small")
		default:
			return rapid.Int64Range(int64(max)+1, int64(max)+100).Draw(t, "too-large")
		}
	}

	low, high := int64(-1000), int64(1000)
	if hasMin {
		low = int64(math.Ceil(min))
	}
	if hasMax {
		high = int64(math.Floor(max))
	}
	if high < low {
		high = low
	}
	return rapid.Int64Range(low, high).Draw(t, "integer")
}

func drawNumber(t *rapid.T, schema Schema, mode Mode) any {
	if value := drawInteger(t, schema, mode); mode == Invalid {
		return value
	}
	min, hasMin := numberAt(schema, "minimum")
	max, hasMax := numberAt(schema, "maximum")

	low, high := -1000.0, 1000.0
	if hasMin {
		low = min
	}
	if hasMax {
		high = max
	}
	if high < low {
		high = low
	}
	return rapid.Float64Range(low, high).Draw(t, "number")
}

func drawArray(t *rapid.T, schema Schema, mode Mode, depth int) any {
	items, _ := schema["items"].(map[string]any)

	min, hasMin := integerAt(schema, "minItems")
	max, hasMax := integerAt(schema, "maxItems")

	if mode == Invalid {
		switch {
		case hasMin && min > 0:
			// Short of the minimum, including the boundary a handler checking
			// `len(x) > 0` gets wrong.
			return fill(t, Schema(items), rapid.IntRange(0, min-1).Draw(t, "too-few"), depth)
		case hasMax:
			return fill(t, Schema(items), rapid.IntRange(max+1, max+4).Draw(t, "too-many"), depth)
		default:
			return rapid.SampledFrom([]any{"vertrag-not-an-array", 42}).Draw(t, "wrong-type")
		}
	}

	low, high := 0, 3
	if hasMin {
		low = min
	}
	if hasMax && max < high {
		high = max
	}
	if high < low {
		high = low
	}

	length := rapid.IntRange(low, high).Draw(t, "length")
	out := make([]any, 0, length)
	for i := 0; i < length; i++ {
		out = append(out, draw(t, Schema(items), Valid, depth+1))
	}
	return out
}

func drawObject(t *rapid.T, schema Schema, mode Mode, depth int) any {
	properties, _ := schema["properties"].(map[string]any)
	required := map[string]bool{}
	if names, ok := list(schema["required"]); ok {
		for _, name := range names {
			if text, ok := name.(string); ok {
				required[text] = true
			}
		}
	}

	// An object can be invalid two ways, and a server may enforce one and not
	// the other: a required property can be missing, or a property that IS
	// present can break its own constraints. Checking only the first would pass
	// any handler that verifies presence and never looks at the value, which is
	// the more common of the two mistakes.
	if mode == Invalid {
		names := sortedNames(properties)

		if len(required) > 0 && (len(names) == 0 || rapid.Bool().Draw(t, "omit-required")) {
			// Which property is dropped is drawn rather than fixed, because a
			// handler often checks the first and forgets the rest.
			omit := rapid.SampledFrom(sortedKeys(required)).Draw(t, "omit")

			out := map[string]any{}
			for _, name := range names {
				if name == omit {
					continue
				}
				nested, _ := properties[name].(map[string]any)
				out[name] = draw(t, Schema(nested), Valid, depth+1)
			}
			return out
		}

		if len(names) > 0 {
			// Everything present and one property wrong, so a handler that
			// checks presence but not values has something to fail on.
			broken := rapid.SampledFrom(names).Draw(t, "break")

			out := map[string]any{}
			for _, name := range names {
				nested, _ := properties[name].(map[string]any)
				valueMode := Valid
				if name == broken {
					valueMode = Invalid
				}
				out[name] = draw(t, Schema(nested), valueMode, depth+1)
			}
			return out
		}
	}

	out := map[string]any{}
	// Sorted, so the same seed produces the same case: Go map order is
	// randomised, and a generated failure nobody can reproduce is not a
	// finding.
	for _, name := range sortedNames(properties) {
		nested, _ := properties[name].(map[string]any)
		// An optional property is sometimes present and sometimes not, which is
		// how a handler that assumes presence gets found.
		if !required[name] && !rapid.Bool().Draw(t, "include-"+name) {
			continue
		}
		out[name] = draw(t, Schema(nested), Valid, depth+1)
	}
	return out
}

// notAmong draws a value that is deliberately none of the listed ones.
func notAmong(t *rapid.T, values []any) any {
	candidates := []any{"vertrag-not-permitted", -999999, true, ""}
	for _, candidate := range candidates {
		found := false
		for _, value := range values {
			if value == candidate {
				found = true
				break
			}
		}
		if !found {
			// Draw anyway, so the case is recorded and shrinkable.
			rapid.Bool().Draw(t, "excluded")
			return candidate
		}
	}
	return rapid.StringN(20, 30, -1).Draw(t, "excluded")
}

func sortedNames(properties map[string]any) []string {
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedKeys(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func list(value any) ([]any, bool) {
	out, ok := value.([]any)
	return out, ok
}

func integerAt(schema Schema, key string) (int, bool) {
	value, ok := numberAt(schema, key)
	return int(value), ok
}

func numberAt(schema Schema, key string) (float64, bool) {
	switch value := schema[key].(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

// fill builds a list of the given length whose members are all valid.
//
// The members have to be drawn rather than left as the zero value, and the
// reason is not tidiness. `make([]any, n)` produces a list of nils, so an array
// meant to be invalid only in its LENGTH was also invalid in every element —
// which for a body means the finding could be about either, and for a parameter
// means the list has no wire form at all and is never sent. The count is the
// constraint under test, so it should be the only one broken.
func fill(t *rapid.T, items Schema, length, depth int) []any {
	out := make([]any, 0, length)
	for i := 0; i < length; i++ {
		out = append(out, draw(t, items, Valid, depth+1))
	}
	return out
}
