package generate

import (
	"encoding/json"
	"testing"

	"github.com/antimatter-studios/vertrag/validate"
)

// TestBoundaryProbesHaveTheValidityTheyClaim is the guarantee that makes a
// coverage finding blame the server rather than the prober: every probe
// labelled Valid satisfies its schema and every one labelled Invalid does
// not, judged by the same validator that judges responses. A probe that
// lied about itself would report a bypass that is really the enumerator's.
func TestBoundaryProbesHaveTheValidityTheyClaim(t *testing.T) {
	for _, schema := range []string{
		`{"type":"string","minLength":2,"maxLength":5}`,
		`{"type":"string","maxLength":0}`,
		`{"type":"string","pattern":"^[a-z]+$"}`,
		`{"type":"string","format":"email"}`,
		`{"type":"integer","minimum":1,"maximum":10}`,
		`{"type":"integer","minimum":-5}`,
		`{"type":"integer"}`,
		`{"type":"number","minimum":0.5,"maximum":9.5}`,
		`{"type":"boolean"}`,
		`{"enum":["a","b"]}`,
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","const":"widget"}`,
		`{"type":"array","items":{"type":"integer","minimum":1},"minItems":1,"maxItems":3}`,
		`{"type":"array","items":{"type":"string"}}`,
		`{"type":"object","required":["name","age"],"properties":{"name":{"type":"string","minLength":3},"age":{"type":"integer","minimum":18,"maximum":120}}}`,
		`{"type":"object","required":["a"],"properties":{"a":{"type":"string"}},"additionalProperties":false}`,
		`{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string","minLength":1},"maxItems":2}}}`,
	} {
		t.Run(schema, func(t *testing.T) {
			var decoded Schema
			if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
				t.Fatalf("schema: %v", err)
			}
			probes := Boundaries(decoded)
			if len(probes) == 0 {
				t.Fatal("a constrained schema yielded no probes")
			}
			for _, probe := range probes {
				encoded, err := json.Marshal(probe.Value)
				if err != nil {
					t.Fatalf("probe %q does not encode: %v", probe.Why, err)
				}
				result := validate.AgainstSchema(json.RawMessage(schema), string(encoded))
				if probe.Mode == Valid && !result.Valid {
					t.Errorf("probe %q claims valid but %s violates the schema: %v", probe.Why, encoded, result.Errors)
				}
				if probe.Mode == Invalid && result.Valid {
					t.Errorf("probe %q claims invalid but %s satisfies the schema", probe.Why, encoded)
				}
			}
		})
	}
}

// TestBoundariesAreDeterministic pins the property the phase exists for:
// the same schema yields the same probes, in the same order, every time.
func TestBoundariesAreDeterministic(t *testing.T) {
	schema := Schema{"type": "object", "required": []any{"a", "b"},
		"properties": map[string]any{
			"a": map[string]any{"type": "integer", "minimum": 1, "maximum": 3},
			"b": map[string]any{"type": "string", "maxLength": 2},
		}}
	first, _ := json.Marshal(Boundaries(schema))
	for i := 0; i < 20; i++ {
		again, _ := json.Marshal(Boundaries(schema))
		if string(again) != string(first) {
			t.Fatalf("run %d differs:\n%s\n%s", i, first, again)
		}
	}
}

// TestBoundariesNameWhatTheyProbe: every probe explains itself, so a finding
// reads as a boundary and not as a random value.
func TestBoundariesNameWhatTheyProbe(t *testing.T) {
	schema := Schema{"type": "integer", "minimum": 1, "maximum": 10}
	whys := map[string]bool{}
	for _, probe := range Boundaries(schema) {
		if probe.Why == "" {
			t.Errorf("probe %v has no explanation", probe.Value)
		}
		whys[probe.Why] = true
	}
	for _, want := range []string{"minimum (1)", "one below minimum (0)", "maximum (10)", "one past maximum (11)"} {
		if !whys[want] {
			t.Errorf("no probe named %q; have %v", want, whys)
		}
	}
}

// TestUnconstrainedSchemasYieldNothingWorthAsking: no constraint, no
// boundary. Object and array shells still probe their type.
func TestUnconstrainedSchemasYieldNothing(t *testing.T) {
	if probes := Boundaries(Schema{}); len(probes) != 0 {
		t.Errorf("an empty schema yielded %d probes: %v", len(probes), probes)
	}
}
