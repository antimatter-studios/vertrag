package fuzz

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
)

func TestAliasSourceAddressIsNotMisclassifiedAsValid(t *testing.T) {
	schema := json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","required":["source_address"],"properties":{"source_address":{"type":"string","format":"idn-email"}}}`)
	valid, decided := intendedValidity(schema, []string{`{"source_address":"a"}`})
	if !decided || valid {
		t.Fatalf("a refused invalid address must not be blamed on the server: valid=%v, decided=%v", valid, decided)
	}
}

func TestIDNFuzzingFindsNoDisagreementWithACorrectServer(t *testing.T) {
	for _, format := range []string{"idn-email", "idn-hostname"} {
		for _, dialect := range []string{"http://json-schema.org/draft-07/schema#", "https://json-schema.org/draft/2020-12/schema"} {
			t.Run(format+"/"+dialect, func(t *testing.T) {
				schema := generate.Schema{"$schema": dialect, "type": "object", "required": []any{"value"}, "properties": map[string]any{"value": map[string]any{"type": "string", "format": format}}}
				raw, _ := json.Marshal(schema)
				var sent int
				send := func(ctx context.Context, value any) (validate.Message, error) {
					sent++
					status := "201"
					if !validate.AgainstSchema(raw, value.(string)).Valid {
						status = "400"
					}
					return validate.Message{StatusCode: status}, nil
				}
				for _, mode := range []generate.Mode{generate.Valid, generate.Invalid} {
					sent = 0
					finding, found := ProbeBody(context.Background(), "application/json", schema, mode, send, Options{Cases: 50, Seed: 2017})
					if found || sent == 0 {
						t.Errorf("mode %d: sent %d, unexpected finding: %+v", mode, sent, finding)
					}
				}
				for _, outcome := range Cover(context.Background(), Subject{In: InBody}, "application/json", schema, send, Options{}) {
					if !outcome.Sent || outcome.Finding != nil {
						t.Errorf("unexpected boundary outcome: %+v", outcome)
					}
				}
			})
		}
	}
}
