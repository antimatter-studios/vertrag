package generate

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Boundary probing is the deterministic complement to random generation.
//
// Random generation asks "does anything break?" and answers differently each
// run. A boundary probe asks the specific questions a schema's constraints
// raise — the maximum, one past it, the empty string, the required property
// missing — and asks the SAME questions every run, so a run in CI is a
// regression gate rather than a lottery. Every probe says what it is testing
// and whether the schema permits it, so a finding reads as "the server
// accepted maximum+1 for size", not as a random number that happened to work.

// Probe is one deterministic value derived from a schema's constraints.
type Probe struct {
	Value any
	Mode  Mode
	// Why names the boundary in words: "maximum (10)", "one past maximum
	// (11)", "required property 'age' missing".
	Why string
}

// Boundaries enumerates the probes a schema's constraints imply, in a fixed
// order. Valid probes sit exactly on a bound; Invalid ones sit one step past
// it, or omit what is required, or carry the wrong type. A schema that
// constrains nothing yields nothing — there is no boundary to probe.
func Boundaries(schema Schema) []Probe {
	return boundaries(schema, 0)
}

func boundaries(schema Schema, depth int) []Probe {
	if depth > maxDepth {
		return nil
	}

	// An enum is its own complete list of boundaries: every member is
	// permitted, and one thing that is none of them is not.
	if values, ok := list(schema["enum"]); ok && len(values) > 0 {
		var out []Probe
		for _, value := range values {
			out = append(out, Probe{value, Valid, fmt.Sprintf("enum member %v", jsonish(value))})
		}
		out = append(out, Probe{"vertrag-not-in-enum", Invalid, "a value outside the enum"})
		return out
	}
	if fixed, ok := schema["const"]; ok {
		return []Probe{
			{fixed, Valid, fmt.Sprintf("the const %v", jsonish(fixed))},
			{"vertrag-not-the-const", Invalid, "a value other than the const"},
		}
	}

	switch firstType(schema) {
	case "string":
		return stringBoundaries(schema)
	case "integer":
		return integerBoundaries(schema)
	case "number":
		return numberBoundaries(schema)
	case "boolean":
		return []Probe{
			{true, Valid, "true"},
			{false, Valid, "false"},
			{"vertrag-not-a-boolean", Invalid, "a string where a boolean belongs"},
		}
	case "array":
		return arrayBoundaries(schema, depth)
	case "object":
		return objectBoundaries(schema, depth)
	}
	return nil
}

func stringBoundaries(schema Schema) []Probe {
	min, hasMin := integerAt(schema, "minLength")
	max, hasMax := integerAt(schema, "maxLength")
	var out []Probe

	_, hasPattern := schema["pattern"]
	_, hasFormat := schema["format"]
	shaped := hasPattern || hasFormat

	// A pattern or a format decides the string's exact shape, so length
	// boundaries and the empty string cannot be offered as VALID beside
	// them: "xx" is two characters and matches no email pattern. The valid
	// probe is then the exemplar, which honours the shape; the length
	// violations still stand, since a too-long string violates however
	// well it matches.
	switch {
	case shaped:
		out = append(out, Probe{exemplar(schema, 0), Valid, "a string of the declared shape"})
	case hasMin:
		out = append(out, Probe{strings.Repeat("x", min), Valid, fmt.Sprintf("minLength (%d)", min)})
	case !hasMax || max > 0:
		// No minimum stated: the empty string is permitted, and is the
		// value a handler forgets to consider.
		out = append(out, Probe{"", Valid, "the empty string"})
	}
	if hasMin && min > 0 {
		out = append(out, Probe{strings.Repeat("x", min-1), Invalid, fmt.Sprintf("one short of minLength (%d)", min-1)})
	}
	if hasMax {
		if !shaped {
			out = append(out, Probe{strings.Repeat("x", max), Valid, fmt.Sprintf("maxLength (%d)", max)})
		}
		out = append(out, Probe{strings.Repeat("x", max+1), Invalid, fmt.Sprintf("one past maxLength (%d)", max+1)})
	}
	if hasPattern {
		out = append(out, Probe{"vertrag does not match", Invalid, "a string that does not match the pattern"})
	}
	if hasFormat {
		out = append(out, Probe{"not-a-" + fmt.Sprint(schema["format"]), Invalid, fmt.Sprintf("a string that is not a %v", schema["format"])})
	}
	out = append(out, Probe{12345, Invalid, "a number where a string belongs"})
	return out
}

