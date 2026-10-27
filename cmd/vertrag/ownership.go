package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/link"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// The ownership phase: every operation that takes an id, sent by one
// tenant's principals with another tenant's ids (#82).
//
// The authorization phase checks roles. Most real break-ins are not about
// roles: they use an operation the caller is entitled to, with an id that
// belongs to somebody else (OWASP API1, broken object-level authorization).
// A description's security cannot say "only on your own objects", so the
// expectation here is vertrag's, and it is configured rather than read:
// `tenants` says whose resources are whose, and `ownership.refuse` which
// answers refuse.
//
// Every cell's resource is made for it, as an authorization cell's is: the
// chain of operations the description's links say its ids come from is sent
// as a principal of the victim tenant, and the attacker's request is filled
// from what came back. A cell for an operation that lists is the other way
// round: the attacker's list must hold none of the ids the victim's
// resources were given during the phase.

// ownershipCell is one operation sent by one principal against one other
// tenant's resources.
type ownershipCell struct {
	// Operation is the matrix's row; Column is its column, "user-b → a":
	// the principal, and the tenant whose ids it is sent.
	Operation string
	Column    string
	Principal string
	Tenant    string

	Transaction compile.Transaction
	Index       int

	// List marks an operation that takes no id and is read for the victim's
	// ids instead of being refused.
	List bool

	// Setup and Owner make the victim's resource, as for an authorization
	// cell.
	Setup []int
	Owner string

	// IDs are the victim tenant's declared ids (`tenants.<name>.ids`): the
	// values of the parameters no link fills, its own account, say (#110).
	IDs map[string]string

	// Expected is what the matrix shows the cell is held to.
	Expected string

	Unsent string
}

// ownershipPlan is the matrix: rows by operation, columns by principal and
// victim tenant, and what is needed to send them.
type ownershipPlan struct {
	Operations []string
	Columns    []string
	Cells      []ownershipCell

	// Refuse is what a refusal of another tenant's id may be.
	Refuse []string

	// Members are the tenant principals, the only ones the phase logs in.
	Members []authorizationPrincipal

	// sending holds the carriers, the sequencer and the transactions the
	// setups are sent with.
	sending authorizationPlan
}

// tenantOrder is the tenants by name, so the matrix does not depend on the
// order a map happened to be read in.
func tenantOrder(tenants map[string][]string) []string {
	return slices.Sorted(maps.Keys(tenants))
}

