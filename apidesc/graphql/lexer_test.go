package graphql

import (
	"reflect"
	"strings"
	"testing"
)

// TestNumberLiteralsKeepTheDifferenceBetweenIntAndFloat pins the distinction
// the lexer exists to make. A server rejects `1.0` where an Int belongs, so a
// default read as the wrong one of the two produces a request that fails for a
// reason the schema never described.
func TestNumberLiteralsKeepTheDifferenceBetweenIntAndFloat(t *testing.T) {
	schema, _ := parseSchema(t, `
type Query {
  numbers(
    whole: Int = -5
    fraction: Float = -1.5
    exponent: Float = 1e3
    small: Float = 1.5E-2
    zero: Int = 0
  ): String
}
`)

	args := map[string]Argument{}
	for _, arg := range fieldNamed(t, typeNamed(t, schema, "Query"), "numbers").Args {
		args[arg.Name] = arg
	}

	for _, want := range []struct {
		name  string
		value any
	}{
		{"whole", int64(-5)},
		{"fraction", -1.5},
		{"exponent", 1000.0},
		{"small", 0.015},
		{"zero", int64(0)},
	} {
		if !reflect.DeepEqual(args[want.name].Default, want.value) {
			t.Errorf("%s = %#v (%T), want %#v (%T)", want.name,
				args[want.name].Default, args[want.name].Default, want.value, want.value)
		}
	}
}

func TestUnicodeEscapesAreDecodedInBothSpellings(t *testing.T) {
	// The escapes are assembled rather than written out, so that what this file
	// contains is the escape itself and not the character an editor or a tool
	// helpfully decoded it into.
	esc := `\`

	for _, spelling := range []struct {
		written string
		want    string
	}{
		{esc + "u00e9", "é"},
		// A code point above the basic plane has two spellings: the surrogate
		// pair the four-digit escape has to be recombined from, and the braced
		// form the 2021 specification added.
		{esc + "ud83d" + esc + "ude00", "\U0001F600"},
		{esc + "u{1F600}", "\U0001F600"},
	} {
		schema, _ := parseSchema(t, `"`+spelling.written+`"`+"\ntype Query { a: String }")
		if got := typeNamed(t, schema, "Query").Description; got != spelling.want {
			t.Errorf("%s decoded to %q, want %q", spelling.written, got, spelling.want)
		}
	}
}

func TestABlockStringCanContainTripleQuotes(t *testing.T) {
	quotes := `"""`
	schema, _ := parseSchema(t, quotes+`Say \`+quotes+` like this.`+quotes+"\ntype Query { a: String }")

	if got, want := typeNamed(t, schema, "Query").Description, `Say """ like this.`; got != want {
		t.Errorf("description = %q, want %q", got, want)
	}
}

func TestAStringThatIsNotClosedIsRefusedWhereItStarts(t *testing.T) {
	for _, source := range []struct {
		document string
		says     string
	}{
		{"type Query { a: String }\n\"unfinished\ntype Other { b: String }", "not closed"},
		{`"bad \q escape"` + "\ntype Query { a: String }", "not a string escape"},
	} {
		_, _, err := Parse([]byte(source.document))
		if err == nil {
			t.Fatalf("this parsed, and should not have:\n%s", source.document)
		}
		if !strings.Contains(err.Error(), source.says) {
			t.Errorf("error = %v, want it to say %q", err, source.says)
		}
	}
}

// TestAnUnreadableCharacterIsNamed covers what a schema written in the wrong
// encoding hits. The message has to say what was found, because the character
// is usually invisible in an editor.
func TestAnUnreadableCharacterIsNamed(t *testing.T) {
	_, _, err := Parse([]byte("type Query { a: String }\n%\n"))
	if err == nil {
		t.Fatalf("a stray character parsed")
	}
	if !strings.Contains(err.Error(), "'%'") {
		t.Errorf("error = %v, want it to quote the character", err)
	}
}
