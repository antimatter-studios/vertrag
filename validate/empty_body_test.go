package validate

import (
	"encoding/json"
	"testing"
)

func TestExplicitEmptyBodiesAndSchemasRemainDistinct(t *testing.T) {
	for _, test := range []struct {
		name     string
		expected Message
		body     string
		valid    bool
		checked  bool
	}{
		{"absent", Message{}, "unexpected", true, false},
		{"empty exact", Message{BodyPresent: true}, "", true, true},
		{"empty wrong", Message{BodyPresent: true}, "unexpected", false, true},
		{"nonempty exact", Message{Body: "expected"}, "expected", true, true},
		{"nonempty wrong", Message{Body: "expected"}, "unexpected", false, true},
		{"JSON schema alone passes", Message{BodySchema: json.RawMessage(`{"type":"integer"}`)}, "1", true, true},
		{"JSON schema alone rejects", Message{BodySchema: json.RawMessage(`{"type":"integer"}`)}, `"wrong"`, false, true},
		{"schema wins over empty example", Message{BodyPresent: true, BodySchema: json.RawMessage(`{"type":"integer"}`)}, "1", true, true},
		{"schema still rejects wrong type", Message{BodyPresent: true, BodySchema: json.RawMessage(`{"type":"integer"}`)}, `"wrong"`, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := Validate(test.expected, Message{Body: test.body})
			if result.Valid != test.valid {
				t.Fatalf("valid %v, want %v: %v", result.Valid, test.valid, result.Fields)
			}
			if _, checked := result.Fields["body"]; checked != test.checked {
				t.Fatalf("body checked %v, want %v", checked, test.checked)
			}
		})
	}
}