func integerBoundaries(schema Schema) []Probe {
	min, hasMin := numberAt(schema, "minimum")
	max, hasMax := numberAt(schema, "maximum")
	var out []Probe

	if hasMin {
		lo := int64(math.Ceil(min))
		out = append(out, Probe{lo, Valid, fmt.Sprintf("minimum (%d)", lo)})
		out = append(out, Probe{lo - 1, Invalid, fmt.Sprintf("one below minimum (%d)", lo-1)})
	}
	if hasMax {
		hi := int64(math.Floor(max))
		out = append(out, Probe{hi, Valid, fmt.Sprintf("maximum (%d)", hi)})
		out = append(out, Probe{hi + 1, Invalid, fmt.Sprintf("one past maximum (%d)", hi+1)})
	}
	if !hasMin && !hasMax {
		out = append(out, Probe{int64(0), Valid, "zero"})
		out = append(out, Probe{int64(-1), Valid, "a negative number"})
	} else if hasMin && min <= 0 && (!hasMax || max >= 0) {
		out = append(out, Probe{int64(0), Valid, "zero"})
	}
	out = append(out, Probe{"vertrag-not-a-number", Invalid, "a string where an integer belongs"})
	out = append(out, Probe{1.5, Invalid, "a fraction where an integer belongs"})
	return out
}

func numberBoundaries(schema Schema) []Probe {
	min, hasMin := numberAt(schema, "minimum")
	max, hasMax := numberAt(schema, "maximum")
	var out []Probe
	if hasMin {
		out = append(out, Probe{min, Valid, fmt.Sprintf("minimum (%v)", min)})
		out = append(out, Probe{math.Nextafter(min, math.Inf(-1)), Invalid, "just below minimum"})
	}
	if hasMax {
		out = append(out, Probe{max, Valid, fmt.Sprintf("maximum (%v)", max)})
		out = append(out, Probe{math.Nextafter(max, math.Inf(1)), Invalid, "just past maximum"})
	}
	if !hasMin && !hasMax {
		out = append(out, Probe{0.0, Valid, "zero"})
	}
	out = append(out, Probe{"vertrag-not-a-number", Invalid, "a string where a number belongs"})
	return out
}

func arrayBoundaries(schema Schema, depth int) []Probe {
	items, _ := schema["items"].(map[string]any)
	min, hasMin := integerAt(schema, "minItems")
	max, hasMax := integerAt(schema, "maxItems")
	var out []Probe

	fillWith := exemplar(Schema(items), depth+1)
	filled := func(n int) []any {
		list := make([]any, 0, n)
		for i := 0; i < n; i++ {
			list = append(list, fillWith)
		}
		return list
	}

	if hasMin {
		out = append(out, Probe{filled(min), Valid, fmt.Sprintf("minItems (%d)", min)})
		if min > 0 {
			out = append(out, Probe{filled(min - 1), Invalid, fmt.Sprintf("one short of minItems (%d)", min-1)})
		}
	} else {
		out = append(out, Probe{[]any{}, Valid, "the empty list"})
	}
	if hasMax {
		out = append(out, Probe{filled(max), Valid, fmt.Sprintf("maxItems (%d)", max)})
		out = append(out, Probe{filled(max + 1), Invalid, fmt.Sprintf("one past maxItems (%d)", max+1)})
	}
	// Each item boundary, in a list of one, so the list itself is well
	// formed and the item is what is under test.
	for _, probe := range boundaries(Schema(items), depth+1) {
		if probe.Mode == Invalid {
			out = append(out, Probe{[]any{probe.Value}, Invalid, "an item that is " + probe.Why})
		}
	}
	out = append(out, Probe{"vertrag-not-an-array", Invalid, "a string where a list belongs"})
	return out
}

