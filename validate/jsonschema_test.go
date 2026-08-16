package validate

import (
	"encoding/json"
	"testing"
)

func check(t *testing.T, schema, body string) FieldResult {
	t.Helper()
	return validateAgainstSchema(json.RawMessage(schema), body)
}

func TestSchemaTypes(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema string
		body   string
		errors []string
	}{
		{"string accepts a string", `{"type":"string"}`, `"x"`, nil},
		{"string rejects a number", `{"type":"string"}`, `1`,
			[]string{"At '/' Invalid type: number (expected string)"}},
		{"integer accepts a whole number", `{"type":"integer"}`, `5`, nil},
		{"integer rejects a fraction", `{"type":"integer"}`, `5.5`,
			[]string{"At '/' Invalid type: number (expected integer)"}},
		{"number accepts a fraction", `{"type":"number"}`, `5.5`, nil},
		{"boolean accepts a boolean", `{"type":"boolean"}`, `true`, nil},
		{"null accepts null", `{"type":"null"}`, `null`, nil},
		{"array accepts an array", `{"type":"array"}`, `[]`, nil},
		{"object accepts an object", `{"type":"object"}`, `{}`, nil},
		{"a union type accepts any member", `{"type":["string","null"]}`, `null`, nil},
		{"a union type names all members when it fails", `{"type":["string","null"]}`, `1`,
			[]string{"At '/' Invalid type: number (expected string/null)"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := check(t, test.schema, test.body)
			assertErrors(t, result, test.errors)
		})
	}
}

// TestActualTypeIsNeverInteger pins that `integer` is something a schema asks
// for, never something a value is reported as being.
func TestActualTypeIsNeverInteger(t *testing.T) {
	result := check(t, `{"type":"string"}`, `5`)
	assertErrors(t, result, []string{"At '/' Invalid type: number (expected string)"})
}

func TestRequiredAndProperties(t *testing.T) {
	schema := `{"type":"object","required":["a","b"],"properties":{"a":{"type":"string"}}}`

	assertErrors(t, check(t, schema, `{"a":"x","b":1}`), nil)
	assertErrors(t, check(t, schema, `{"a":"x"}`),
		[]string{"At '/b' Missing required property: b"})

	// Missing keys are reported in sorted order so the output is stable.
	assertErrors(t, check(t, schema, `{}`), []string{
		"At '/a' Missing required property: a",
		"At '/b' Missing required property: b",
	})
}

// TestWrongTypeStopsDescent pins that a value of the wrong type is reported
// once, rather than also being picked apart for missing properties it could
// never have had.
func TestWrongTypeStopsDescent(t *testing.T) {
	result := check(t, `{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`, `"a string"`)
	assertErrors(t, result, []string{"At '/' Invalid type: string (expected object)"})
}

func TestNestedPointers(t *testing.T) {
	schema := `{"type":"object","properties":{"o":{"type":"object","required":["x"],"properties":{"x":{"type":"string"}}}}}`
	assertErrors(t, check(t, schema, `{"o":{"x":5}}`),
		[]string{"At '/o/x' Invalid type: number (expected string)"})
	assertErrors(t, check(t, schema, `{"o":{}}`),
		[]string{"At '/o/x' Missing required property: x"})
}

func TestArrayItems(t *testing.T) {
	schema := `{"type":"array","items":{"type":"object","required":["id"],"properties":{"id":{"type":"integer"}}}}`
	assertErrors(t, check(t, schema, `[{"id":1},{"id":"two"},{}]`), []string{
		"At '/1/id' Invalid type: string (expected integer)",
		"At '/2/id' Missing required property: id",
	})
}

func TestEnum(t *testing.T) {
	schema := `{"type":"object","properties":{"k":{"enum":["a","b"]}}}`
	assertErrors(t, check(t, schema, `{"k":"a"}`), nil)
	assertErrors(t, check(t, schema, `{"k":"z"}`), []string{`At '/k' No enum match for: "z"`})
}

// TestPointerEscaping pins JSON Pointer's two reserved characters.
func TestPointerEscaping(t *testing.T) {
	schema := `{"type":"object","required":["a/b","c~d"]}`
	assertErrors(t, check(t, schema, `{}`), []string{
		"At '/a~1b' Missing required property: a/b",
		"At '/c~0d' Missing required property: c~d",
	})
}

func TestUnparseableBodyIsNotAComparison(t *testing.T) {
	result := check(t, `{"type":"object"}`, `{{{`)
	if result.Valid {
		t.Fatal("an unparseable body should fail")
	}
	if result.Kind != nil {
		t.Errorf("kind = %q, want null", *result.Kind)
	}
}

func TestUnreadableSchemaIsNotAFailure(t *testing.T) {
	// A schema vertrag cannot read says nothing about the response. Failing the
	// test would blame the server for the description's problem.
	if result := check(t, `not a schema`, `{"a":1}`); !result.Valid {
		t.Error("an unreadable schema should not fail the response")
	}
}

func assertErrors(t *testing.T, result FieldResult, want []string) {
	t.Helper()

	if len(want) == 0 {
		if !result.Valid {
			t.Errorf("expected valid, got errors: %v", result.Errors)
		}
		return
	}

	if result.Valid {
		t.Fatalf("expected errors %v, got a valid result", want)
	}
	if len(result.Errors) != len(want) {
		t.Fatalf("got %d error(s) %v, want %d %v", len(result.Errors), result.Errors, len(want), want)
	}
	for i := range want {
		if result.Errors[i] != want[i] {
			t.Errorf("error[%d] = %q, want %q", i, result.Errors[i], want[i])
		}
	}
}
