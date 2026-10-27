package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/runner"
)

// itemsAPI keeps real resources: POST /items makes one and answers its id,
// and /items/{id} answers 404 for an id nobody made — the description's
// example id 1 among them. Reading takes items:read, deleting admin; a
// principal without the role gets 403, or 404 when hide is set, as an API
// that does not reveal what a caller may not see answers.
type itemsAPI struct {
	hide bool
	// conflict answers every create with 409.
	conflict bool

	mu    sync.Mutex
	next  int
	items map[string]bool
	seen  []string // "METHOD path token"
}

var itemsRoles = map[string][]string{
	"reader-token": {"items:read"},
	"admin-token":  {"items:read", "admin"},
}

func (a *itemsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen = append(a.seen, r.Method+" "+r.URL.Path+" "+token)
	reply := func(status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
	roles, known := itemsRoles[token]
	if !known {
		reply(http.StatusUnauthorized, `{"error":"who are you"}`)
		return
	}
	if r.URL.Path == "/items" && r.Method == http.MethodPost {
		if !slices.Contains(roles, "admin") {
			reply(http.StatusForbidden, `{"error":"not yours"}`)
			return
		}
		if a.conflict {
			reply(http.StatusConflict, `{"error":"exists"}`)
			return
		}
		a.next++
		id := fmt.Sprint(100 + a.next)
		if a.items == nil {
			a.items = map[string]bool{}
		}
		a.items[id] = true
		reply(http.StatusCreated, `{"id":"`+id+`"}`)
		return
	}
	id, ok := strings.CutPrefix(r.URL.Path, "/items/")
	if !ok {
		reply(http.StatusNotFound, `{}`)
		return
	}
	need := "items:read"
	if r.Method == http.MethodDelete {
		need = "admin"
	}
	switch {
	case !slices.Contains(roles, need) && a.hide:
		reply(http.StatusNotFound, `{"error":"no such item"}`)
	case !slices.Contains(roles, need):
		reply(http.StatusForbidden, `{"error":"not yours"}`)
	case !a.items[id]:
		reply(http.StatusNotFound, `{"error":"no such item"}`)
	case r.Method == http.MethodDelete:
		delete(a.items, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		reply(http.StatusOK, `{"id":"`+id+`"}`)
	}
}

func (a *itemsAPI) requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.seen)
}

// itemsTransactions is the description: a create whose response links to the
// read and the delete by the id it returned, each of which documents the
// example id 1.
func itemsTransactions() []compile.Transaction {
	byID := func(link string) compile.Link {
		return compile.Link{Name: link, OperationID: link, Parameters: map[string]string{"id": "$response.body#/id"}}
	}
	create := transactionFor("POST", "/items", "201", requires(compile.Requirement{need(bearerScheme, "admin")}))
	create.OperationID = "createItem"
	create.Links = []compile.Link{byID("getItem"), byID("deleteItem")}
	idParameter := []compile.Parameter{{In: compile.InPath, Name: "id", Value: "1", HasValue: true}}
	get := transactionFor("GET", "/items/{id}", "200", requires(compile.Requirement{need(bearerScheme, "items:read")}))
	get.OperationID, get.Request.URI, get.Request.Parameters = "getItem", "/items/1", idParameter
	remove := transactionFor("DELETE", "/items/{id}", "204", requires(compile.Requirement{need(bearerScheme, "admin")}))
	remove.OperationID, remove.Request.URI, remove.Request.Parameters = "deleteItem", "/items/1", idParameter
	return []compile.Transaction{create, get, remove}
}

func itemsPrincipals() []config.Principal {
	return []config.Principal{
		{Name: "anonymous"},
		{Name: "reader", Auth: config.Auth{Header: "Authorization: Bearer reader-token"}, Roles: []string{"items:read"}},
		{Name: "admin", Auth: config.Auth{Header: "Authorization: Bearer admin-token"}, Roles: []string{"items:read", "admin"}},
	}
}

