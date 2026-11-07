package generate

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/antimatter-studios/vertrag/validate"
	"pgregory.net/rapid"
)

func TestIDNGenerationAgreesWithValidation(t *testing.T) {
	for _, format := range []string{"idn-email", "idn-hostname"} {
		for _, mode := range []Mode{Valid, Invalid} {
			t.Run(fmt.Sprintf("%s/%d", format, mode), func(t *testing.T) {
				schema := Schema{"type": "string", "format": format}
				raw, _ := json.Marshal(schema)
				rapid.Check(t, func(rt *rapid.T) {
					value := Value(schema, mode).Draw(rt, "value")
					if _, ok := value.(string); !ok {
						rt.Fatalf("IDN generation must exercise the format, not just the type: %v", value)
					}
					encoded, _ := json.Marshal(value)
					if result := validate.AgainstSchema(raw, string(encoded)); result.Valid != (mode == Valid) {
						rt.Fatalf("mode %d generated %s with the wrong validity: %v", mode, encoded, result.Errors)
					}
				})
			})
		}
	}
}

func TestIDNBoundariesIncludeASCIIAndInternationalExamples(t *testing.T) {
	for _, format := range []string{"idn-email", "idn-hostname"} {
		schema := Schema{"type": "string", "format": format}
		raw, _ := json.Marshal(schema)
		seen := map[string]Mode{}
		for _, probe := range Boundaries(schema) {
			encoded, _ := json.Marshal(probe.Value)
			if result := validate.AgainstSchema(raw, string(encoded)); result.Valid != (probe.Mode == Valid) {
				t.Errorf("%s: probe %q has the wrong validity: %v", format, probe.Why, result.Errors)
			}
			if value, ok := probe.Value.(string); ok {
				seen[value] = probe.Mode
			}
		}
		for _, mode := range []Mode{Valid, Invalid} {
			for _, example := range idnExamples(format, mode) {
				if got, exists := seen[example]; !exists || got != mode {
					t.Errorf("%s: missing example %q in mode %d", format, example, mode)
				}
			}
		}
		if specimen, ok := FromFormat(format); !ok || specimen != idnExamples(format, Valid)[0] {
			t.Errorf("%s: missing stable ASCII specimen: %q", format, specimen)
		}
		drawn := map[string]bool{}
		generator := Value(schema, Valid)
		for seed := 1; seed <= 64; seed++ {
			drawn[generator.Example(seed).(string)] = true
		}
		for _, example := range idnExamples(format, Valid) {
			if !drawn[example] {
				t.Errorf("%s: random generation never draws %q", format, example)
			}
		}
	}
	if examples := idnExamples("email", Valid); examples != nil {
		t.Errorf("ordinary formats must retain their generation: %v", examples)
	}
}
