package fuzz

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
)

// userSchema is a shape with one constraint of each kind a handler tends to
// forget: a length bound, a numeric bound, and a required property.
var userSchema = generate.Schema{
	"type":     "object",
	"required": []any{"username", "age"},
	"properties": map[string]any{
		"username": map[string]any{"type": "string", "minLength": float64(3), "maxLength": float64(12)},
		"age":      map[string]any{"type": "integer", "minimum": float64(18), "maximum": float64(120)},
	},
}

// server is a stand-in handler, so a test can pin what each kind of server bug
// looks like from the outside without needing a real one.
type server func(body map[string]any) string

func (s server) sender() Sender {
	return func(ctx context.Context, body string) (validate.Message, error) {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			// Not an object: a correct server rejects it.
			return validate.Message{StatusCode: "400"}, nil
		}
		return validate.Message{StatusCode: s(decoded)}, nil
	}
}

// correct enforces everything the schema states.
func correct(body map[string]any) string {
	name, ok := body["username"].(string)
	if !ok || len(name) < 3 || len(name) > 12 {
		return "400"
	}
	age, ok := body["age"].(float64)
	if !ok || age < 18 || age > 120 {
		return "400"
	}
	return "201"
}

func TestASoundServerProducesNoFindings(t *testing.T) {
	for _, mode := range []generate.Mode{generate.Valid, generate.Invalid} {
		finding, found := Probe(context.Background(), userSchema, mode,
			server(correct).sender(), Options{Cases: 200})
		if found {
			t.Errorf("mode %v: unexpected finding %q for body %s",
				mode, finding.Message, finding.Body)
		}
	}
}

// TestValidationBypassIsFound is the finding this package exists for: a server
// that accepts input its own description forbids.
func TestValidationBypassIsFound(t *testing.T) {
	// Checks the username but forgets the age bounds entirely — the shape of
	// mistake where one field's validation is written and the next is not.
	forgetsAge := func(body map[string]any) string {
		name, ok := body["username"].(string)
		if !ok || len(name) < 3 || len(name) > 12 {
			return "400"
		}
		return "201"
	}

	finding, found := Probe(context.Background(), userSchema, generate.Invalid,
		server(forgetsAge).sender(), Options{Cases: 200})
	if !found {
		t.Fatal("a server ignoring its age constraint should be found")
	}
	if !strings.Contains(finding.Message, "not enforced") {
		t.Errorf("message = %q, want it to name the unenforced constraint", finding.Message)
	}
	if finding.Status != "201" {
		t.Errorf("status = %q, want 201", finding.Status)
	}
}

// TestOverStrictServerIsFound covers the opposite disagreement: input the
// description permits and the server refuses.
func TestOverStrictServerIsFound(t *testing.T) {
	// Enforces a stricter minimum than the schema states, which is what
	// happens when a constraint is tightened in code and the description is
	// not updated.
	stricter := func(body map[string]any) string {
		age, ok := body["age"].(float64)
		if !ok || age < 21 {
			return "400"
		}
		return "201"
	}

	finding, found := Probe(context.Background(), userSchema, generate.Valid,
		server(stricter).sender(), Options{Cases: 200})
	if !found {
		t.Fatal("a server stricter than its description should be found")
	}
	if !strings.Contains(finding.Message, "disagrees with its description") {
		t.Errorf("message = %q, want it to name the disagreement", finding.Message)
	}
}

// TestServerErrorIsFoundInEitherMode pins that a crash is reported whichever
// body caused it. A server may refuse input; failing on it is never documented.
func TestServerErrorIsFoundInEitherMode(t *testing.T) {
	crashes := func(map[string]any) string { return "500" }

	for _, mode := range []generate.Mode{generate.Valid, generate.Invalid} {
		finding, found := Probe(context.Background(), userSchema, mode,
			server(crashes).sender(), Options{Cases: 50})
		if !found {
			t.Fatalf("mode %v: a 500 should be reported", mode)
		}
		if !strings.Contains(finding.Message, "failed rather than rejected") {
			t.Errorf("mode %v: message = %q", mode, finding.Message)
		}
	}
}

// TestFindingIsShrunk pins the property that makes generated failures usable.
//
// Without shrinking, the reported body is whatever random value first tripped
// the bug — long, arbitrary, and hard to relate to the cause. rapid reduces it
// to a minimal failing case.
func TestFindingIsShrunk(t *testing.T) {
	// Rejects any username of 3 or more characters, so the minimal valid body
	// has exactly the shortest permitted name.
	rejectsEverything := func(map[string]any) string { return "400" }

	finding, found := Probe(context.Background(), userSchema, generate.Valid,
		server(rejectsEverything).sender(), Options{Cases: 200})
	if !found {
		t.Fatal("expected a finding")
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(finding.Body), &body); err != nil {
		t.Fatalf("finding body does not parse: %v", err)
	}

	// Shrunk to the boundary: the shortest permitted username and the lowest
	// permitted age. Anything larger means shrinking did not run.
	if name, _ := body["username"].(string); len(name) != 3 {
		t.Errorf("username = %q (len %d), want the 3-character minimum", name, len(name))
	}
	if age, _ := body["age"].(float64); age != 18 {
		t.Errorf("age = %v, want the minimum of 18", age)
	}
}

func TestTransportFailureIsReported(t *testing.T) {
	failing := func(ctx context.Context, body string) (validate.Message, error) {
		return validate.Message{}, context.DeadlineExceeded
	}
	if _, found := Probe(context.Background(), userSchema, generate.Valid, failing, Options{Cases: 5}); !found {
		t.Error("a transport failure should be reported rather than passing silently")
	}
}
