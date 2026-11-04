package fuzz

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
)

var accountSchema = generate.Schema{
	"type":     "object",
	"required": []any{"password"},
	"properties": map[string]any{
		"password": map[string]any{"type": "string", "minLength": 12, "maxLength": 20},
	},
}

func accountOptions(operation string) Options {
	return Options{
		Cases:     20,
		Seed:      1,
		Operation: operation,
		ValidPins: ValidPins{"createAccount": Pins{"password": "Tr0ub4dor-long"}},
		Engaged:   map[string]int{},
	}
}

// passwordsSent runs the coverage phase of one operation and returns the
// password of every request, by the mode it was sent in.
func passwordsSent(t *testing.T, options Options) map[generate.Mode][]any {
	t.Helper()
	sent := map[generate.Mode][]any{}
	send := func(ctx context.Context, value any) (validate.Message, error) {
		var body map[string]any
		if err := json.Unmarshal([]byte(value.(string)), &body); err != nil {
			t.Fatalf("the body was not JSON: %v", err)
		}
		mode, _ := ModeOf(ctx)
		sent[mode] = append(sent[mode], body["password"])
		return validate.Message{StatusCode: "400"}, nil
	}
	Cover(context.Background(), Subject{In: InBody}, "application/json", accountSchema, send, options)
	return sent
}

func TestAValidPinReplacesTheValueOnItsOperationsValidProbes(t *testing.T) {
	options := accountOptions("createAccount")
	sent := passwordsSent(t, options)
	if len(sent[generate.Valid]) == 0 {
		t.Fatal("no valid probe was sent, so the replaced value was not sent either")
	}
	for _, password := range sent[generate.Valid] {
		if password != "Tr0ub4dor-long" {
			t.Errorf("a valid probe carried %v", password)
		}
	}
	if options.Engaged["createAccount.password"] == 0 {
		t.Error("the run would report that nothing was held")
	}
}

func TestAValidPinLeavesInvalidProbesAlone(t *testing.T) {
	sent := passwordsSent(t, accountOptions("createAccount"))
	tooShort := false
	for _, password := range sent[generate.Invalid] {
		if password == "Tr0ub4dor-long" {
			t.Error("an invalid probe carried the replaced value")
		}
		if text, ok := password.(string); ok && len(text) < 12 {
			tooShort = true
		}
	}
	if !tooShort {
		t.Errorf("the too-short password was not sent: %v", sent[generate.Invalid])
	}
}

func TestAValidPinLeavesOtherOperationsAlone(t *testing.T) {
	options := accountOptions("login")
	sent := passwordsSent(t, options)
	if len(sent[generate.Valid]) == 0 {
		t.Fatal("no valid probe was sent")
	}
	for _, password := range sent[generate.Valid] {
		if password == "Tr0ub4dor-long" {
			t.Error("another operation with the same field received the value")
		}
	}
	if len(options.Engaged) != 0 {
		t.Errorf("counted %v on another operation", options.Engaged)
	}
}

func TestAPinWinsOverAValidPin(t *testing.T) {
	options := accountOptions("createAccount")
	options.Pin = Pins{"password": "safety-interlock"}
	for _, password := range passwordsSent(t, options)[generate.Valid] {
		if password != "safety-interlock" {
			t.Errorf("a valid probe carried %v", password)
		}
	}
}

func TestAValidPinCarriesIntoTheWholeRequest(t *testing.T) {
	options := accountOptions("createAccount")
	subject := Subject{In: InBody}
	parts := []Part{{Subject: subject, Media: "application/json", Schema: accountSchema}}
	var sent []string
	send := func(ctx context.Context, values map[string]any) (validate.Message, error) {
		sent = append(sent, values[subject.label()].(string))
		return validate.Message{StatusCode: "200"}, nil
	}
	ProbeWhole(context.Background(), parts, generate.Valid, send, options)
	if len(sent) == 0 {
		t.Fatal("nothing was sent")
	}
	for _, body := range sent {
		if !strings.Contains(body, "Tr0ub4dor-long") {
			t.Errorf("a whole request carried %s", body)
		}
	}
}

func TestAValidPinThatCanHoldNothingStopsTheRun(t *testing.T) {
	targets := []PinTarget{{Operation: "createAccount", Bodies: []generate.Schema{accountSchema}}}
	for name, testCase := range map[string]struct {
		pins ValidPins
		want string
	}{
		"operation": {ValidPins{"createAcount": {"password": "Tr0ub4dor-long"}}, "createAcount: no transaction"},
		"field":     {ValidPins{"createAccount": {"passwd": "Tr0ub4dor-long"}}, "createAccount.passwd: the operation declares"},
		"value":     {ValidPins{"createAccount": {"password": "short"}}, "createAccount.password: the value"},
	} {
		t.Run(name, func(t *testing.T) {
			err := CheckValidPins(testCase.pins, targets)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("got %v, want it to mention %q", err, testCase.want)
			}
			if err != nil && strings.Contains(err.Error(), "Tr0ub4dor") {
				t.Errorf("the error repeated the value: %v", err)
			}
		})
	}
	if err := CheckValidPins(ValidPins{"createAccount": {"password": "Tr0ub4dor-long"}}, targets); err != nil {
		t.Errorf("a good setting was refused: %v", err)
	}
}
