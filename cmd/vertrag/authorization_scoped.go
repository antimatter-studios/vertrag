package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/runner"
)

func applyScopedRefusals(plan *authorizationPlan, refusals []config.AuthorizationRefusal) error {
	for _, refusal := range refusals {
		matched := false
		for index := range plan.Cells {
			cell := &plan.Cells[index]
			if cell.Transaction.OperationID != refusal.Operation || cell.Principal != refusal.Principal {
				continue
			}
			if cell.Refusal != nil {
				return fmt.Errorf("authorization.refusals: duplicate expectation for %s as %s", refusal.Operation, refusal.Principal)
			}
			if refusal.Status < 400 || refusal.Status > 499 || len(refusal.Body) == 0 {
				return fmt.Errorf("authorization.refusals: %s as %s requires an exact 4xx status and nonempty body", refusal.Operation, refusal.Principal)
			}
			encoded, err := json.Marshal(refusal.Body)
			if err != nil {
				return fmt.Errorf("authorization.refusals: invalid JSON body: %w", err)
			}
			cell.Refusal = &refusal
			cell.Expect = expectForbidden
			cell.Status = strconv.Itoa(refusal.Status)
			cell.Why = fmt.Sprintf("explicit operation/principal refusal with exact JSON values %s", encoded)
			matched = true
		}
		if !matched {
			return fmt.Errorf("authorization.refusals: %s as %s matches no selected authorization cell", refusal.Operation, refusal.Principal)
		}
	}
	return nil
}

func judgeScopedRefusal(cell authorizationCell, sent runner.Result) authorizationOutcome {
	outcome := authorizationOutcome{Cell: cell, Result: sent}
	outcome.Result.Status = runner.StatusFail
	status := sent.Actual.StatusCode
	if status != cell.Status {
		outcome.Bypass = strings.HasPrefix(status, "2")
		outcome.Result.Errors = append(outcome.Result.Errors, fmt.Sprintf("scoped authorization refusal: expected exactly %s, got %s", cell.Status, status))
		return outcome
	}
	var actual, expected any
	encoded, err := json.Marshal(cell.Refusal.Body)
	if err != nil {
		outcome.Result.Errors = append(outcome.Result.Errors, "scoped authorization refusal: invalid expected JSON")
		return outcome
	}
	if json.Unmarshal(encoded, &expected) != nil || json.Unmarshal([]byte(sent.Actual.Body), &actual) != nil || !matchesRefusalBody(expected, actual) {
		outcome.Result.Errors = append(outcome.Result.Errors, "scoped authorization refusal: response does not contain the exact declared JSON values")
		return outcome
	}
	if len(sent.Errors) == 0 {
		outcome.Result.Status = runner.StatusPass
	}
	return outcome
}

func matchesRefusalBody(expected, actual any) bool {
	object, ok := expected.(map[string]any)
	if !ok {
		return reflect.DeepEqual(expected, actual)
	}
	got, ok := actual.(map[string]any)
	if !ok {
		return false
	}
	for key, value := range object {
		found, exists := got[key]
		if !exists || !matchesRefusalBody(value, found) {
			return false
		}
	}
	return true
}
