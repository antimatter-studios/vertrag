package runner

import (
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/validate"
)

func TestExplicitBodyIntentDoesNotOverrideBodilessHTTPResponses(t *testing.T) {
	for _, test := range []struct{ method, status string }{
		{"HEAD", "200"}, {"GET", "204"}, {"GET", "304"},
	} {
		t.Run(test.method+test.status, func(t *testing.T) {
			tx := newTransaction(compile.Transaction{
				Request:  compile.Request{Method: test.method, URI: "/"},
				Response: compile.Response{Status: test.status, BodyPresent: true},
			}, "http://example.test", nil)
			tx.Real = validate.Message{StatusCode: test.status}
			result := tx.validated(Checks{}, 0, 0)
			if result.Status != StatusPass {
				t.Fatalf("status %s: %v", result.Status, result.Errors)
			}
			if _, checked := result.Validation.Fields["body"]; checked {
				t.Fatal("a bodyless HTTP response must not be compared for body content")
			}
		})
	}
}
