package openapi3

import (
	"strconv"

	"github.com/antimatter-studios/vertrag/generate"
)

// constraintsOf reads what a schema says about a single value.
//
// Both description parsers do this and hand the result to the same code, so
// that "the smallest string a minLength, a pattern and a format permit" has one
// implementation rather than one per format.
func constraintsOf(schema node) generate.Constraints {
	return generate.Constraints{
		MinLength: numberOf(schema, "minLength"),
		MaxLength: numberOf(schema, "maxLength"),
		Pattern:   schema.Get("pattern").Str(),
		Format:    schema.Get("format").Str(),

		Minimum:          numberOf(schema, "minimum"),
		Maximum:          numberOf(schema, "maximum"),
		ExclusiveMinimum: numberOf(schema, "exclusiveMinimum"),
		ExclusiveMaximum: numberOf(schema, "exclusiveMaximum"),
		// draft-4, which OpenAPI 3.0 yields, spells these as booleans modifying
		// `minimum` and `maximum`; 2019-09 onwards spells them as the bounds
		// themselves. Reading both means a 3.0 and a 3.1 document describing
		// the same API produce the same specimen.
		ExclusiveMinimumFlag: schema.Get("exclusiveMinimum").Bool(),
		ExclusiveMaximumFlag: schema.Get("exclusiveMaximum").Bool(),

		MultipleOf: numberOf(schema, "multipleOf"),
	}
}

// numberOf reads a numeric keyword, reporting nil when the schema was silent.
//
// The distinction matters: `minimum: 0` says something and a missing minimum
// does not, and collapsing the two would make every unbounded number start from
// zero whether or not that was permitted.
func numberOf(schema node, key string) *float64 {
	entry := schema.Get(key)
	if !entry.IsScalar() {
		return nil
	}
	value, err := strconv.ParseFloat(entry.Value, 64)
	if err != nil {
		return nil
	}
	return &value
}
