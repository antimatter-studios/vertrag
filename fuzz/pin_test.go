package fuzz

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
)

// orderSchema is the shape the pin exists for: a flag that decides whether the
// request does something irreversible, which the schema is perfectly happy to
// see either way.
var orderSchema = generate.Schema{
	"type":     "object",
	"required": []any{"symbol", "quantity", "dry_run"},
	"properties": map[string]any{
		"symbol":   map[string]any{"type": "string", "minLength": 1},
		"quantity": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000},
		"dry_run":  map[string]any{"type": "boolean"},
	},
}

// TestEveryGeneratedBodyCarriesThePinnedValue is the test the feature exists
// for. Without the pin, generation draws `dry_run: false` about half the time,
// and each of those is a real order.
func TestEveryGeneratedBodyCarriesThePinnedValue(t *testing.T) {
	var live, sent int
	send := func(ctx context.Context, value any) (validate.Message, error) {
		sent++
		var body map[string]any
		if err := json.Unmarshal([]byte(value.(string)), &body); err != nil {
			t.Fatalf("the body was not JSON: %v", err)
		}
		if body["dry_run"] != true {
			live++
		}
		return validate.Message{StatusCode: "200"}, nil
	}

	_, found := ProbeBody(context.Background(), "application/json", orderSchema,
		generate.Valid, send, Options{
			Cases: 40,
			Seed:  1,
			Pin:   Pins{"dry_run": true},
		})

	if sent == 0 {
		t.Fatal("nothing was sent, so the pin proved nothing")
	}
	if live != 0 {
		t.Errorf("%d of %d generated bodies did not carry the pin", live, sent)
	}
	if found {
		t.Error("a 200 to a valid body is not a finding")
	}
}

// TestWithoutAPinTheDangerousValueIsDrawn is the other half, and the reason the
// test above is not vacuous: if generation never produced `dry_run: false`
// anyway, the pin would be proving nothing.
func TestWithoutAPinTheDangerousValueIsDrawn(t *testing.T) {
	var live int
	send := func(ctx context.Context, value any) (validate.Message, error) {
		var body map[string]any
		_ = json.Unmarshal([]byte(value.(string)), &body)
		if body["dry_run"] != true {
			live++
		}
		return validate.Message{StatusCode: "200"}, nil
	}

	ProbeBody(context.Background(), "application/json", orderSchema,
		generate.Valid, send, Options{Cases: 40, Seed: 1})

	if live == 0 {
		t.Fatal("generation never drew the unpinned value, so the pin test proves nothing")
	}
	t.Logf("unpinned, %d generated bodies would have been live orders", live)
}

// TestAPinReportsWhichOperationsItHeld pins the reporting half. "Configured"
// and "engaged" are different facts and only the second one is safety.
func TestAPinReportsWhichOperationsItHeld(t *testing.T) {
	engaged := map[string]int{}
	send := func(ctx context.Context, value any) (validate.Message, error) {
		return validate.Message{StatusCode: "200"}, nil
	}

	ProbeBody(context.Background(), "application/json", orderSchema,
		generate.Valid, send, Options{
			Cases:   10,
			Seed:    1,
			Pin:     Pins{"dry_run": true},
			Engaged: engaged,
		})

	if engaged["dry_run"] == 0 {
		t.Error("the pin held a field but reported engaging nowhere")
	}
}

// TestAPinNamingNothingRefusesToRun is the failure this is built around: a
// typo, or a field renamed in the description since the pin was written, must
// not leave a configuration that reads like a safety control and holds nothing.
func TestAPinNamingNothingRefusesToRun(t *testing.T) {
	err := CheckPins(Pins{"dryrun": true}, []generate.Schema{orderSchema})
	if err == nil {
		t.Fatal("a pin naming no field anywhere should refuse to run")
	}
	// The message has to make the typo findable.
	for _, want := range []string{"dryrun", "fuzz.pin", "safety"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// TestAPinMatchingOneSchemaOfManyIsAccepted: `dry_run` belongs to the ordering
// endpoints and nothing else, so matching everywhere cannot be the rule.
func TestAPinMatchingOneSchemaOfManyIsAccepted(t *testing.T) {
	health := generate.Schema{"type": "object", "properties": map[string]any{
		"note": map[string]any{"type": "string"},
	}}

	if err := CheckPins(Pins{"dry_run": true}, []generate.Schema{health, orderSchema}); err != nil {
		t.Errorf("a pin matching one body of several should be accepted: %v", err)
	}
	// And on the operation that does not declare it, nothing is inserted:
	// inventing a field the schema never mentioned would make a valid body
	// invalid and report a finding against the server for vertrag's own edit.
	value, engaged := Pins{"dry_run": true}.Apply(health, map[string]any{"note": "x"})
	if _, added := value.(map[string]any)["dry_run"]; added {
		t.Error("the pin invented a field the schema does not declare")
	}
	if len(engaged) != 0 {
		t.Errorf("the pin reported engaging where it did not: %v", engaged)
	}
}

// TestAPinOnANonObjectBodyIsLeftAlone guards the apply path against a schema
// that produces a scalar or a list, where there is no field to hold.
func TestAPinOnANonObjectBodyIsLeftAlone(t *testing.T) {
	list := generate.Schema{"type": "array", "items": map[string]any{"type": "string"}}
	value, engaged := Pins{"dry_run": true}.Apply(list, []any{"a", "b"})
	if got, ok := value.([]any); !ok || len(got) != 2 {
		t.Errorf("the list was disturbed: %v", value)
	}
	if len(engaged) != 0 {
		t.Errorf("engaged on a list: %v", engaged)
	}
}
