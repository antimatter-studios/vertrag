package fuzz

import (
	"context"
	"testing"

	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
)

// TestAnUngeneratedContextClaimsNoMode.
//
// generate.Valid is the zero value of generate.Mode, so a reader taking the
// absent case as a value would read every request the DESCRIPTION composed as
// one drawn valid. The two are the opposite ends of how much a status observed
// on such a request is worth, so the boolean is the point of the pair.
func TestAnUngeneratedContextClaimsNoMode(t *testing.T) {
	if _, generated := ModeOf(context.Background()); generated {
		t.Error("a context nothing generated on claims to carry a mode")
	}
	if mode, generated := ModeOf(WithMode(context.Background(), generate.Valid)); !generated || mode != generate.Valid {
		t.Errorf("a valid draw reads back as (%v, %v)", mode, generated)
	}
}

// TestEveryGeneratedRequestTellsTheSenderHowItWasDrawn.
//
// Whoever is watching the wire has to be able to tell a request whose value the
// schema permits from one it forbids, because a server refusing the second is
// probably right and a server refusing the first is not. The probe is the only
// thing that knows which it drew, and this is where it says so.
func TestEveryGeneratedRequestTellsTheSenderHowItWasDrawn(t *testing.T) {
	schema := generate.Schema{"type": "integer", "minimum": 1, "maximum": 100}

	for _, mode := range []generate.Mode{generate.Valid, generate.Invalid} {
		drawn := 0
		send := func(ctx context.Context, value any) (validate.Message, error) {
			drawn++
			got, generated := ModeOf(ctx)
			if !generated {
				t.Error("a generated request reached the sender claiming the description composed it")
			}
			if got != mode {
				t.Errorf("the sender was told mode %v for a value drawn %v", got, mode)
			}
			return validate.Message{StatusCode: "400"}, nil
		}

		subject := Subject{In: InQuery, Name: "limit"}
		ProbeParameter(context.Background(), subject, schema, mode, send, Options{Cases: 5, Seed: 3})
		if drawn == 0 {
			t.Errorf("mode %v sent nothing, so the claim was never tested", mode)
		}
	}
}

// TestABoundarySweepTellsTheSenderWhichBoundaryItDrew.
//
// Coverage is the phase where this is easiest to get wrong: one call sweeps a
// schema's whole set of boundaries, valid and invalid together, so the mode
// belongs to the probe rather than to the call. A sweep that labelled every
// send with the phase's own mode would file the boundary that sits exactly ON
// the maximum — a value the description publishes as acceptable — beside the
// one past it.
func TestABoundarySweepTellsTheSenderWhichBoundaryItDrew(t *testing.T) {
	schema := generate.Schema{"type": "integer", "minimum": 1, "maximum": 100}

	seen := map[generate.Mode]int{}
	send := func(ctx context.Context, value any) (validate.Message, error) {
		mode, generated := ModeOf(ctx)
		if !generated {
			t.Error("a boundary probe reached the sender claiming the description composed it")
		}
		seen[mode]++
		return validate.Message{StatusCode: "200"}, nil
	}

	subject := Subject{In: InQuery, Name: "limit"}
	Cover(context.Background(), subject, "", schema, send, Options{})

	if seen[generate.Valid] == 0 || seen[generate.Invalid] == 0 {
		t.Errorf("a sweep of both kinds of boundary reported %v", seen)
	}
}
