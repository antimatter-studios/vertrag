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
			[]string{"the response body: got number, want string"}},
		{"integer accepts a whole number", `{"type":"integer"}`, `5`, nil},
		{"integer rejects a fraction", `{"type":"integer"}`, `5.5`,
			[]string{"the response body: got number, want integer"}},
		{"number accepts a fraction", `{"type":"number"}`, `5.5`, nil},
		{"boolean accepts a boolean", `{"type":"boolean"}`, `true`, nil},
		{"null accepts null", `{"type":"null"}`, `null`, nil},
		{"array accepts an array", `{"type":"array"}`, `[]`, nil},
		{"object accepts an object", `{"type":"object"}`, `{}`, nil},
		{"a union type accepts any member", `{"type":["string","null"]}`, `null`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := check(t, test.schema, test.body)
			assertErrors(t, result, test.errors)
		})
	}
}

// TestConstraintsBeyondTypeAreChecked is the regression this file exists for.
//
// An earlier hand-rolled validator implemented type, required, properties,
// items and enum, and silently accepted everything below. Each of these was a
// contract violation vertrag reported as a pass.
func TestConstraintsBeyondTypeAreChecked(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema string
		body   string
	}{
		{"pattern", `{"type":"object","properties":{"s":{"type":"string","pattern":"^[a-z]+$"}}}`, `{"s":"AB"}`},
		{"minLength", `{"type":"object","properties":{"s":{"type":"string","minLength":5}}}`, `{"s":"ab"}`},
		{"maxLength", `{"type":"object","properties":{"s":{"type":"string","maxLength":1}}}`, `{"s":"abc"}`},
		{"minimum", `{"type":"object","properties":{"n":{"type":"number","minimum":10}}}`, `{"n":1}`},
		{"maximum", `{"type":"object","properties":{"n":{"type":"number","maximum":10}}}`, `{"n":99}`},
		{"multipleOf", `{"type":"object","properties":{"n":{"type":"number","multipleOf":5}}}`, `{"n":7}`},
		{"minItems", `{"type":"object","properties":{"a":{"type":"array","minItems":2}}}`, `{"a":[1]}`},
		{"maxItems", `{"type":"object","properties":{"a":{"type":"array","maxItems":1}}}`, `{"a":[1,2]}`},
		{"uniqueItems", `{"type":"object","properties":{"a":{"type":"array","uniqueItems":true}}}`, `{"a":[1,1]}`},
		{"minProperties", `{"type":"object","minProperties":2}`, `{"a":1}`},
		{"additionalProperties", `{"type":"object","properties":{"a":{}},"additionalProperties":false}`, `{"a":1,"b":2}`},
		{"allOf", `{"allOf":[{"type":"object","required":["a"]},{"type":"object","required":["b"]}]}`, `{"a":1}`},
		{"anyOf", `{"anyOf":[{"type":"string"},{"type":"number"}]}`, `{}`},
		{"oneOf", `{"oneOf":[{"type":"string"},{"type":"number"}]}`, `{}`},
		{"not", `{"not":{"type":"object"}}`, `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if result := check(t, test.schema, test.body); result.Valid {
				t.Errorf("%s violation was accepted", test.name)
			}
		})
	}
}

// TestReferencedSchemaReportsTheUnderlyingFailure pins that a `$ref` reports
// what actually went wrong rather than "validation failed" — the shape both
// OpenAPI parsers emit for a referenced schema.
func TestReferencedSchemaReportsTheUnderlyingFailure(t *testing.T) {
	result := check(t,
		`{"$ref":"#/definitions/T","definitions":{"T":{"type":"object","required":["a"]}}}`, `{}`)
	assertErrors(t, result, []string{"the response body: missing property 'a'"})
}

// TestDraftSelection pins that a schema declaring a modern dialect is read
// under it, which is what OpenAPI 3.1 needs.
func TestDraftSelection(t *testing.T) {
	// `const` is 2019-09 onwards; under draft-4 it would simply be ignored.
	schema := `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"c":{"const":"x"}}}`
	if result := check(t, schema, `{"c":"y"}`); result.Valid {
		t.Error("const should be enforced under 2020-12")
	}
}

// TestNumericExclusiveMinimum is the keyword that makes the declared dialect
// load-bearing rather than cosmetic.
//
// draft-4 spells exclusiveMinimum as a boolean modifying `minimum`; 2019-09
// onwards spells it as the bound itself. The same document therefore means two
// different things depending on which dialect is claimed, and a validator told
// the wrong one does not under-check quietly — Gavel rejects the schema
// outright with "Provided JSON Schema is not a valid JSON Schema draftV4",
// because a number is not a legal value for the keyword it thinks it is
// reading. Dredd stamps draft-04 on every schema it emits, including those it
// took from an OpenAPI 3.1 document, so this is a document it cannot test.
func TestNumericExclusiveMinimum(t *testing.T) {
	schema := `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object",` +
		`"properties":{"n":{"type":"integer","exclusiveMinimum":5}}}`

	if result := check(t, schema, `{"n":1}`); result.Valid {
		t.Error("n=1 should violate exclusiveMinimum 5")
	}
	if result := check(t, schema, `{"n":6}`); !result.Valid {
		t.Errorf("n=6 should satisfy exclusiveMinimum 5: %v", result.Errors)
	}
}

func TestRequiredAndProperties(t *testing.T) {
	schema := `{"type":"object","required":["a","b"],"properties":{"a":{"type":"string"}}}`

	assertErrors(t, check(t, schema, `{"a":"x","b":1}`), nil)
	if result := check(t, schema, `{"a":"x"}`); result.Valid {
		t.Error("a missing required property should fail")
	}
	if result := check(t, schema, `{}`); result.Valid {
		t.Error("missing required properties should fail")
	}
}

// TestWrongTypeStopsDescent pins that a value of the wrong type is reported
// once, rather than also being picked apart for missing properties it could
// never have had.
func TestWrongTypeStopsDescent(t *testing.T) {
	result := check(t, `{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`, `"a string"`)
	assertErrors(t, result, []string{"the response body: got string, want object"})
}

func TestNestedPointers(t *testing.T) {
	schema := `{"type":"object","properties":{"o":{"type":"object","required":["x"],"properties":{"x":{"type":"string"}}}}}`
	assertErrors(t, check(t, schema, `{"o":{"x":5}}`),
		[]string{"/o/x: got number, want string"})
	assertErrors(t, check(t, schema, `{"o":{}}`),
		[]string{"/o: missing property 'x'"})
}

func TestArrayItems(t *testing.T) {
	schema := `{"type":"array","items":{"type":"object","required":["id"],"properties":{"id":{"type":"integer"}}}}`
	assertErrors(t, check(t, schema, `[{"id":1},{"id":"two"},{}]`), []string{
		"/1/id: got string, want integer",
		"/2: missing property 'id'",
	})
}

func TestEnum(t *testing.T) {
	schema := `{"type":"object","properties":{"k":{"enum":["a","b"]}}}`
	assertErrors(t, check(t, schema, `{"k":"a"}`), nil)
	assertErrors(t, check(t, schema, `{"k":"z"}`), []string{"/k: value must be one of 'a', 'b'"})
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
