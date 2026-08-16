package generate

import (
	"encoding/json"
	"testing"

	"github.com/antimatter-studios/vertrag/validate"
	"pgregory.net/rapid"
)

// TestASpecimenAlwaysSatisfiesItsOwnConstraints is the whole obligation of this
// package, checked against the validator rather than against arithmetic.
//
// Every constraint is honoured by a separate step — a minimum raises, a
// multipleOf rounds up, a maximum lowers — and each is correct alone. What no
// table of cases establishes is that they are correct TOGETHER: rounding up to
// a multiple can push a value past a maximum, an exclusive bound can collide
// with a step, and a minLength can contradict a pattern. Combinations are what
// this draws.
//
// A specimen that violates the schema it came from is not a cosmetic problem.
// It is sent as a request body, so a server correctly enforcing its own
// contract answers 400 and the run reports the server as broken.
func TestASpecimenAlwaysSatisfiesItsOwnConstraints(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		constraints, schema := drawNumericConstraints(rt)

		value := constraints.Number(0)

		encoded, err := json.Marshal(value)
		if err != nil {
			rt.Fatalf("%v does not encode: %v", value, err)
		}
		raw, err := json.Marshal(schema)
		if err != nil {
			rt.Fatalf("schema does not encode: %v", err)
		}

		result := validate.AgainstSchema(raw, string(encoded))
		if !result.Valid {
			rt.Fatalf("schema %s produced %s, which it forbids: %v",
				raw, encoded, result.Errors)
		}
	})
}

// drawNumericConstraints builds a set of bounds that CAN be satisfied.
//
// An impossible schema — a minimum above its maximum — has no specimen, and
// asserting one would be asserting nonsense. The bounds are drawn in order so
// the range is always non-empty, which keeps the test about the arithmetic
// rather than about contradictions the arithmetic cannot resolve.
func drawNumericConstraints(t *rapid.T) (Constraints, map[string]any) {
	schema := map[string]any{"type": "number"}
	var constraints Constraints

	low := float64(rapid.IntRange(-100, 100).Draw(t, "low"))
	span := float64(rapid.IntRange(0, 200).Draw(t, "span"))
	high := low + span

	if rapid.Bool().Draw(t, "has-minimum") {
		constraints.Minimum = &low
		schema["minimum"] = low
	}
	if rapid.Bool().Draw(t, "has-maximum") {
		constraints.Maximum = &high
		schema["maximum"] = high
	}

	// A step that divides the range, so at least one multiple sits inside it.
	// A step larger than the span describes a schema nothing satisfies, which
	// is the document's problem and not this function's.
	if span > 0 && rapid.Bool().Draw(t, "has-multiple-of") {
		step := rapid.SampledFrom([]float64{1, 2, 5, 0.1, 0.25, 0.5}).Draw(t, "step")
		if step <= span {
			constraints.MultipleOf = &step
			schema["multipleOf"] = step
		}
	}

	return constraints, schema
}

// TestAStringSpecimenAlwaysSatisfiesItsOwnConstraints is the same obligation for
// the other half, where a pattern and a length bound can contradict each other.
func TestAStringSpecimenAlwaysSatisfiesItsOwnConstraints(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		schema := map[string]any{"type": "string"}
		var constraints Constraints

		minLength := float64(rapid.IntRange(0, 12).Draw(rt, "min-length"))
		maxLength := minLength + float64(rapid.IntRange(0, 12).Draw(rt, "extra"))

		if rapid.Bool().Draw(rt, "has-min-length") {
			constraints.MinLength = &minLength
			schema["minLength"] = minLength
		}
		if rapid.Bool().Draw(rt, "has-max-length") {
			constraints.MaxLength = &maxLength
			schema["maxLength"] = maxLength
		}
		if rapid.Bool().Draw(rt, "has-format") {
			format := rapid.SampledFrom([]string{
				"date-time", "date", "email", "uuid", "ipv4", "hostname",
			}).Draw(rt, "format")
			constraints.Format = format
			schema["format"] = format
		}

		value := constraints.String()

		encoded, _ := json.Marshal(value)
		raw, _ := json.Marshal(schema)

		result := validate.AgainstSchema(raw, string(encoded))
		if !result.Valid {
			// A format and a length bound can genuinely contradict: no email
			// address is three characters long. The specimen follows the format,
			// since that is the constraint a server actually parses, and the
			// document is what is wrong. Only report where no such conflict
			// exists.
			if constraints.Format != "" && (constraints.MinLength != nil || constraints.MaxLength != nil) {
				return
			}
			rt.Fatalf("schema %s produced %s, which it forbids: %v",
				raw, encoded, result.Errors)
		}
	})
}
