package generate

import (
	"math"
	"strings"
)

// Constraints are what a schema says about a single value.
//
// It exists so the two description parsers agree on what a specimen looks like.
// They read different document formats and cannot share a schema type, but
// "the smallest string permitted by a minLength, a pattern and a format" has
// one right answer and having it written twice guarantees the two drift — which
// is exactly what happened before this: OpenAPI 3 honoured the bounds and
// Swagger sent -100000000 for every number, so the same API described in the
// two formats was tested with different rigour.
//
// A nil pointer means the schema was silent about that bound, which is not the
// same as a zero: `minimum: 0` says something, and a missing minimum does not.
type Constraints struct {
	MinLength *float64
	MaxLength *float64
	Pattern   string
	Format    string

	Minimum          *float64
	Maximum          *float64
	ExclusiveMinimum *float64
	ExclusiveMaximum *float64
	// The draft-4 spelling, where the keyword is a boolean modifying `minimum`
	// rather than the bound itself. OpenAPI 3.0 documents use this one.
	ExclusiveMinimumFlag bool
	ExclusiveMaximumFlag bool

	MultipleOf *float64
}

// Describes reports whether the schema said anything about the value beyond its
// type, which is what separates an array whose contents are described from one
// whose items are a bare primitive.
func (c Constraints) Describes() bool {
	return c.MinLength != nil || c.MaxLength != nil ||
		c.Pattern != "" || c.Format != "" ||
		c.Minimum != nil || c.Maximum != nil ||
		c.ExclusiveMinimum != nil || c.ExclusiveMaximum != nil ||
		c.MultipleOf != nil
}

// String picks the shortest string the constraints permit.
//
// A pattern and a format each describe the value exactly and are asserted by
// the validator, so they come first: a length bound generally cannot be
// honoured alongside them, since a string matching a regex is whatever length
// that regex makes it, and of the two the exact constraint is the one a server
// enforces.
func (c Constraints) String() string {
	if c.Pattern != "" {
		if value, ok := FromPattern(c.Pattern); ok {
			return value
		}
	}
	if c.Format != "" {
		if value, ok := FromFormat(c.Format); ok {
			return value
		}
	}

	length := 0.0
	if c.MinLength != nil {
		length = *c.MinLength
	}
	if c.MaxLength != nil && *c.MaxLength < length {
		// A maxLength below the minLength permits nothing at all. Following the
		// upper bound keeps this to one impossible constraint rather than two.
		length = *c.MaxLength
	}
	if length <= 0 {
		return ""
	}
	return strings.Repeat("x", int(length))
}

// Number picks the smallest number the constraints permit, staying at the given
// starting point when that is already allowed.
//
// The start differs by format: OpenAPI 3 demonstrates a number with 0, while
// Swagger uses a deliberately implausible sample so a reader cannot mistake it
// for real data. Either is only a starting point, and the bounds move it.
func (c Constraints) Number(start float64) float64 {
	value := start

	if c.Minimum != nil && value < *c.Minimum {
		value = *c.Minimum
	}
	if c.ExclusiveMinimumFlag && c.Minimum != nil && value <= *c.Minimum {
		value = *c.Minimum + 1
	}
	if c.ExclusiveMinimum != nil && value <= *c.ExclusiveMinimum {
		value = *c.ExclusiveMinimum + 1
	}

	if c.MultipleOf != nil && *c.MultipleOf > 0 {
		// Rounding goes UP, because rounding down would satisfy multipleOf by
		// breaking the minimum just applied — and a minimum is rarely itself a
		// multiple. `multipleOf: 5, minimum: 7` permits 10, not 7 and not 5.
		value = math.Ceil(value / *c.MultipleOf) * *c.MultipleOf
	}

	if c.Maximum != nil && value > *c.Maximum {
		value = *c.Maximum
	}
	if c.ExclusiveMaximumFlag && c.Maximum != nil && value >= *c.Maximum {
		value = *c.Maximum - 1
	}
	if c.ExclusiveMaximum != nil && value >= *c.ExclusiveMaximum {
		value = *c.ExclusiveMaximum - 1
	}
	return value
}

// Items is how many elements an array must carry to satisfy a minItems.
func Items(minItems *float64) int {
	if minItems == nil || *minItems < 1 {
		return 1
	}
	return int(*minItems)
}