// planOwnership works out the matrix. It sends nothing.
func planOwnership(settings config.Config, transactions []compile.Transaction,
	skips map[string]string) (ownershipPlan, error) {
	principals, err := authorizationPrincipals(settings.Principals, transactions)
	if err != nil {
		return ownershipPlan{}, err
	}
	refuse := settings.Ownership.Refuse
	if len(refuse) == 0 {
		refuse = config.DefaultOwnershipRefuse
	}

	// The authorization plan says who the description lets call what, which
	// is the question asked before this one: a principal its roles refuse is
	// that phase's finding, not this one's.
	roles := planAuthorization(transactions, principals, authorizationSkip(skips))
	byCell := map[[2]string]authorizationCell{}
	for _, cell := range roles.Cells {
		byCell[[2]string{cell.Operation, cell.Principal}] = cell
	}
	index := make(map[string]int, len(transactions))
	for i, transaction := range transactions {
		index[transaction.Name] = i
	}
	sequencer := link.NewSequencer(transactions)

	plan := ownershipPlan{Refuse: slices.Clone(refuse), sending: authorizationPlan{
		Carriers: roles.Carriers, Sequencer: sequencer, Transactions: transactions}}

	type column struct{ principal, tenant string }
	var columns []column
	tenants := tenantOrder(settings.Tenants)
	members := map[string]bool{}
	for _, victim := range tenants {
		for _, attacker := range tenants {
			if attacker == victim {
				continue
			}
			for _, name := range settings.Tenants[attacker] {
				columns = append(columns, column{name, victim})
				plan.Columns = append(plan.Columns, name+" → "+victim)
				members[name] = true
			}
		}
	}
	for _, principal := range principals {
		if members[principal.Name] {
			plan.Members = append(plan.Members, principal)
		}
	}

	// allowed reports whether the plan sends this principal this operation
	// and expects it to succeed.
	allowed := func(operation, principal string) bool {
		cell, ok := byCell[[2]string{operation, principal}]
		return ok && cell.Unsent == "" && cell.Expect == expectAllowed
	}
	// owner is the first principal of a tenant allowed on every operation of
	// a chain.
	owner := func(tenant string, chain []int) string {
		for _, name := range settings.Tenants[tenant] {
			if !slices.ContainsFunc(chain, func(step int) bool {
				return !allowed(operationLabel(transactions[step]), name)
			}) {
				return name
			}
		}
		return ""
	}

	for _, operation := range roles.Operations {
		success := byCell[[2]string{operation, principals[0].Name}].Transaction
		if success.Name == "" {
			continue
		}
		takesID := slices.ContainsFunc(success.Request.Parameters, func(p compile.Parameter) bool {
			return p.In == compile.InPath
		})
		list := !takesID && strings.EqualFold(success.Request.Method, "GET")
		if !takesID && !list {
			continue
		}
		plan.Operations = append(plan.Operations, operation)
		chain := sequencer.Ancestors(index[success.Name])

		for i, col := range columns {
			cell := ownershipCell{
				Operation: operation, Column: plan.Columns[i], Principal: col.principal, Tenant: col.tenant,
				Transaction: success, Index: index[success.Name], List: list,
				Expected: strings.Join(refuse, "|"),
			}
			if list {
				cell.Expected = success.Response.Status
			}
			roleCell := byCell[[2]string{operation, col.principal}]
			switch {
			case roleCell.Unsent != "":
				cell.Unsent = roleCell.Unsent
			case roleCell.Expect != expectAllowed:
				cell.Unsent = col.principal + "'s roles already refuse it, which the authorization phase checks"
			case list:
			case len(chain) == 0 && coversPath(success, settings.TenantIDs[col.tenant]):
				// Ids the tenant already has: its own account, which no
				// operation in the description makes for it.
				cell.IDs = settings.TenantIDs[col.tenant]
			case len(chain) == 0:
				cell.Unsent = "no link makes a resource for its ids, and tenant " + col.tenant +
					" declares no ids for them, so whose they are is not known"
			case slices.ContainsFunc(chain, func(step int) bool {
				label := operationLabel(transactions[step])
				return !slices.ContainsFunc(roles.Cells, func(c authorizationCell) bool {
					return c.Operation == label && c.Unsent == ""
				})
			}):
				cell.Unsent = "an operation its ids come from is not one this run sends"
			default:
				cell.IDs = settings.TenantIDs[col.tenant]
				cell.Setup, cell.Owner = chain, owner(col.tenant, chain)
				if cell.Owner == "" {
					cell.Setup = nil
					cell.Unsent = "no principal of tenant " + col.tenant + " may make its resource"
				}
			}
			plan.Cells = append(plan.Cells, cell)
		}
	}
	return plan, nil
}

// ownershipOutcome is what became of one cell.
type ownershipOutcome struct {
	Cell   ownershipCell
	Result runner.Result
	Unsent string
	Bypass bool
	Replay string
}

