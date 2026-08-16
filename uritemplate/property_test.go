package uritemplate

import (
	"net/url"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// TestAnExpandedValueSurvivesTheURL is the property this package exists to
// guarantee, and the one whose absence has cost this project three separate
// bugs: a request that goes somewhere other than where it was aimed.
//
// A value put into a path segment must be readable back out of the resulting
// URL as the same value. If the encoding is wrong the request reaches a
// different route, the failure that follows describes the wrong operation, and
// nothing in the report points anywhere near the cause.
//
// Checked by parsing the result with net/url — the same decoding a server does
// — rather than by comparing against the encoder's own rules, which would only
// establish that it agrees with itself.
func TestAnExpandedValueSurvivesTheURL(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		value := drawValue(rt)

		template, err := Parse("/resource/{id}/sub")
		if err != nil {
			rt.Fatalf("parsing: %v", err)
		}
		expanded := template.Expand(map[string]any{"id": value})

		parsed, err := url.Parse("http://example.com" + expanded)
		if err != nil {
			rt.Fatalf("expanded to %q, which is not a URL: %v", expanded, err)
		}

		// EscapedPath, not Path: url.Path is decoded, so a value containing an
		// encoded slash appears as a real one and the count is wrong. The
		// encoding is what a server routes on.
		segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
		if len(segments) != 3 {
			rt.Fatalf("value %q expanded to %q, which has %d path segments rather than 3 — "+
				"the request would reach a different route",
				value, expanded, len(segments))
		}
		if segments[0] != "resource" || segments[2] != "sub" {
			rt.Fatalf("value %q expanded to %q, which changed the surrounding path",
				value, expanded)
		}
	})
}

// TestAQueryValueSurvivesTheURL is the same guarantee for the query string,
// where a stray ampersand or equals sign would invent a parameter.
func TestAQueryValueSurvivesTheURL(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		value := drawValue(rt)

		template, err := Parse("/search{?q}")
		if err != nil {
			rt.Fatalf("parsing: %v", err)
		}
		expanded := template.Expand(map[string]any{"q": value})

		parsed, err := url.Parse("http://example.com" + expanded)
		if err != nil {
			rt.Fatalf("expanded to %q, which is not a URL: %v", expanded, err)
		}

		query := parsed.Query()
		if len(query) != 1 {
			rt.Fatalf("value %q expanded to %q, which carries %d query parameters rather than 1",
				value, expanded, len(query))
		}
		if query.Get("q") != value {
			rt.Fatalf("value %q came back as %q from %q", value, query.Get("q"), expanded)
		}
	})
}

// drawValue produces the kinds of value a description actually supplies for a
// parameter, weighted towards the ones that break encoders.
func drawValue(t *rapid.T) string {
	return rapid.SampledFrom([]string{
		"simple", "7", "a-b_c.d~e",
		"with space", "with+plus", "with%25percent", "with&ampersand",
		"with=equals", "with?question", "with#hash", "with/slash",
		"with\"quote", "with'apostrophe", "with\\backslash",
		"café", "中文", "🎉", "ß",
		"a:b", "a;b", "a,b", "a@b", "a[b]", "a{b}",
		"..", ".", "-", "_",
		// Control characters, which the unpadded-hex defect corrupted along
		// with whatever followed them.
		"a\nb", "a\tb", "a\rb", "\x01x",
	}).Draw(t, "value")
}

// TestParseNeverPanics covers templates nobody meant to write, which appear in
// descriptions as readily as the ones somebody did.
//
// The template comes from a document vertrag was asked to read, so a panic
// lands on the person least able to do anything about it — and this parser is
// hand-written, which is exactly the code where a malformed input finds an
// index nobody bounded.
func TestParseNeverPanics(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		template := rapid.StringOfN(
			rapid.RuneFrom([]rune("{}/?&#+;.*:,()abc019 %~")), 0, 24, -1).Draw(rt, "template")

		parsed, err := Parse(template)
		if err != nil {
			return
		}
		// Expanding is half the surface, and a template that parsed must
		// expand rather than panicking on the values it was given.
		parsed.Expand(map[string]any{
			"a": "1", "b": []any{"x", "y"}, "c": map[string]any{"k": "v"},
			"abc": "value", "": "empty-name",
		})
	})
}
