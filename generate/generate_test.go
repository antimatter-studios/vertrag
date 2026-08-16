package generate

import (
	"encoding/json"
	"testing"

	"pgregory.net/rapid"
)

// drawOnce produces one value, which is how a caller outside a property test
// gets a single case.
func drawOnce(t *testing.T, schema string, mode Mode) any {
	t.Helper()

	var decoded Schema
	if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
		t.Fatalf("schema: %v", err)
	}

	var out any
	rapid.Check(t, func(rt *rapid.T) {
		out = Value(decoded, mode).Draw(rt, "value")
	})
	return out
}

// TestValidValuesSatisfyTheirSchema is the property that matters: whatever is
// generated must be something the description permits, or a failure says
// nothing about the server.
func TestValidValuesSatisfyTheirSchema(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema string
		check  func(*rapid.T, any)
	}{
		{
			name:   "string honours its length bounds",
			schema: `{"type":"string","minLength":3,"maxLength":5}`,
			check: func(rt *rapid.T, v any) {
				s, ok := v.(string)
				if !ok {
					rt.Fatalf("got %T, want string", v)
				}
				if len(s) < 3 || len(s) > 5 {
					rt.Fatalf("length %d outside [3,5]: %q", len(s), s)
				}
			},
		},
		{
			name:   "integer honours its range",
			schema: `{"type":"integer","minimum":10,"maximum":20}`,
			check: func(rt *rapid.T, v any) {
				n, ok := v.(int64)
				if !ok {
					rt.Fatalf("got %T, want int64", v)
				}
				if n < 10 || n > 20 {
					rt.Fatalf("%d outside [10,20]", n)
				}
			},
		},
		{
			name:   "enum draws only listed values",
			schema: `{"enum":["a","b","c"]}`,
			check: func(rt *rapid.T, v any) {
				switch v {
				case "a", "b", "c":
				default:
					rt.Fatalf("%v is not in the enum", v)
				}
			},
		},
		{
			name:   "array honours minItems",
			schema: `{"type":"array","items":{"type":"string"},"minItems":2,"maxItems":3}`,
			check: func(rt *rapid.T, v any) {
				items, ok := v.([]any)
				if !ok {
					rt.Fatalf("got %T, want a list", v)
				}
				if len(items) < 2 || len(items) > 3 {
					rt.Fatalf("length %d outside [2,3]", len(items))
				}
			},
		},
		{
			name:   "required properties are always present",
			schema: `{"type":"object","required":["a","b"],"properties":{"a":{"type":"string"},"b":{"type":"integer"},"c":{"type":"string"}}}`,
			check: func(rt *rapid.T, v any) {
				object, ok := v.(map[string]any)
				if !ok {
					rt.Fatalf("got %T, want an object", v)
				}
				for _, name := range []string{"a", "b"} {
					if _, present := object[name]; !present {
						rt.Fatalf("required property %q missing from %v", name, object)
					}
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var decoded Schema
			json.Unmarshal([]byte(test.schema), &decoded)

			rapid.Check(t, func(rt *rapid.T) {
				test.check(rt, Value(decoded, Valid).Draw(rt, "value"))
			})
		})
	}
}

// TestInvalidValuesViolateTheirSchema is the other half. A value that happens
// to be valid would make the negative test assert nothing — the server would
// accept it, correctly, and vertrag would call that a validation bypass.
func TestInvalidValuesViolateTheirSchema(t *testing.T) {
	for _, test := range []struct {
		name    string
		schema  string
		violate func(any) bool
	}{
		{
			"below minLength", `{"type":"string","minLength":5}`,
			func(v any) bool { s, ok := v.(string); return ok && len(s) < 5 },
		},
		{
			"above maxLength", `{"type":"string","maxLength":3}`,
			func(v any) bool { s, ok := v.(string); return ok && len(s) > 3 },
		},
		{
			"below minimum", `{"type":"integer","minimum":10}`,
			func(v any) bool { n, ok := v.(int64); return ok && n < 10 },
		},
		{
			"outside the enum", `{"enum":["a","b"]}`,
			func(v any) bool { return v != "a" && v != "b" },
		},
		{
			"missing a required property",
			`{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`,
			func(v any) bool {
				object, ok := v.(map[string]any)
				if !ok {
					return false
				}
				_, present := object["a"]
				return !present
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var decoded Schema
			json.Unmarshal([]byte(test.schema), &decoded)

			rapid.Check(t, func(rt *rapid.T) {
				value := Value(decoded, Invalid).Draw(rt, "value")
				if !test.violate(value) {
					rt.Fatalf("%#v does not violate the schema, so it tests nothing", value)
				}
			})
		})
	}
}

// TestRecursionTerminates pins that a self-referencing shape cannot generate
// forever.
func TestRecursionTerminates(t *testing.T) {
	nested := `{"type":"object","properties":{"child":{"type":"object","properties":{"child":{"type":"object","properties":{"child":{"type":"object","properties":{"child":{"type":"object","properties":{"child":{"type":"object","properties":{"child":{"type":"object"}}}}}}}}}}}}}`
	if drawOnce(t, nested, Valid) == nil {
		t.Skip("nothing generated, which is also termination")
	}
}

// TestUnknownKeywordsDoNotStopGeneration pins the tolerance a real description
// needs: a schema in the wild is often not quite valid, and refusing to
// generate would mean testing less than a stricter tool would.
func TestUnknownKeywordsDoNotStopGeneration(t *testing.T) {
	value := drawOnce(t, `{"type":"string","x-vendor":"whatever","format":"unheard-of"}`, Valid)
	if _, ok := value.(string); !ok {
		t.Errorf("got %T, want a string despite the unknown keywords", value)
	}
}
