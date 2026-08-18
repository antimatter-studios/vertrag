package fuzz

import (
	"context"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
)

// TestRepeatedDrawsCostOneRequest pins deduplication: an enum of three values
// drawn twenty times can only produce three distinct probes, and a server
// must see three requests, not twenty. Every case the user asked for is a
// distinct question or it is not asked.
func TestRepeatedDrawsCostOneRequest(t *testing.T) {
	seen := map[string]int{}
	send := func(ctx context.Context, value any) (validate.Message, error) {
		seen[value.(string)]++
		return validate.Message{StatusCode: "200"}, nil
	}

	schema := generate.Schema{"enum": []any{"a", "b", "c"}}
	_, found := ProbeParameter(context.Background(), Subject{In: InQuery, Name: "x"},
		schema, generate.Valid, send, Options{Cases: 20, Seed: 3})
	if found {
		t.Fatal("a 200 for a valid enum value is not a finding")
	}
	if len(seen) == 0 || len(seen) > 3 {
		t.Fatalf("distinct values sent = %d, want 1..3", len(seen))
	}
	for value, count := range seen {
		if count != 1 {
			t.Errorf("value %q was sent %d times; a repeat draw should cost no request", value, count)
		}
	}
}

// TestADeadlineStopsDrawing: once the budget is spent a probe sends nothing
// more and reports no finding — the caller says what was not reached.
func TestADeadlineStopsDrawing(t *testing.T) {
	requests := 0
	send := func(ctx context.Context, value any) (validate.Message, error) {
		requests++
		return validate.Message{StatusCode: "200"}, nil
	}
	schema := generate.Schema{"type": "integer", "minimum": 1, "maximum": 1000000}

	past := Options{Cases: 50, Seed: 3, Deadline: time.Now().Add(-time.Second)}
	if _, found := ProbeParameter(context.Background(), Subject{In: InQuery, Name: "n"},
		schema, generate.Valid, send, past); found {
		t.Fatal("an out-of-time probe reported a finding")
	}
	if requests != 0 {
		t.Errorf("an out-of-time probe sent %d requests, want 0", requests)
	}
}