// runOwnership plans, logs every tenant principal in once, and sends the
// cells: the id-taking ones first, which give each tenant the resources its
// lists are then read for.
func runOwnership(ctx context.Context, engine *runner.Runner, settings config.Config,
	transactions []compile.Transaction) ([]runner.Result, *reporter.Authorization, error) {
	plan, err := planOwnership(settings, transactions, engine.Skip)
	if err != nil {
		return nil, nil, err
	}
	credentials, unsupported, err := authorizationCredentials(ctx, engine.Client, engine.Endpoint,
		settings.Principals, plan.Members)
	if err != nil {
		return nil, nil, fmt.Errorf("the ownership phase: %w", err)
	}
	for _, member := range plan.Members {
		if reason, ok := unsupported[member.Name]; ok {
			fmt.Printf("ownership: %s is not tested: %s\n", member.Name, reason)
		}
	}
	outcomes := executeOwnership(ctx, engine, plan, credentials, unsupported)
	results := make([]runner.Result, 0, len(outcomes))
	for _, outcome := range outcomes {
		results = append(results, outcome.Result)
	}
	return results, ownershipMatrix(plan, outcomes), nil
}

func executeOwnership(ctx context.Context, engine *runner.Runner, plan ownershipPlan,
	credentials map[string]runner.Credential, unsupported map[string]string) []authorizationOutcome {
	for _, name := range plan.sending.Carriers.Headers {
		reporter.AddRedactedHeader(name)
	}
	outcomes := make([]ownershipOutcome, len(plan.Cells))
	// Each tenant's ids, for the list leak check: the declared ones, then
	// those its resources are given as the cells run.
	ids := map[string][]string{}
	for _, cell := range plan.Cells {
		for _, id := range cell.IDs {
			if !slices.Contains(ids[cell.Tenant], id) {
				ids[cell.Tenant] = append(ids[cell.Tenant], id)
			}
		}
	}
	for _, lists := range []bool{false, true} {
		for i, cell := range plan.Cells {
			if cell.List != lists {
				continue
			}
			outcomes[i] = executeOwnershipCell(ctx, engine, plan, cell, credentials, unsupported[cell.Principal], ids)
		}
	}
	out := make([]authorizationOutcome, 0, len(outcomes))
	for _, outcome := range outcomes {
		out = append(out, authorizationOutcome{Result: outcome.Result, Unsent: outcome.Unsent,
			Bypass: outcome.Bypass, Replay: outcome.Replay,
			Cell: authorizationCell{Operation: outcome.Cell.Operation, Principal: outcome.Cell.Column,
				Status: outcome.Cell.Expected, Transaction: outcome.Cell.Transaction}})
	}
	return out
}

func executeOwnershipCell(ctx context.Context, engine *runner.Runner, plan ownershipPlan, cell ownershipCell,
	credentials map[string]runner.Credential, unsupported string, ids map[string][]string) ownershipOutcome {
	name := cell.Operation + " as " + cell.Principal + " on " + cell.Tenant + "'s"
	unsent := func(reason string) ownershipOutcome {
		return ownershipOutcome{Cell: cell, Unsent: reason,
			Result: runner.Result{Name: name, Status: runner.StatusSkip, Errors: []string{"not sent: " + reason}}}
	}
	switch {
	case cell.Unsent != "":
		return unsent(cell.Unsent)
	case unsupported != "":
		return unsent(unsupported)
	case cell.List && len(ids[cell.Tenant]) == 0:
		return unsent("no resource of tenant " + cell.Tenant + "'s was made this run to look for")
	case ctx.Err() != nil:
		return unsent("the run was cancelled before this cell was sent")
	}

	var prepare func(*runner.Transaction) error
	var sequencer *link.Sequencer
	if len(cell.Setup) > 0 {
		var completed map[int]runner.Result
		var failure string
		sequencer, completed, failure = setUp(ctx, engine, plan.sending,
			authorizationCell{Setup: cell.Setup, Owner: cell.Owner}, credentials[cell.Owner])
		if failure != "" {
			return unsent(failure)
		}
		var prepared *runner.Transaction
		prepare = prepareFrom(sequencer, cell.Index, completed, &prepared)
	}
	if len(cell.IDs) > 0 {
		prepare = withDeclaredIDs(prepare, cell, func() map[string]string {
			if sequencer == nil {
				return nil
			}
			return sequencer.Filled(cell.Index)
		})
	}

	carriers := plan.sending.Carriers
	sent, err := engine.SendPreparedAs(ctx, name, cell.Transaction, credentials[cell.Principal], carriers, prepare)
	if sequencer != nil {
		for _, id := range sequencer.Filled(cell.Index) {
			if !slices.Contains(ids[cell.Tenant], id) {
				ids[cell.Tenant] = append(ids[cell.Tenant], id)
			}
		}
	}
	var unprepared errUnprepared
	switch {
	case errors.As(err, &unprepared):
		return unsent("its ids could not be filled from its resource: " + string(unprepared))
	case errors.Is(err, runner.ErrSkippedByHook):
		return unsent("a hook skipped it")
	case errors.Is(err, runner.ErrCredentialChangedByHook):
		return unsent("a hook changed the credential, so the request no longer said who sent it")
	case err != nil && ctx.Err() != nil:
		return unsent("the run was cancelled while this cell was being sent")
	case err != nil:
		sent.Name, sent.Status = name, runner.StatusError
		sent.Errors = append(sent.Errors, err.Error())
		return ownershipOutcome{Cell: cell, Result: sent}
	}

	outcome := judgeOwnership(cell, sent, plan.Refuse, ids[cell.Tenant])
	if outcome.Result.Status == runner.StatusFail {
		outcome.Replay = authorizationReplay(sent.Request, carriers)
	}
	return outcome
}

