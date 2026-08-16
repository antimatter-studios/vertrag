package oracle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/antimatter-studios/vertrag/validate"
)

// validationCase is one expected/actual pair, as the corpus stores it.
type validationCase struct {
	Expected messageJSON `json:"expected"`
	Real     messageJSON `json:"real"`
}

// messageJSON mirrors the shape Gavel is handed, so the same file can be fed to
// both implementations without translation.
type messageJSON struct {
	StatusCode string            `json:"statusCode"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	BodySchema json.RawMessage   `json:"bodySchema"`
}

// TestValidateMatchesReference checks vertrag's pass/fail verdicts against
// Gavel's.
//
// Verdicts are compared, and which fields failed — not the wording. vertrag's
// messages are deliberately its own: Gavel inherits its text from two different
// validators and describes the same class of problem two ways depending on the
// keyword, and there is nothing to gain by reproducing that. What must not
// diverge is the answer — a response Gavel rejects has to be rejected here, or
// vertrag would pass a body that violates its contract.
//
// The wording is pinned separately, by the validate package's own tests.
func TestValidateMatchesReference(t *testing.T) {
	root := repoRoot(t)
	requireReference(t, root)

	dir := filepath.Join(root, "oracle", "corpus", "validation")
	cases, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("listing cases: %v", err)
	}
	if len(cases) == 0 {
		t.Fatalf("no validation cases in %s", dir)
	}
	sort.Strings(cases)

	for _, path := range cases {
		t.Run(baseName(path), func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading case: %v", err)
			}
			var testCase validationCase
			if err := json.Unmarshal(data, &testCase); err != nil {
				t.Fatalf("decoding case: %v", err)
			}

			got := validate.Validate(toMessage(testCase.Expected), toMessage(testCase.Real))
			want := runReferenceValidate(t, root, path)

			compareVerdicts(t, want, got)
		})
	}
}

// compareVerdicts checks the answer rather than the prose.
func compareVerdicts(t *testing.T, want any, got validate.Result) {
	t.Helper()

	reference, ok := want.(map[string]any)
	if !ok {
		t.Fatalf("unexpected reference shape %T", want)
	}

	if valid, _ := reference["valid"].(bool); valid != got.Valid {
		t.Errorf("valid = %v, Gavel says %v (errors: %v)", got.Valid, valid, allErrors(got))
	}

	fields, _ := reference["fields"].(map[string]any)
	for name, raw := range fields {
		field, _ := raw.(map[string]any)
		referenceValid, _ := field["valid"].(bool)

		ours, present := got.Fields[name]
		if !present {
			// A field Gavel judged and vertrag did not is a gap in coverage,
			// whichever way the verdict went.
			t.Errorf("Gavel reports on %q and vertrag does not", name)
			continue
		}
		if ours.Valid != referenceValid {
			t.Errorf("%s: valid = %v, Gavel says %v (errors: %v)",
				name, ours.Valid, referenceValid, ours.Errors)
		}
	}

	for name := range got.Fields {
		if _, present := fields[name]; !present {
			t.Errorf("vertrag reports on %q and Gavel does not", name)
		}
	}
}

func allErrors(result validate.Result) []string {
	var out []string
	for _, field := range result.Fields {
		out = append(out, field.Errors...)
	}
	return out
}

func toMessage(m messageJSON) validate.Message {
	return validate.Message{
		StatusCode: m.StatusCode,
		Headers:    m.Headers,
		Body:       m.Body,
		BodySchema: m.BodySchema,
	}
}

func roundTripValue(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encoding result: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	return decoded
}

func runReferenceValidate(t *testing.T, root, path string) any {
	t.Helper()
	return runReferenceScript(t, root, "validate.js", path)
}
