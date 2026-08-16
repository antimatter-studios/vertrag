package openapi3

import (
	"encoding/json"
	"testing"

	"github.com/antimatter-studios/vertrag/validate"
)

// violations pairs each JSON Schema keyword with a value the keyword forbids.
//
// It is the fixed point of the test below: if vertrag rejects the value, the
// keyword is being acted on, whatever any list claims.
var violations = []struct {
	keyword string
	schema  string
	body    string
}{
	{"multipleOf", `{"type":"integer","multipleOf":5}`, `7`},
	{"maximum", `{"type":"integer","maximum":10}`, `11`},
	{"exclusiveMaximum", `{"type":"integer","maximum":10,"exclusiveMaximum":true}`, `10`},
	{"minimum", `{"type":"integer","minimum":10}`, `9`},
	{"exclusiveMinimum", `{"type":"integer","minimum":10,"exclusiveMinimum":true}`, `10`},
	{"maxLength", `{"type":"string","maxLength":2}`, `"abc"`},
	{"minLength", `{"type":"string","minLength":3}`, `"ab"`},
	{"pattern", `{"type":"string","pattern":"^[a-z]+$"}`, `"AB"`},
	{"format", `{"type":"string","format":"email"}`, `"not-an-email"`},
	{"maxItems", `{"type":"array","maxItems":1}`, `[1,2]`},
	{"minItems", `{"type":"array","minItems":2}`, `[1]`},
	{"uniqueItems", `{"type":"array","uniqueItems":true}`, `[1,1]`},
	{"maxProperties", `{"type":"object","maxProperties":1}`, `{"a":1,"b":2}`},
	{"minProperties", `{"type":"object","minProperties":2}`, `{"a":1}`},
	{"required", `{"type":"object","required":["a"]}`, `{}`},
	{"enum", `{"enum":["a"]}`, `"b"`},
	{"const", `{"$schema":"https://json-schema.org/draft/2020-12/schema","const":"a"}`, `"b"`},
	{"allOf", `{"allOf":[{"type":"string"},{"type":"integer"}]}`, `"a"`},
	{"anyOf", `{"anyOf":[{"type":"string"},{"type":"integer"}]}`, `{}`},
	{"oneOf", `{"oneOf":[{"type":"string"},{"type":"integer"}]}`, `{}`},
	{"not", `{"not":{"type":"string"}}`, `"a"`},
	{"additionalProperties", `{"type":"object","properties":{"a":{}},"additionalProperties":false}`, `{"a":1,"b":2}`},
	{"items", `{"type":"array","items":{"type":"string"}}`, `[1]`},
	{"properties", `{"type":"object","properties":{"a":{"type":"string"}}}`, `{"a":1}`},
	{"type", `{"type":"string"}`, `1`},
	// A reference is acted on wherever a schema can appear: the target's
	// constraints reach the compiled transaction and are enforced. It is in
	// this table because it was listed unsupported for parameter schemas while
	// being resolved for them, which the guard could not see while the only
	// keywords it knew were the ones a body is validated with.
	{"$ref", `{"$ref":"#/definitions/T","definitions":{"T":{"type":"string"}}}`, `1`},
}

// TestNoActedOnKeywordIsCalledUnsupported is a structural guard against a
// mistake vertrag has now made three times.
//
// The `unsupported` lists are transcribed from Dredd's parser, which acts on
// almost nothing beyond `type` and `enum`. Every capability vertrag gains makes
// another entry in them false, and the warning they produce — "'Schema Object'
// contains unsupported key 'minLength'" — tells a reader that a constraint does
// nothing. The natural response to being told that is to delete the constraint,
// which is precisely the one being enforced. It has happened for the Schema
// Object, for the Header Object and for parameter schemas, each caught by
// accident and each after the misleading warning had already shipped.
//
// So the lists are checked against behaviour rather than against a memory of
// what was implemented when they were written. A keyword vertrag enforces is a
// keyword vertrag supports, whatever any list says.
func TestNoActedOnKeywordIsCalledUnsupported(t *testing.T) {
	for _, spec := range []objectSpec{specSchema, specParameterSchema} {
		for _, keyword := range spec.unsupported {
			probe, known := violationFor(keyword)
			if !known {
				// Not a validation keyword, or not one with a value that can
				// be violated — `xml` and `externalDocs` describe presentation
				// and lineage, and no body can contradict them.
				continue
			}

			result := validate.AgainstSchema(json.RawMessage(probe.schema), probe.body)
			if !result.Valid {
				t.Errorf("%s lists %q as unsupported, but vertrag enforces it: %s was rejected (%v)",
					spec.name, keyword, probe.body, result.Errors)
			}
		}
	}
}

// TestEveryEnforcedKeywordIsListedAsSupported is the same check from the other
// side, so a keyword vertrag acts on cannot simply be absent from both lists
// and go unmentioned.
func TestEveryEnforcedKeywordIsListedAsSupported(t *testing.T) {
	for _, probe := range violations {
		if result := validate.AgainstSchema(json.RawMessage(probe.schema), probe.body); result.Valid {
			// Not enforced, so there is nothing to claim.
			continue
		}

		if !listed(specSchema.supported, probe.keyword) {
			t.Errorf("vertrag enforces %q but the Schema Object does not list it as supported", probe.keyword)
		}
	}
}

func violationFor(keyword string) (struct {
	keyword string
	schema  string
	body    string
}, bool) {
	for _, probe := range violations {
		if probe.keyword == keyword {
			return probe, true
		}
	}
	return struct {
		keyword string
		schema  string
		body    string
	}{}, false
}

func listed(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}