func objectBoundaries(schema Schema, depth int) []Probe {
	properties, _ := schema["properties"].(map[string]any)
	required := requiredNames(schema)
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)

	base := exemplarObject(schema, depth)
	var out []Probe

	// The exemplar itself: every required property present, each at a value
	// the schema permits. It is the object every other probe is a variant of.
	out = append(out, Probe{base, Valid, "every required property present"})

	// Each required property missing in turn — the omission handlers most
	// often fail to check.
	for _, name := range names {
		if !required[name] {
			continue
		}
		without := copyObject(base)
		delete(without, name)
		out = append(out, Probe{without, Invalid, fmt.Sprintf("required property %q missing", name)})
	}

	// Each property's own boundaries, with the rest of the object held at
	// the exemplar, so a finding names one property and one boundary.
	for _, name := range names {
		property, _ := properties[name].(map[string]any)
		for _, probe := range boundaries(Schema(property), depth+1) {
			variant := copyObject(base)
			variant[name] = probe.Value
			out = append(out, Probe{variant, probe.Mode, fmt.Sprintf("%q at %s", name, probe.Why)})
		}
	}

	if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
		extra := copyObject(base)
		extra["vertragUnexpected"] = "extra"
		out = append(out, Probe{extra, Invalid, "an undeclared property where additionalProperties is false"})
	}
	return out
}

// exemplar is one deterministic value the schema permits: the smallest thing
// that satisfies every constraint, so it can stand in for "the rest of the
// object" while one property is being probed. It mirrors draw's Valid branch
// without the randomness.
func exemplar(schema Schema, depth int) any {
	if depth > maxDepth {
		return nil
	}
	if values, ok := list(schema["enum"]); ok && len(values) > 0 {
		return values[0]
	}
	if fixed, ok := schema["const"]; ok {
		return fixed
	}
	switch firstType(schema) {
	case "string":
		min, _ := integerAt(schema, "minLength")
		if min < 1 {
			min = 1
		}
		if format, ok := schema["format"].(string); ok {
			if value, ok := FromFormat(format); ok {
				return value
			}
		}
		if pattern, ok := schema["pattern"].(string); ok {
			if value, ok := FromPattern(pattern); ok {
				return value
			}
		}
		return strings.Repeat("x", min)
	case "integer":
		if min, ok := numberAt(schema, "minimum"); ok {
			return int64(math.Ceil(min))
		}
		if max, ok := numberAt(schema, "maximum"); ok && max < 1 {
			return int64(math.Floor(max))
		}
		return int64(1)
	case "number":
		if min, ok := numberAt(schema, "minimum"); ok {
			return min
		}
		if max, ok := numberAt(schema, "maximum"); ok && max < 1 {
			return max
		}
		return 1.0
	case "boolean":
		return true
	case "array":
		items, _ := schema["items"].(map[string]any)
		min, _ := integerAt(schema, "minItems")
		list := make([]any, 0, min)
		for i := 0; i < min; i++ {
			list = append(list, exemplar(Schema(items), depth+1))
		}
		return list
	case "object":
		return exemplarObject(schema, depth)
	case "null":
		return nil
	}
	return "x"
}

func exemplarObject(schema Schema, depth int) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	required := requiredNames(schema)
	out := map[string]any{}
	for name, property := range properties {
		if !required[name] {
			continue
		}
		p, _ := property.(map[string]any)
		out[name] = exemplar(Schema(p), depth+1)
	}
	return out
}

func requiredNames(schema Schema) map[string]bool {
	out := map[string]bool{}
	if names, ok := list(schema["required"]); ok {
		for _, name := range names {
			if s, ok := name.(string); ok {
				out[s] = true
			}
		}
	}
	return out
}

func copyObject(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// firstType is the schema's declared type, or the first of several — the
// deterministic counterpart of declaredType, which draws one at random.
func firstType(schema Schema) string {
	switch declared := schema["type"].(type) {
	case string:
		return declared
	case []any:
		for _, item := range declared {
			if name, ok := item.(string); ok && name != "null" {
				return name
			}
		}
	}
	return ""
}

func jsonish(value any) string {
	if s, ok := value.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprint(value)
}