// coversPath reports whether a tenant's declared ids name every path
// parameter of a transaction, so it can be sent with them and nothing else.
func coversPath(transaction compile.Transaction, declared map[string]string) bool {
	if len(declared) == 0 {
		return false
	}
	for _, p := range transaction.Request.Parameters {
		if p.In != compile.InPath {
			continue
		}
		if !slices.ContainsFunc(slices.Collect(maps.Keys(declared)), func(key string) bool {
			return link.Match(key, p.In, p.Name)
		}) {
			return false
		}
	}
	return true
}

// withDeclaredIDs runs prepare, then puts the victim tenant's declared ids
// into every parameter the links did not fill. The link values are put back
// with them, because filling a parameter rebuilds the request from its
// template.
func withDeclaredIDs(prepare func(*runner.Transaction) error, cell ownershipCell,
	filled func() map[string]string) func(*runner.Transaction) error {
	return func(transaction *runner.Transaction) error {
		if prepare != nil {
			if err := prepare(transaction); err != nil {
				return err
			}
		}
		values := map[string]any{}
		linked := filled()
		for key, value := range linked {
			values[key] = value
		}
		for key, value := range cell.IDs {
			taken := false
			for _, p := range cell.Transaction.Request.Parameters {
				if _, ok := linked[p.In+"."+p.Name]; ok && link.Match(key, p.In, p.Name) {
					taken = true
				}
			}
			if !taken {
				values[key] = value
			}
		}
		if failure := link.Apply(transaction, cell.Transaction, values); failure != "" {
			return errUnprepared(failure)
		}
		return nil
	}
}

