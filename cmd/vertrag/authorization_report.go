package main

import (
	"strings"

	"github.com/antimatter-studios/vertrag/reporter"
)

// authorizationMatrix is the plan and its outcomes as the reporters show
// them. It is the one place the executor's outcome becomes the report's
// cell, so the rule that nothing unsent passes is applied once: an outcome
// with an Unsent reason is unsent whatever its Result says, and has no status
// that came back.
func authorizationMatrix(plan authorizationPlan, outcomes []authorizationOutcome) *reporter.Authorization {
	matrix := &reporter.Authorization{
		Operations: append([]string{}, plan.Operations...),
		Principals: append([]string{}, plan.Principals...),
		Cells:      make([]reporter.AuthorizationCell, 0, len(outcomes)),
	}
	for _, outcome := range outcomes {
		unsent := outcome.Unsent != ""
		cell := reporter.AuthorizationCell{
			Operation: outcome.Cell.Operation,
			Principal: outcome.Cell.Principal,
			Expected:  outcome.Cell.Status,
			Verdict:   reporter.AuthorizationVerdictOf(outcome.Result.Status, unsent),
			Bypass:    outcome.Bypass,
			Replay:    outcome.Replay,
		}
		switch {
		case unsent:
			cell.Reason = "not sent: " + outcome.Unsent
		case cell.Verdict != reporter.AuthorizationPass:
			cell.Actual = outcome.Result.Actual.StatusCode
			cell.Reason = strings.Join(outcome.Result.Errors, "; ")
		default:
			cell.Actual = outcome.Result.Actual.StatusCode
		}
		matrix.Cells = append(matrix.Cells, cell)
	}
	return matrix
}
