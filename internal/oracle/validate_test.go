package oracle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/antimatter-studios/vertrag/internal/validate"
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
// The error text is compared, not just the verdict. A failing run prints these
// messages, so a user reading vertrag's output should see what Dredd would have
// shown them; matching only on valid/invalid would let the wording drift.
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

			got := roundTripValue(t, validate.Validate(
				toMessage(testCase.Expected), toMessage(testCase.Real)))
			want := runReferenceValidate(t, root, path)

			for _, diff := range diffValues("", want, got) {
				t.Errorf("%s", diff)
			}
		})
	}
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