// runItems plans and executes the matrix as the phase does, with settings
// standing in for the configuration's `authorization` section.
func runItems(t *testing.T, api *itemsAPI, settings config.AuthorizationSettings) []authorizationOutcome {
	t.Helper()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	configured := itemsPrincipals()
	plan, principals, err := planAuthorizationPhase(
		config.Config{Principals: configured, Authorization: settings}, itemsTransactions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	engine := runner.New(server.URL)
	credentials, unsupported, err := authorizationCredentials(context.Background(), engine.Client, server.URL,
		configured, principals)
	if err != nil {
		t.Fatal(err)
	}
	return executeAuthorization(context.Background(), engine, plan, credentials, unsupported)
}

// cellLines reads outcomes as "operation principal verdict actual", in plan
// order.
func cellLines(outcomes []authorizationOutcome) []string {
	var lines []string
	for _, outcome := range outcomes {
		verdict := string(outcome.Result.Status)
		if outcome.Bypass {
			verdict += " bypass"
		}
		lines = append(lines, strings.TrimSpace(outcome.Cell.Operation+" "+outcome.Cell.Principal+" "+verdict+" "+
			outcome.Result.Actual.StatusCode))
	}
	return lines
}

// Each id-taking cell addresses a resource made for it, by following the
// description's links as the principal allowed to make one — not the example
// id 1, which does not exist and would make every admitted principal's 404
// fixture noise (#81).
func TestAuthorizationCellsAddressAResourceMadeForThem(t *testing.T) {
	api := &itemsAPI{}
	outcomes := runItems(t, api, config.AuthorizationSettings{})

	want := []string{
		"POST /items anonymous pass 401", "POST /items reader pass 403", "POST /items admin pass 201",
		"GET /items/{id} anonymous pass 401", "GET /items/{id} reader pass 200", "GET /items/{id} admin pass 200",
		"DELETE /items/{id} anonymous pass 401", "DELETE /items/{id} reader pass 403", "DELETE /items/{id} admin pass 204",
	}
	if got := cellLines(outcomes); !slices.Equal(got, want) {
		t.Fatalf("cells:\n  got  %q\n  want %q", got, want)
	}

	// Every id-taking cell had a create of its own, sent as admin, the first
	// principal the description lets make one; and none sent the example id.
	creates, addressed := 0, map[string]bool{}
	for _, request := range api.requests() {
		method, rest, _ := strings.Cut(request, " ")
		path, token, _ := strings.Cut(rest, " ")
		switch {
		case method == "POST" && token == "admin-token":
			creates++
		case strings.HasPrefix(path, "/items/"):
			if path == "/items/1" {
				t.Errorf("%s sent the example id", request)
			}
			if addressed[path] {
				t.Errorf("%s addresses a resource another cell already used", request)
			}
			addressed[path] = true
		}
	}
	// One for the POST cell itself, and one per id-taking cell.
	if creates != 1+6 {
		t.Errorf("%d creates as admin, want 7: one per id-taking cell and the POST cell's own", creates)
	}
	for _, outcome := range outcomes[3:] {
		if outcome.Cell.Owner != "admin" || !strings.Contains(outcome.Cell.Why, "its ids come from") {
			t.Errorf("%s as %s: owner %q, why %q", outcome.Cell.Operation, outcome.Cell.Principal,
				outcome.Cell.Owner, outcome.Cell.Why)
		}
	}
}

// An API that hides what a caller may not see answers 404 where 403 is
// required. That is a failure unless authorization.refuse accepts it — and
// with real ids, accepting it is safe: an admitted principal's 404 can no
// longer be a missing fixture.
func TestAuthorizationRefuseAcceptsTheConfiguredRefusals(t *testing.T) {
	hidden := func(outcomes []authorizationOutcome) []string {
		var lines []string
		for _, line := range cellLines(outcomes) {
			if strings.Contains(line, "reader") && strings.HasPrefix(line, "DELETE") {
				lines = append(lines, line)
			}
		}
		return lines
	}

	strict := runItems(t, &itemsAPI{hide: true}, config.AuthorizationSettings{})
	if got := hidden(strict); !slices.Equal(got, []string{"DELETE /items/{id} reader fail 404"}) {
		t.Errorf("without refuse: %q", got)
	}
	if reason := strict[7].Result.Errors; len(reason) == 0 || !strings.Contains(reason[0], "404") {
		t.Errorf("without refuse, the failure says %q", reason)
	}

	lenient := runItems(t, &itemsAPI{hide: true}, config.AuthorizationSettings{Refuse: []string{"401", "403", "404"}})
	if got := hidden(lenient); !slices.Equal(got, []string{"DELETE /items/{id} reader pass 404"}) {
		t.Errorf("with refuse: %q", got)
	}
}

// A setup that cannot make the cell's resource leaves the cell unsent with
// the reason, and an admitted create answering 409 is its example colliding
// with one that exists: neither is a verdict on who may call.
func TestAuthorizationFixtureFailuresAreNotVerdicts(t *testing.T) {
	outcomes := runItems(t, &itemsAPI{conflict: true}, config.AuthorizationSettings{})
	for _, outcome := range outcomes {
		switch {
		case outcome.Cell.Operation == "POST /items" && outcome.Cell.Principal == "admin":
			if !strings.Contains(outcome.Unsent, "409") || outcome.Result.Status != runner.StatusSkip {
				t.Errorf("colliding create: unsent %q, status %s", outcome.Unsent, outcome.Result.Status)
			}
		case outcome.Cell.Operation == "POST /items":
			if outcome.Result.Status != runner.StatusPass {
				t.Errorf("create refused for %s: %s", outcome.Cell.Principal, outcome.Result.Status)
			}
		default:
			if !strings.Contains(outcome.Unsent, "could not be made") || outcome.Result.Status != runner.StatusSkip {
				t.Errorf("%s as %s: unsent %q, status %s", outcome.Cell.Operation, outcome.Cell.Principal,
					outcome.Unsent, outcome.Result.Status)
			}
		}
	}
}

func TestAuthorizationSetupOwner(t *testing.T) {
	transactions := itemsTransactions()
	principals, err := authorizationPrincipals(itemsPrincipals(), transactions)
	if err != nil {
		t.Fatal(err)
	}
	owners := func(plan authorizationPlan) []string {
		var out []string
		for _, cell := range plan.Cells {
			out = append(out, cell.Owner)
		}
		return out
	}

	plan := planAuthorization(transactions, principals, nil)
	planSetups(&plan, transactions, "")
	if want := []string{"", "", "", "admin", "admin", "admin", "admin", "admin", "admin"}; !slices.Equal(owners(plan), want) {
		t.Errorf("default owners %q, want %q", owners(plan), want)
	}
	if plan.Cells[4].Setup == nil || transactions[plan.Cells[4].Setup[0]].OperationID != "createItem" {
		t.Errorf("GET's setup is %v", plan.Cells[4].Setup)
	}

	configured := planAuthorization(transactions, principals, nil)
	planSetups(&configured, transactions, "reader")
	if got := owners(configured)[3]; got != "reader" {
		t.Errorf("configured owner: %q", got)
	}

	// A create the run will not send is not sent to make a fixture either:
	// the cell keeps the example, and says so.
	skipCreate := func(transaction compile.Transaction) string {
		if transaction.OperationID == "createItem" {
			return "skipped"
		}
		return ""
	}
	skipped := planAuthorization(transactions, principals, skipCreate)
	planSetups(&skipped, transactions, "")
	for _, cell := range skipped.Cells[3:] {
		if cell.Setup != nil || !strings.Contains(cell.Why, "example ids are sent as written") {
			t.Errorf("%s as %s: setup %v, why %q", cell.Operation, cell.Principal, cell.Setup, cell.Why)
		}
	}
	if skipped.Sequencer != nil {
		t.Error("a plan with no setup keeps a sequencer")
	}
}

// The dry run says where each cell's ids would come from.
func TestAuthorizationDryRunNamesTheSetup(t *testing.T) {
	transactions := itemsTransactions()
	plan, _, err := planAuthorizationPhase(config.Config{Principals: itemsPrincipals()}, transactions, nil)
	if err != nil {
		t.Fatal(err)
	}
	matrix := dryRunAuthorization(plan)
	encoded, err := json.Marshal(matrix)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "its ids come from POST /items") {
		t.Errorf("dry run: %s", encoded)
	}
}
