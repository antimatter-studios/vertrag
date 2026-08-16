// Command clibinary exercises the fuzz package from a plain binary.
//
// It exists because the failure it guards against cannot be reproduced from a
// Go test: rapid.Check consults testing.Short, which panics unless the testing
// package has been initialised and its flags parsed — conditions `go test`
// satisfies for free and a compiled command does not. A unit test would pass
// while `vertrag fuzz` panicked on first use.
package main

import (
	"context"
	"fmt"

	"github.com/antimatter-studios/vertrag/fuzz"
	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
)

func main() {
	schema := generate.Schema{
		"type":     "object",
		"required": []any{"age"},
		"properties": map[string]any{
			"age": map[string]any{"type": "integer", "minimum": float64(18)},
		},
	}
	// A server that enforces nothing, so the invalid mode always has a finding.
	accepts := func(ctx context.Context, body any) (validate.Message, error) {
		return validate.Message{StatusCode: "201"}, nil
	}

	first, found := fuzz.Probe(context.Background(), schema, generate.Invalid,
		accepts, fuzz.Options{Cases: 50, Seed: 7})
	if !found {
		fmt.Println("FAIL: no finding against a server that enforces nothing")
		return
	}

	second, _ := fuzz.Probe(context.Background(), schema, generate.Invalid,
		accepts, fuzz.Options{Cases: 50, Seed: 7})
	if first.Value != second.Value {
		fmt.Printf("FAIL: seed 7 gave %s then %s\n", first.Value, second.Value)
		return
	}

	fmt.Println("OK", first.Value)
}
