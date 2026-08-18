package openapi3

import (
	"encoding/json"
	"strings"
	"testing"
)

// A claim vertrag makes about itself is a thing that can rot.
//
// There is already a guard in the other direction — no keyword vertrag
// enforces may be listed as unsupported — and it has caught the same mistake
// three times, because it measures behaviour rather than trusting a list.
// This is its inverse, and it exists because of the mistake the other one
// cannot see: a keyword listed as SUPPORTED that no branch consults. That is
// a promise to the reader that vertrag is honouring their constraint when it
// is doing nothing with it, and refactoring the branch away leaves the promise
// standing.
//
// Every supported keyword must therefore be accounted for in exactly one of
// three ways: the validator enforces it (the `violations` table), or it
// changes what vertrag generates (`generationKeywords` below), or it is
// genuinely inert and says why (`inertKeywords`). Adding a keyword to the
// supported list without accounting for it fails here.

// generationKeywords are the ones that change what vertrag SENDS rather than
// what it accepts, so no violating body can demonstrate them. Each is a
// schema and the substring its effect must produce in the generated request.
var generationKeywords = map[string]struct {
	schema string
	expect string
}{
	"default":   {`{"type":"object","properties":{"a":{"type":"string","default":"from-default"}}}`, "from-default"},
	"example":   {`{"type":"object","properties":{"a":{"type":"string","example":"from-example"}}}`, "from-example"},
	"nullable":  {`{"type":"object","properties":{"a":{"type":"string","nullable":true}}}`, `"a":null`},
	"readOnly":  {`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer","readOnly":true}}}`, ""},
	"writeOnly": {`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer","writeOnly":true}}}`, `"b"`},
}

// inertKeywords are accepted and act on nothing, with the reason. They are
// listed as supported because reporting them as unsupported would tell an
// author their perfectly good documentation was a problem.
var inertKeywords = map[string]string{
	"title":       "names the schema for a reader; no bearing on what is sent or accepted",
	"description": "prose for a reader, same",
}

func TestEverySupportedKeywordIsAccountedFor(t *testing.T) {
	for _, keyword := range specSchema.supported {
		if _, enforced := violationFor(keyword); enforced {
			continue
		}
		if _, generates := generationKeywords[keyword]; generates {
			continue
		}
		if reason, inert := inertKeywords[keyword]; inert {
			if reason == "" {
				t.Errorf("%q is listed inert with no reason", keyword)
			}
			continue
		}
		t.Errorf("%q is listed as supported but nothing here shows vertrag acts on it. "+
			"Add it to the violations table if the validator enforces it, to "+
			"generationKeywords if it changes what is sent, or to inertKeywords with "+
			"the reason it does neither — or stop claiming it.", keyword)
	}
}

// TestGenerationKeywordsActuallyChangeWhatIsSent is the half that keeps the
// table above honest: an entry claiming a keyword shapes the request must be
// demonstrated by the request changing.
func TestGenerationKeywordsActuallyChangeWhatIsSent(t *testing.T) {
	for keyword, test := range generationKeywords {
		t.Run(keyword, func(t *testing.T) {
			body := requestBodyFor(t, test.schema)

			// A keyword whose effect is an OMISSION says so with an empty
			// expectation: the property it names must not be there.
			if test.expect == "" {
				if strings.Contains(body, `"b"`) {
					t.Errorf("%s did not remove the property it should have: %s", keyword, body)
				}
				return
			}
			if !strings.Contains(body, test.expect) {
				t.Errorf("%s produced %s, which does not contain %q", keyword, body, test.expect)
			}
		})
	}
}

// requestBodyFor compiles a document whose request body carries the schema,
// and returns the body vertrag would send.
func requestBodyFor(t *testing.T, schema string) string {
	t.Helper()

	var decoded any
	if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
		t.Fatalf("schema: %v", err)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}

	result := compileSource(t, `
openapi: "3.0.3"
info: {title: T, version: "1"}
paths:
  /things:
    post:
      summary: Create
      requestBody:
        content:
          application/json:
            schema: `+string(encoded)+`
      responses:
        "200": {description: OK}
`)
	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d: %v", len(result.Transactions), result.Annotations)
	}
	return result.Transactions[0].Request.Body
}
