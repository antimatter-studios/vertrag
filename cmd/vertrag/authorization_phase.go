package main

import (
	"context"
	"fmt"
	"slices"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
)

// The authorization phase, as `run` drives it: planned from the transactions
// this run selected, with the run's own skips, then — unless it is a dry run —
// every principal logged in once and every cell sent and judged.

// authorizationSkip is the run's decision not to send a transaction, read the
// way the probing phases read it: a configured `skip`. Selection is applied
// before this — the plan only ever sees the selected transactions — and the
// pin is applied to their bodies before the phase runs, so a cell sends what
// the examples phase sent and nothing a skip ruled out.
func authorizationSkip(skips map[string]string) func(compile.Transaction) string {
	return func(transaction compile.Transaction) string {
		reason, skipped := skips[transaction.Name]
		if !skipped {
			return ""
		}
		return skipReasonText(reason)
	}
}

// planAuthorizationPhase reads the configured principals and plans the matrix.
func planAuthorizationPhase(settings config.Config, transactions []compile.Transaction,
	skips map[string]string) (authorizationPlan, []authorizationPrincipal, error) {
	principals, err := authorizationPrincipals(settings.Principals, transactions)
	if err != nil {
		return authorizationPlan{}, nil, err
	}
	plan := planAuthorization(transactions, principals, authorizationSkip(skips))
	planSetups(&plan, transactions, settings.Authorization.Owner)
	plan.Refuse = slices.Clone(settings.Authorization.Refuse)
	return plan, principals, nil
}

// dryRunAuthorization is the plan as a matrix with nothing sent: every cell
// unsent, with the planner's own reason where it had one.
func dryRunAuthorization(plan authorizationPlan) *reporter.Authorization {
	outcomes := make([]authorizationOutcome, 0, len(plan.Cells))
	for _, cell := range plan.Cells {
		reason := cell.Unsent
		if reason == "" {
			// What the cell would have been held to, so the plan can be read
			// from the dry run.
			reason = fmt.Sprintf("dry run; the description requires %s (%s)", cell.Status, cell.Why)
		}
		outcomes = append(outcomes, authorizationOutcome{
			Cell:   cell,
			Unsent: reason,
			Result: runner.Result{Name: cell.Operation + " as " + cell.Principal, Status: runner.StatusSkip,
				Errors: []string{"not sent: " + reason}},
		})
	}
	return authorizationMatrix(plan, outcomes)
}

// runAuthorization plans, logs every principal in once, and sends the cells.
// It returns each cell's ordinary result — the report counts them as it counts
// any — and the matrix that is the phase's own view of them.
//
// A principal that cannot log in stops the run: there is no identity to test,
// and a matrix without it would read as complete.
func runAuthorization(ctx context.Context, engine *runner.Runner, settings config.Config,
	transactions []compile.Transaction) ([]runner.Result, *reporter.Authorization, error) {
	plan, principals, err := planAuthorizationPhase(settings, transactions, engine.Skip)
	if err != nil {
		return nil, nil, err
	}
	credentials, unsupported, err := authorizationCredentials(ctx, engine.Client, engine.Endpoint,
		settings.Principals, principals)
	if err != nil {
		return nil, nil, fmt.Errorf("the authorization phase: %w", err)
	}
	for _, principal := range principals {
		if reason, ok := unsupported[principal.Name]; ok {
			fmt.Printf("authorization: %s is not tested: %s\n", principal.Name, reason)
		}
	}
	outcomes := executeAuthorization(ctx, engine, plan, credentials, unsupported)
	results := make([]runner.Result, 0, len(outcomes))
	for _, outcome := range outcomes {
		results = append(results, outcome.Result)
	}
	return results, authorizationMatrix(plan, outcomes), nil
}
