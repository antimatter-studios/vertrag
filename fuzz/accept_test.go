package fuzz

import (
	"context"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
)

// TestAnAcceptedStatusIsNotAFinding covers the case the feature exists for: a
// body that satisfies every documented constraint and is refused for a business
// reason the description does not carry.
func TestAnAcceptedStatusIsNotAFinding(t *testing.T) {
	send := func(ctx context.Context, value any) (validate.Message, error) {
		return validate.Message{StatusCode: "422"}, nil
	}
	schema := generate.Schema{"type": "object", "properties": map[string]any{
		"quantity": map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
	}}

	finding, found := ProbeBody(context.Background(), "application/json", schema,
		generate.Valid, send, Options{Cases: 5, Seed: 1, Accept: Accept{422}})
	if found {
		t.Errorf("an accepted 422 was reported anyway: %s", finding.Message)
	}
}

// TestAnUnacceptedStatusIsStillAFinding is the guard that keeps the test above
// from being vacuous — the same run without the acceptance must report.
func TestAnUnacceptedStatusIsStillAFinding(t *testing.T) {
	send := func(ctx context.Context, value any) (validate.Message, error) {
		return validate.Message{StatusCode: "422"}, nil
	}
	schema := generate.Schema{"type": "object", "properties": map[string]any{
		"quantity": map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
	}}

	if _, found := ProbeBody(context.Background(), "application/json", schema,
		generate.Valid, send, Options{Cases: 5, Seed: 1}); !found {
		t.Error("a 422 to a valid body is a finding unless it was accepted")
	}
}

// TestEveryAcceptedAnswerIsCounted is the condition on which accepting is worth
// offering at all. An acceptance list that hides findings silently is a machine
// for hiding bugs; one that reports how much it hid is a decision somebody can
// audit.
func TestEveryAcceptedAnswerIsCounted(t *testing.T) {
	var suppression Suppression
	send := func(ctx context.Context, value any) (validate.Message, error) {
		return validate.Message{StatusCode: "409"}, nil
	}
	schema := generate.Schema{"type": "object", "properties": map[string]any{
		"quantity": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000},
	}}

	ProbeBody(context.Background(), "application/json", schema, generate.Valid, send,
		Options{Cases: 12, Seed: 1, Accept: Accept{409}, Suppression: &suppression})

	if suppression.Total == 0 {
		t.Fatal("answers were excused and none were counted")
	}
	if suppression.ByStatus[409] != suppression.Total {
		t.Errorf("ByStatus = %v, does not account for the total %d", suppression.ByStatus, suppression.Total)
	}
	if got := suppression.Describe(); !strings.Contains(got, "409") {
		t.Errorf("Describe() = %q, want it to name the status", got)
	}
}

// TestAServerErrorCannotBeAccepted pins the limit. A 5xx means the server broke
// on the request rather than refusing it, which is the finding a probing phase
// exists to produce — one config line must not be able to turn that off while
// leaving the tool looking like it is on.
func TestAServerErrorCannotBeAccepted(t *testing.T) {
	err := CheckAccept(Accept{422, 500})
	if err == nil {
		t.Fatal("accepting a 500 should be refused")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("the error does not name the status: %v", err)
	}

	// And the refusal is at the boundary, not merely at 500.
	if CheckAccept(Accept{503}) == nil {
		t.Error("503 should be refused too")
	}
	if CheckAccept(Accept{499}) != nil {
		t.Error("499 is a rejection, not a server error")
	}
}

// TestASuccessCannotBeAccepted is the other end of the range, and the
// reasoning is the mirror image: a status under 400 means the request was
// answered, so for a body the schema forbids it IS the finding.
func TestASuccessCannotBeAccepted(t *testing.T) {
	err := CheckAccept(Accept{200})
	if err == nil {
		t.Fatal("accepting a 200 should be refused")
	}
	if !strings.Contains(err.Error(), "200") {
		t.Errorf("the error does not name the status: %v", err)
	}
}

// TestAcceptDoesNotExcuseAServerError is the runtime counterpart of the config
// check: even if a 500 reached the accept list somehow, the probe must report
// it. Two independent guards, because this is the one finding that must never
// be lost.
func TestAcceptDoesNotExcuseAServerError(t *testing.T) {
	send := func(ctx context.Context, value any) (validate.Message, error) {
		return validate.Message{StatusCode: "500"}, nil
	}
	schema := generate.Schema{"type": "object", "properties": map[string]any{
		"quantity": map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
	}}

	finding, found := ProbeBody(context.Background(), "application/json", schema,
		generate.Valid, send, Options{Cases: 5, Seed: 1, Accept: Accept{500}})
	if !found {
		t.Fatal("a 500 was excused by the accept list; it must never be")
	}
	if !strings.Contains(finding.Message, "failed rather than rejected") {
		t.Errorf("the finding does not name the server failure: %s", finding.Message)
	}
}
