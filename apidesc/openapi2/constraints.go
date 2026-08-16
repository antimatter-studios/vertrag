package openapi2

import (
	"strconv"

	"github.com/antimatter-studios/vertrag/generate"
)

// constraintsOf reads what a schema says about a single value.
//
// The same reading as the OpenAPI 3 parser makes, handed to the same code.
// Swagger schemas are draft-4, so `exclusiveMinimum` is only ever the boolean
// spelling — the numeric fields stay nil and cost nothing.
func constraintsOf(schema node) generate.Constraints {
	return generate.Constraints{
		MinLength: numberOf(schema, "minLength"),
		MaxLength: numberOf(schema, "maxLength"),
		Pattern:   schema.Get("pattern").Str(),
		Format:    schema.Get("format").Str(),

		Minimum:              numberOf(schema, "minimum"),
		Maximum:              numberOf(schema, "maximum"),
		ExclusiveMinimumFlag: schema.Get("exclusiveMinimum").Bool(),
		ExclusiveMaximumFlag: schema.Get("exclusiveMaximum").Bool(),

		MultipleOf: numberOf(schema, "multipleOf"),
	}
}

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
