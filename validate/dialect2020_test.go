package validate

import "testing"

// TestKeywordsAddedAfterDraft4 covers the JSON Schema keywords an OpenAPI 3.1
// document may use that draft-4 does not have.
//
// These are the ones Dredd cannot check. It emits 3.1 schemas verbatim but
// stamps `$schema: draft-04` on them, and Gavel validates with ajv 6, which
// tops out at draft-07 — so a keyword newer than that is either ignored or
// fatal. Measured against Gavel: prefixItems, dependentRequired and
// unevaluatedProperties are silently ignored, and a numeric exclusiveMinimum
// throws "Provided JSON Schema is not a valid JSON Schema draftV4" because
// draft-4 spells that keyword as a boolean.
//
// Each case is a body the schema forbids. Accepting one means the keyword is
// not being enforced.
func TestKeywordsAddedAfterDraft4(t *testing.T) {
	const dialect = `"$schema":"https://json-schema.org/draft/2020-12/schema",`

	for _, test := range []struct {
		name   string
		schema string
		body   string
	}{
		{"numeric exclusiveMinimum", `{"type":"integer","exclusiveMinimum":5}`, `1`},
		{"const", `{"const":"widget"}`, `"other"`},
		{"prefixItems", `{"type":"array","prefixItems":[{"type":"string"},{"type":"integer"}]}`, `[1,"x"]`},
		{"dependentRequired", `{"type":"object","dependentRequired":{"a":["b"]}}`, `{"a":1}`},
		{"unevaluatedProperties", `{"type":"object","properties":{"a":{}},"unevaluatedProperties":false}`, `{"a":1,"zz":2}`},
		{"if/then", `{"type":"object","if":{"required":["a"]},"then":{"required":["b"]}}`, `{"a":1}`},
		{"contentEncoding type", `{"type":"string","contentEncoding":"base64"}`, `5`},
		{"type array", `{"type":["string","null"]}`, `42`},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema := `{` + dialect + `"type":"object","properties":{"v":` + test.schema + `}}`
			if result := check(t, schema, `{"v":`+test.body+`}`); result.Valid {
				t.Errorf("%s was not enforced: body %s accepted", test.name, test.body)
			}
		})
	}
}
