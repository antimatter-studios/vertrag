package generate

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/antimatter-studios/vertrag/validate"
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

// TestInvalidValuesViolateTheirSchema is the other half, and it is checked
// against the real validator rather than a hand-written predicate.
//
// The property that matters is "this value is one the schema forbids", not
// "this value is missing a property" or any other particular shape of wrongness
// — Invalid has several strategies and gains more over time. An earlier version
// asserted the shape, and broke the moment object generation learned to send a
// present-but-wrong property instead of omitting a required one: the value was
// genuinely invalid, and the test said otherwise.
//
// Getting this wrong in the other direction is what actually costs something. A
// value meant to be invalid that is in fact valid makes the negative test
// assert nothing worse than nothing: the server accepts it, correctly, and the
// run reports a validation bypass that is really a generator bug.
func TestInvalidValuesViolateTheirSchema(t *testing.T) {
	for _, schema := range []string{
		`{"type":"string","minLength":5}`,
		`{"type":"string","maxLength":3}`,
		`{"type":"integer","minimum":10}`,
		`{"type":"integer","maximum":10}`,
		`{"enum":["a","b"]}`,
		// const is 2019-09 onwards. Without the dialect declared this schema
		// is read as draft-4, where the keyword does not exist and every value
		// satisfies it — which is a real hazard, not a test artefact, and is
		// guarded in the fuzz package rather than here.
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","const":"widget"}`,
		`{"type":"array","items":{"type":"string"},"minItems":2}`,
		// An unbounded list of constrained items: the only violation with a
		// list's wire form is a bad item, and it must be drawn.
		`{"type":"array","items":{"type":"integer","minimum":1}}`,
		`{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`,
		`{"type":"object","required":["a","b"],"properties":{"a":{"type":"string","minLength":3},"b":{"type":"integer","minimum":18}}}`,
	} {
		t.Run(schema, func(t *testing.T) {
			var decoded Schema
			if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
				t.Fatalf("schema: %v", err)
			}

			rapid.Check(t, func(rt *rapid.T) {
				value := Value(decoded, Invalid).Draw(rt, "value")

				encoded, err := json.Marshal(value)
				if err != nil {
					rt.Fatalf("generated value does not encode: %v", err)
				}
				if result := validate.AgainstSchema(json.RawMessage(schema), string(encoded)); result.Valid {
					rt.Fatalf("%s satisfies the schema, so it tests nothing", encoded)
				}
			})
		})
	}
}

// TestValidValuesAreAcceptedByTheValidator is the same check the other way
// round, and closes the loop the fuzz package depends on: a body sent as valid
// really is valid, so a 4xx is the server's disagreement and not vertrag's.
func TestValidValuesAreAcceptedByTheValidator(t *testing.T) {
	for _, schema := range []string{
		`{"type":"string","minLength":3,"maxLength":5}`,
		`{"type":"integer","minimum":10,"maximum":20}`,
		`{"enum":["a","b","c"]}`,
		`{"type":"array","items":{"type":"string"},"minItems":2,"maxItems":3}`,
		`{"type":"object","required":["a","b"],"properties":{"a":{"type":"string","minLength":3},"b":{"type":"integer","minimum":18,"maximum":120}}}`,
	} {
		t.Run(schema, func(t *testing.T) {
			var decoded Schema
			if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
				t.Fatalf("schema: %v", err)
			}

			rapid.Check(t, func(rt *rapid.T) {
				value := Value(decoded, Valid).Draw(rt, "value")

				encoded, err := json.Marshal(value)
				if err != nil {
					rt.Fatalf("generated value does not encode: %v", err)
				}
				result := validate.AgainstSchema(json.RawMessage(schema), string(encoded))
				if !result.Valid {
					rt.Fatalf("%s violates its own schema: %v", encoded, result.Errors)
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

// TestInvalidArraysAreSometimesStillArrays pins the strategy a wire-form
// prober needs: for a list whose items carry constraints, invalid mode must
// draw an actual LIST with a bad item — not only a scalar of the wrong type,
// which no path or query parameter can carry as a list. Without this every
// invalid draw for an unbounded list was unusable and the parameter went
// unprobed, silently.
func TestInvalidArraysAreSometimesStillArrays(t *testing.T) {
	schema := Schema{"type": "array", "items": map[string]any{"type": "integer", "minimum": 1}}

	sawList := false
	rapid.Check(t, func(rt *rapid.T) {
		if list, ok := Value(schema, Invalid).Draw(rt, "value").([]any); ok {
			sawList = true
			if len(list) == 0 {
				rt.Fatalf("an empty list violates nothing here")
			}
		}
	})
	if !sawList {
		t.Fatal("invalid mode never drew a list with a bad item; only wrong-type scalars, which a parameter cannot carry")
	}
}

// TestValidIntegersReachTheirBoundaries pins the spread: over a run of draws
// in a bounded range, the maximum itself must come up more than rarely. It is
// the value a `<` written for `<=` gets wrong, and a generator that visits it
// once in twenty-five cases will miss that handler in most twenty-case runs.
//
// The sample is deliberately much larger than the assertion needs, and that is
// the whole point of the loop. Measuring a proportion is only as good as the
// number of draws behind it: one draw per check gave 100 samples of a ~21.5%
// event, which swings between 16 and 29 run to run, and CI duly failed once at
// 9 — a healthy generator called broken, on a test whose job is to notice an
// unhealthy one. Fifty draws per check puts the estimate within a point of the
// truth every time, so the threshold below separates "reaches the boundary" from
// "visits it once in twenty-five" on evidence rather than on luck.
func TestValidIntegersReachTheirBoundaries(t *testing.T) {
	schema := Schema{"type": "integer", "minimum": 1, "maximum": 1000}
	const perCheck = 50
	atMax, draws := 0, 0
	rapid.Check(t, func(rt *rapid.T) {
		for i := range perCheck {
			draws++
			if n, ok := Value(schema, Valid).Draw(rt, fmt.Sprintf("v%d", i)).(int64); ok && n == 1000 {
				atMax++
			}
		}
	})
	// The measured rate is a little over a fifth. A tenth is comfortably below
	// that and comfortably above the twenty-fifth this exists to catch.
	if want := draws / 10; draws >= 50*perCheck && atMax < want {
		t.Errorf("the maximum was drawn %d times in %d, fewer than the %d expected; the range's far end is not being reached",
			atMax, draws, want)
	}
}
