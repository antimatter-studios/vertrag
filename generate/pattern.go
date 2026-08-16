package generate

import (
	"regexp/syntax"
	"strings"
)

// FromPattern produces the shortest string a regular expression matches.
//
// A schema saying `pattern: "^[a-f0-9]{6}$"` is describing its values exactly,
// and a specimen that ignores it is one the document itself calls invalid. That
// is not academic: the specimen is sent as a request body, so a server enforcing
// the pattern rejects it and the run blames the server.
//
// The parsing is regexp/syntax from the standard library and only the walk over
// its tree is here. The three Go packages that do the whole job were last
// touched between 2020 and 2023 and have little use between them, and a
// contract tester whose correctness rests on an unmaintained micro-dependency
// has a problem it cannot fix later — the exact position Dredd is in, unable to
// support OpenAPI 3.1 because the validator it depends on stopped at draft-07.
// The hard half is the parser, and that is the standard library's.
//
// RE2 has no backreferences and no lookaround, so every node in the tree can be
// generated from independently. That is what makes this tractable rather than a
// constraint solver.
//
// The result is deterministic: the same pattern yields the same string, because
// a run has to be reproducible and a specimen that changes between runs would
// make every diff noise.
func FromPattern(pattern string) (string, bool) {
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return "", false
	}

	var b strings.Builder
	if !writeMatch(&b, parsed.Simplify()) {
		return "", false
	}
	return b.String(), true
}

// maxPatternRepeat bounds an unbounded repetition.
//
// A `+` needs one repetition to match and nothing is gained by more, so the
// shortest match is also the cheapest to read in a failure report.
const maxPatternRepeat = 1

func writeMatch(b *strings.Builder, node *syntax.Regexp) bool {
	switch node.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary,
		syntax.OpNoWordBoundary:
		// Anchors and empty matches contribute no characters.
		return true

	case syntax.OpLiteral:
		b.WriteString(string(node.Rune))
		return true

	case syntax.OpCharClass:
		rune, ok := firstUsableRune(node.Rune)
		if !ok {
			return false
		}
		b.WriteRune(rune)
		return true

	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		// Any character will do, so pick one that survives being pasted into a
		// terminal, a URL and a report.
		b.WriteByte('x')
		return true

	case syntax.OpCapture:
		return writeMatch(b, node.Sub[0])

	case syntax.OpConcat:
		for _, sub := range node.Sub {
			if !writeMatch(b, sub) {
				return false
			}
		}
		return true

	case syntax.OpAlternate:
		// The first branch that can be generated at all. A later branch being
		// impossible says nothing about this one.
		for _, sub := range node.Sub {
			var attempt strings.Builder
			if writeMatch(&attempt, sub) {
				b.WriteString(attempt.String())
				return true
			}
		}
		return false

	case syntax.OpStar, syntax.OpQuest:
		// Both match the empty string, which is the shortest thing they match.
		return true

	case syntax.OpPlus:
		return repeat(b, node.Sub[0], maxPatternRepeat)

	case syntax.OpRepeat:
		return repeat(b, node.Sub[0], node.Min)

	case syntax.OpNoMatch:
		// A pattern matching nothing has no specimen, and inventing one would
		// mean sending a body the document forbids.
		return false

	default:
		return false
	}
}

func repeat(b *strings.Builder, node *syntax.Regexp, times int) bool {
	for i := 0; i < times; i++ {
		if !writeMatch(b, node) {
			return false
		}
	}
	return true
}

// firstUsableRune picks a character from a class.
//
// The ranges arrive sorted, so the first is the lowest — frequently a control
// character when the class is a negation like `[^a]`, which begins at NUL. A
// specimen containing NUL is unusable in a report and may not survive being
// sent, so the search prefers something printable and only falls back to the
// literal first when the class permits nothing else.
func firstUsableRune(ranges []rune) (rune, bool) {
	if len(ranges) == 0 {
		return 0, false
	}

	for i := 0; i+1 < len(ranges); i += 2 {
		low, high := ranges[i], ranges[i+1]
		if low > high {
			continue
		}
		// Start at the first printable ASCII the range contains.
		candidate := low
		if candidate < ' ' {
			candidate = ' '
		}
		if candidate <= high && candidate >= ' ' && candidate < 0x7f {
			return candidate, true
		}
	}

	// Nothing printable: the class is genuinely made of control characters or
	// lies outside ASCII, and its own first character is the honest answer.
	return ranges[0], true
}