// judgeOwnership decides one cell. An id-taking cell must be refused with one
// of the configured statuses, and success is an ownership bypass. A list
// must hold none of the victim's ids.
func judgeOwnership(cell ownershipCell, sent runner.Result, refuse, victims []string) ownershipOutcome {
	status := sent.Actual.StatusCode
	outcome := ownershipOutcome{Cell: cell, Result: sent}
	outcome.Result.Status = runner.StatusPass

	if cell.List {
		if !validate.StatusMatches(cell.Transaction.Response.Status, status) {
			reason := fmt.Sprintf("it answered %s, not the documented %s, so there is no list to read; "+
				"the authorization phase judges that", status, cell.Transaction.Response.Status)
			outcome.Unsent, outcome.Result.Status, outcome.Result.Errors = reason, runner.StatusSkip,
				[]string{"not sent: " + reason}
			return outcome
		}
		if leaked := idsIn(sent.Actual.Body, victims); len(leaked) > 0 {
			outcome.Bypass, outcome.Result.Status = true, runner.StatusFail
			outcome.Result.Errors = []string{fmt.Sprintf(
				"ownership bypass: %s's answer to %s holds tenant %s's %s %s",
				cell.Principal, cell.Operation, cell.Tenant, plural(len(leaked), "id", "ids"), strings.Join(leaked, ", "))}
		}
		return outcome
	}

	switch {
	case slices.Contains(refuse, status):
		return outcome
	case strings.HasPrefix(status, "2"):
		outcome.Bypass = true
		outcome.Result.Errors = []string{fmt.Sprintf(
			"ownership bypass: %s answered %s for %s with tenant %s's resource, where %s is required",
			cell.Operation, status, cell.Principal, cell.Tenant, strings.Join(refuse, " or "))}
	case strings.HasPrefix(status, "5"):
		outcome.Result.Errors = []string{fmt.Sprintf("the server failed with %s where it should have refused %s "+
			"tenant %s's resource", status, cell.Principal, cell.Tenant)}
	default:
		outcome.Result.Errors = []string{fmt.Sprintf("refused %s with %s where ownership.refuse lists %s",
			cell.Principal, status, strings.Join(refuse, ", "))}
	}
	outcome.Result.Status = runner.StatusFail
	return outcome
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// idsIn is which of ids a JSON body carries as the value of an id field: a
// key `id`, or one ending `_id`, `Id` or `ID`. Only id fields are read, so a
// count or a page number that happens to equal an id is not mistaken for one.
func idsIn(body string, ids []string) []string {
	var document any
	if json.Unmarshal([]byte(body), &document) != nil {
		return nil
	}
	found := map[string]bool{}
	var walk func(key string, value any)
	walk = func(key string, value any) {
		switch value := value.(type) {
		case map[string]any:
			for k, v := range value {
				walk(k, v)
			}
		case []any:
			for _, v := range value {
				walk(key, v)
			}
		case string, float64, json.Number:
			if isIDKey(key) {
				text := fmt.Sprint(value)
				if slices.Contains(ids, text) {
					found[text] = true
				}
			}
		}
	}
	walk("", document)
	return slices.Sorted(maps.Keys(found))
}

func isIDKey(key string) bool {
	return strings.EqualFold(key, "id") || strings.HasSuffix(key, "_id") ||
		strings.HasSuffix(key, "Id") || strings.HasSuffix(key, "ID")
}

// ownershipMatrix is the phase's outcomes in the matrix every report shows.
func ownershipMatrix(plan ownershipPlan, outcomes []authorizationOutcome) *reporter.Authorization {
	matrix := authorizationMatrix(authorizationPlan{Operations: plan.Operations, Principals: plan.Columns}, outcomes)
	matrix.Phase = config.PhaseOwnership
	return matrix
}

// dryRunOwnership is the plan with nothing sent.
func dryRunOwnership(plan ownershipPlan) *reporter.Authorization {
	outcomes := make([]authorizationOutcome, 0, len(plan.Cells))
	for _, cell := range plan.Cells {
		reason := cell.Unsent
		switch {
		case reason != "":
		case cell.List:
			reason = "dry run; its answer must hold none of tenant " + cell.Tenant + "'s ids"
		default:
			reason = fmt.Sprintf("dry run; tenant %s's resource, made as %s, must be refused with %s",
				cell.Tenant, cell.Owner, strings.Join(plan.Refuse, " or "))
		}
		outcomes = append(outcomes, authorizationOutcome{
			Cell:   authorizationCell{Operation: cell.Operation, Principal: cell.Column, Status: cell.Expected},
			Unsent: reason,
			Result: runner.Result{Name: cell.Operation + " as " + cell.Column, Status: runner.StatusSkip,
				Errors: []string{"not sent: " + reason}},
		})
	}
	return ownershipMatrix(plan, outcomes)
}
