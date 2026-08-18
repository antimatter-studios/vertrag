package fuzz

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/antimatter-studios/vertrag/generate"
)

// Coverage is the deterministic phase: instead of drawing values at random,
// it sends the boundary probes a schema implies — the maximum, one past it,
// the required property missing — the same ones every run. It shares the
// whole judging pipeline with random probing (wire form, validity check,
// verdict) and differs only in where values come from, so a coverage finding
// and a fuzz finding mean the same thing.

// CoverageFinding is one boundary probe the server mishandled.
type CoverageFinding struct {
	Probe   generate.Probe
	Subject Subject
	Value   any
	Status  string
	Message string
}

// CoverageOutcome is what one probe established.
type CoverageOutcome struct {
	Probe generate.Probe
	// Sent is false when the probe had no wire form for its subject, or
	// did not have the validity it claimed once rendered — nothing went out.
	Sent    bool
	Finding *CoverageFinding
}

// Cover sends every boundary probe of a subject and reports each outcome, in
// order. Unlike random probing it does not stop at the first finding: the
// point of a deterministic pass is a complete answer to a fixed set of
// questions, and "the server accepted maximum+1 AND minimum-1" is more useful
// than the first of those alone.
func Cover(ctx context.Context, subject Subject, mediaType string, schema generate.Schema, send Sender) []CoverageOutcome {
	rawSchema, err := json.Marshal(map[string]any(schema))
	if err != nil {
		return nil
	}
	var form wire
	if subject.In == InBody {
		f, ok := BodyForm(mediaType, schema)
		if !ok {
			return nil
		}
		form = f
	} else {
		form = parameterForm(subject, schema)
	}

	var outcomes []CoverageOutcome
	for _, probe := range generate.Boundaries(schema) {
		if ctx.Err() != nil {
			return outcomes
		}
		outcome := CoverageOutcome{Probe: probe}

		rendered, ok := form.render(probe.Value)
		if !ok {
			outcomes = append(outcomes, outcome)
			continue
		}
		valid, decided := intendedValidity(rawSchema, form.interpret(rendered))
		if !decided || valid != (probe.Mode == generate.Valid) {
			// The probe's claim did not survive the wire form — a list
			// boundary on a parameter that cannot carry lists, say. Not
			// sent, not counted, and never a finding.
			outcomes = append(outcomes, outcome)
			continue
		}
		outcome.Sent = true

		reply, err := send(ctx, rendered)
		if err != nil {
			outcome.Finding = &CoverageFinding{Probe: probe, Subject: subject, Value: rendered,
				Message: fmt.Sprintf("the request could not be completed: %v", err)}
			outcomes = append(outcomes, outcome)
			continue
		}
		if message, bad := judge(probe.Mode, subject, reply.StatusCode); bad {
			outcome.Finding = &CoverageFinding{Probe: probe, Subject: subject, Value: rendered,
				Status: reply.StatusCode, Message: message}
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}
