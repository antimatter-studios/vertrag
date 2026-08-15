package uritemplate

import "testing"

// These tests pin the deviations from RFC 6570 that vertrag reproduces on
// purpose. They matter more than the conforming cases: a later "cleanup" that
// made the encoder correct would pass a spec-conformance test and silently
// disagree with Dredd about every URI containing a reserved character.

func TestExpandOperators(t *testing.T) {
	vars := map[string]any{
		"var":   "value",
		"hello": "Hello World!",
		"empty": "",
		"num":   float64(42),
		"list":  []any{"red", "green"},
	}

	for _, test := range []struct {
		template string
		want     string
	}{
		{"/plain", "/plain"},
		{"{var}", "value"},
		{"/a/{var}/b", "/a/value/b"},
		{"{hello}", "Hello%20World%21"},
		{"{+hello}", "Hello%20World!"},
		{"{#hello}", "#Hello%20World!"},
		{"{.var}", ".value"},
		{"{/var}", "/value"},
		{"{;var}", ";var=value"},
		{"{?var}", "?var=value"},
		{"{&var}", "&var=value"},
		{"{?var,num}", "?var=value&num=42"},
		{"{?empty}", "?empty="},
		{"{?missing}", ""},
		{"{var:3}", "val"},
		{"{list}", "red,green"},
		{"{?list*}", "?list=red&list=green"},
		{"/a{?var}suffix", "/a?var=valuesuffix"},
	} {
		parsed, err := Parse(test.template)
		if err != nil {
			t.Errorf("Parse(%q): %v", test.template, err)
			continue
		}
		if got := parsed.Expand(vars); got != test.want {
			t.Errorf("Expand(%q) = %q, want %q", test.template, got, test.want)
		}
	}
}

// TestEncodingQuirks pins the reference encoder's defects.
func TestEncodingQuirks(t *testing.T) {
	for _, test := range []struct {
		name     string
		template string
		value    string
		want     string
	}{
		{
			// The escape is built without zero padding, so a byte below 0x10
			// yields a single hex digit. This is wrong, and it is what Dredd
			// emits.
			name: "unpadded escape below 0x10", template: "{v}", value: "a\nb", want: "a%Ab",
		},
		{
			// The reserved encoder leaves % alone only when two DIGITS follow,
			// so an existing escape is escaped again.
			name: "already-escaped sequence is double-escaped", template: "{+v}", value: "%2F", want: "%252F",
		},
		{
			name: "percent followed by digits is left alone", template: "{+v}", value: "%20", want: "%20",
		},
		{
			name: "unreserved set is not escaped", template: "{v}", value: "aZ0_~.-", want: "aZ0_~.-",
		},
		{
			name: "reserved characters survive the plus operator", template: "{+v}", value: "/a?b#c[d]", want: "/a?b#c[d]",
		},
		{
			name: "reserved characters are escaped by the simple operator", template: "{v}", value: "/a?b", want: "%2Fa%3Fb",
		},
		{
			// Non-ASCII is encoded per UTF-16 code unit, so a character outside
			// the BMP becomes its surrogate halves rather than proper UTF-8.
			name: "multi-byte characters", template: "{v}", value: "é", want: "%C3%A9",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := Parse(test.template)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := parsed.Expand(map[string]any{"v": test.value}); got != test.want {
				t.Errorf("Expand(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

// TestDefinedness pins which values count as present.
//
// A scalar is always defined, the empty string included, while a collection
// holding nothing truthy is treated as absent. That asymmetry decides whether a
// query parameter appears at all.
func TestDefinedness(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		want  string
	}{
		{"empty string is present", "", "?v="},
		{"zero is present", float64(0), "?v=0"},
		{"false is present", false, "?v=false"},
		{"empty list is absent", []any{}, ""},
		{"list of falsy values is absent", []any{"", float64(0)}, ""},
		{"list with one truthy value is present", []any{"", "x"}, "?v=,x"},
		{"nil is absent", nil, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := Parse("{?v}")
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := parsed.Expand(map[string]any{"v": test.value}); got != test.want {
				t.Errorf("Expand(%#v) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

// TestParseErrors pins the generated parser's error reporting, which reaches
// users verbatim as a parser annotation.
func TestParseErrors(t *testing.T) {
	for _, test := range []struct {
		template string
		want     string
	}{
		{
			"/honey{",
			`Expected "(", "*", ",", ":", "}", [\/;:.?&+#] or [a-zA-Z0-9_.%] but end of input found.`,
		},
		{
			// ':' is accepted by the grammar but has no expansion rule, so
			// constructing the expression fails rather than the parse.
			"{:v}",
			`unsupported URI template operator ":"`,
		},
	} {
		_, err := Parse(test.template)
		if err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", test.template)
			continue
		}
		if err.Error() != test.want {
			t.Errorf("Parse(%q) error = %q, want %q", test.template, err.Error(), test.want)
		}
	}
}

func TestParseAcceptsEmptyAndBareTemplates(t *testing.T) {
	for _, template := range []string{"", "/no/expressions", "{}"} {
		if _, err := Parse(template); err != nil {
			t.Errorf("Parse(%q): unexpected error %v", template, err)
		}
	}
}
