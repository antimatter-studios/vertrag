package generate

import (
	"regexp"
	"regexp/syntax"
	"testing"

	"pgregory.net/rapid"
)

// TestFromPatternProducesSomethingTheRegexMatches is the property that matters,
// and it is checked against the regexp engine rather than against a table.
//
// A table says the cases someone thought of. This says: whatever pattern comes
// out of the generator below, if a string is produced for it then that string
// matches — verified by compiling the pattern and running it. A specimen that
// does not match is one the description forbids, and it is sent as a request.
func TestFromPatternProducesSomethingTheRegexMatches(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		pattern := drawPattern(rt, 0)

		value, ok := FromPattern(pattern)
		if !ok {
			// Refusing is always allowed: some patterns match nothing at all,
			// and inventing a specimen for those would be worse than none.
			return
		}

		compiled, err := regexp.Compile(pattern)
		if err != nil {
			rt.Fatalf("generated a value for %q, which does not compile", pattern)
		}
		if !compiled.MatchString(value) {
			rt.Fatalf("pattern %q produced %q, which it does not match", pattern, value)
		}
	})
}

// TestFromPatternNeverPanics covers the inputs a description actually contains,
// which include patterns nobody meant to write.
//
// A parser that panics on a malformed pattern takes the whole run with it, and
// the pattern came from a document vertrag was asked to read — so the crash
// lands on the person who cannot do anything about it.
func TestFromPatternNeverPanics(t *testing.T) {
	for _, pattern := range []string{
		"", "(", ")", "[", "]", "*", "+", "?", "{", "}", "|",
		"a{2,1}", "[z-a]", "(?P<n>a)(?P<n>b)", "\\", "\\p{Nope}",
		"(((((((((((a)))))))))))", "a{1000000}", "[^\\x00-\\x{10FFFF}]",
		"^$", "(?i)ABC", "(?s).", "\\b\\B", "(?:)", "[[:alpha:]]",
	} {
		// The assertion is reaching the next line.
		value, ok := FromPattern(pattern)
		if !ok {
			continue
		}
		if compiled, err := regexp.Compile(pattern); err == nil && !compiled.MatchString(value) {
			t.Errorf("pattern %q produced %q, which it does not match", pattern, value)
		}
	}
}

// drawPattern builds a regular expression out of the constructs a description
// plausibly contains.
//
// Deliberately not arbitrary text: a random string is almost never a valid
// pattern, so the check would spend its time on the refusal path and establish
// nothing about the part that generates.
func drawPattern(t *rapid.T, depth int) string {
	if depth > 3 {
		return rapid.SampledFrom([]string{"a", "b", "[0-9]", "[a-z]"}).Draw(t, "leaf")
	}

	switch rapid.IntRange(0, 8).Draw(t, "shape") {
	case 0:
		return rapid.SampledFrom([]string{"a", "xyz", "0", "-", "_", "."}).Draw(t, "literal")
	case 1:
		return rapid.SampledFrom([]string{
			"[a-z]", "[A-Z]", "[0-9]", "[a-f0-9]", "[^0-9]", "[abc]", "\\d", "\\w", "\\s",
		}).Draw(t, "class")
	case 2:
		return drawPattern(t, depth+1) + drawPattern(t, depth+1)
	case 3:
		return "(" + drawPattern(t, depth+1) + ")"
	case 4:
		return "(?:" + drawPattern(t, depth+1) + ")"
	case 5:
		return drawPattern(t, depth+1) + "|" + drawPattern(t, depth+1)
	case 6:
		return drawPattern(t, depth+1) +
			rapid.SampledFrom([]string{"*", "+", "?"}).Draw(t, "quantifier")
	case 7:
		low := rapid.IntRange(0, 4).Draw(t, "low")
		return drawPattern(t, depth+1) +
			"{" + itoa(low) + "," + itoa(low+rapid.IntRange(0, 3).Draw(t, "span")) + "}"
	default:
		return "^" + drawPattern(t, depth+1) + "$"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestFromPatternRefusesAPatternMatchingNothing pins that an impossible pattern
// yields no specimen rather than an arbitrary one.
func TestFromPatternRefusesAPatternMatchingNothing(t *testing.T) {
	// syntax.OpNoMatch: a character class with nothing in it.
	parsed, err := syntax.Parse(`[^\x00-\x{10FFFF}]`, syntax.Perl)
	if err != nil {
		t.Skipf("the engine rejects this outright: %v", err)
	}
	if parsed.Simplify().Op != syntax.OpNoMatch {
		t.Skipf("not the shape this test is about: %v", parsed.Simplify().Op)
	}

	if value, ok := FromPattern(`[^\x00-\x{10FFFF}]`); ok {
		t.Errorf("produced %q for a pattern that matches nothing", value)
	}
}
