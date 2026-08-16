package generate

import (
	"math"
	"testing"
)

// TestNumberSatisfiesItsOwnConstraints is the property that matters: whatever
// the arithmetic does, the result has to be a value the schema permits.
//
// Binary floating point makes that harder than it sounds. `0.3 / 0.1` is
// 2.9999999999999996, so ceiling it jumps a whole step; `3 * 0.1` is
// 0.30000000000000004, so even the right multiple is not one. Both produced
// specimens the validator rejected against the very constraint they came from.
func TestNumberSatisfiesItsOwnConstraints(t *testing.T) {
	at := func(v float64) *float64 { return &v }

	for _, test := range []struct {
		name        string
		constraints Constraints
		want        float64
	}{
		{"a whole step", Constraints{MultipleOf: at(5), Minimum: at(7)}, 10},
		{"a step the minimum already satisfies", Constraints{MultipleOf: at(5), Minimum: at(10)}, 10},
		{"tenths", Constraints{MultipleOf: at(0.1), Minimum: at(0.3)}, 0.3},
		{"tenths again", Constraints{MultipleOf: at(0.1), Minimum: at(0.7)}, 0.7},
		{"quarters", Constraints{MultipleOf: at(0.25), Minimum: at(0.3)}, 0.5},
		{"hundredths", Constraints{MultipleOf: at(0.01), Minimum: at(0.07)}, 0.07},
		// Zero is a multiple of everything and satisfies a negative minimum,
		// so the starting point already qualifies and nothing moves it. The
		// smallest PERMITTED value is not the same as the lower bound.
		{"a negative minimum leaves zero alone", Constraints{MultipleOf: at(0.1), Minimum: at(-0.3)}, 0},
		{"thirds, which do not divide evenly", Constraints{MultipleOf: at(0.3), Minimum: at(0.5)}, 0.6},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := test.constraints.Number(0)
			if math.Abs(got-test.want) > 1e-12 {
				t.Errorf("Number(0) = %v, want %v", got, test.want)
			}

			// The property, independent of the expected value: the result must
			// be an exact multiple, and must not print with the drift that
			// makes a validator reject it.
			step := *test.constraints.MultipleOf
			if remainder := math.Abs(math.Remainder(got, step)); remainder > 1e-12 {
				t.Errorf("%v is not a multiple of %v (remainder %v)", got, step, remainder)
			}
			if got < *test.constraints.Minimum {
				t.Errorf("%v is below the minimum of %v", got, *test.constraints.Minimum)
			}
		})
	}
}

// TestDecimalPlacesRefusesExponentForm pins that a step too large or too small
// to write plainly is left alone. Rounding to a digit count that does not exist
// would do more harm than the drift it prevents.
func TestDecimalPlacesRefusesExponentForm(t *testing.T) {
	for _, test := range []struct {
		step float64
		want int
	}{
		{5, 0},
		{0.1, 1},
		{0.25, 2},
		{0.001, 3},
		// 'f' never emits exponent form, so 1e21 is a plain integer with no
		// decimals at all.
		{1e21, 0},
		// Past what a float64 can distinguish, the digits being rounded are the
		// representation error itself, so rounding is refused.
		{1e-20, -1},
	} {
		if got := decimalPlaces(test.step); got != test.want {
			t.Errorf("decimalPlaces(%v) = %d, want %d", test.step, got, test.want)
		}
	}
}
