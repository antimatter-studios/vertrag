package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/runner"
)

// tenantAPI keeps items per tenant: an admin makes them, anyone of the
// tenant reads them, an admin deletes them, and another tenant's item is not
// found. brokenRead forgets the owner check on reading one item, brokenList
// on listing them — the two mistakes the ownership phase exists to find.
type tenantAPI struct {
	brokenRead bool
	brokenList bool

	mu    sync.Mutex
	next  int
	owner map[string]string // item id → tenant
}

var tenantTokens = map[string]struct {
	tenant string
	admin  bool
}{
	"tok-admin-a": {"a", true}, "tok-user-a": {"a", false},
	"tok-admin-b": {"b", true}, "tok-user-b": {"b", false},
}

func (a *tenantAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	reply := func(status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	caller, known := tenantTokens[token]
	if !known {
		reply(http.StatusUnauthorized, `{"error":"who are you"}`)
		return
	}
	if a.owner == nil {
		a.owner = map[string]string{}
	}
	switch {
	case r.URL.Path == "/items" && r.Method == http.MethodPost:
		if !caller.admin {
			reply(http.StatusForbidden, `{}`)
			return
		}
		a.next++
		id := fmt.Sprint(a.next)
		a.owner[id] = caller.tenant
		reply(http.StatusCreated, `{"id":"`+id+`"}`)
	case r.URL.Path == "/items":
		var items []string
		for id, tenant := range a.owner {
			if tenant == caller.tenant || a.brokenList {
				items = append(items, `{"id":"`+id+`","count":"`+id+`"}`)
			}
		}
		slices.Sort(items)
		reply(http.StatusOK, `{"items":[`+strings.Join(items, ",")+`]}`)
	default:
		id, _ := strings.CutPrefix(r.URL.Path, "/items/")
		tenant, exists := a.owner[id]
		visible := exists && (tenant == caller.tenant || a.brokenRead && r.Method == http.MethodGet)
		switch {
		case r.Method == http.MethodDelete && !caller.admin:
			reply(http.StatusForbidden, `{}`)
		case !visible:
			reply(http.StatusNotFound, `{"error":"no such item"}`)
		case r.Method == http.MethodDelete:
			delete(a.owner, id)
			w.WriteHeader(http.StatusNoContent)
		default:
			reply(http.StatusOK, `{"id":"`+id+`"}`)
		}
	}
}

func tenantTransactions() []compile.Transaction {
	transactions := itemsTransactions()
	list := transactionFor("GET", "/items", "200", requires(compile.Requirement{need(bearerScheme, "items:read")}))
	list.OperationID = "listItems"
	return append(transactions, list)
}

func tenantSettings() config.Config {
	principal := func(name string, roles ...string) config.Principal {
		return config.Principal{Name: name, Auth: config.Auth{Header: "Authorization: Bearer tok-" + name}, Roles: roles}
	}
	return config.Config{
		Principals: []config.Principal{
			{Name: "anonymous"},
			principal("admin-a", "items:read", "admin"), principal("user-a", "items:read"),
			principal("admin-b", "items:read", "admin"), principal("user-b", "items:read"),
		},
		Tenants: map[string][]string{"b": {"admin-b", "user-b"}, "a": {"admin-a", "user-a"}},
	}
}

func runTenants(t *testing.T, api *tenantAPI) ([]authorizationOutcome, ownershipPlan) {
	t.Helper()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	settings := tenantSettings()
	plan, err := planOwnership(settings, tenantTransactions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	engine := runner.New(server.URL)
	credentials, unsupported, err := authorizationCredentials(context.Background(), engine.Client, server.URL,
		settings.Principals, plan.Members)
	if err != nil {
		t.Fatal(err)
	}
	return executeOwnership(context.Background(), engine, plan, credentials, unsupported), plan
}

// ownershipLines reads outcomes as "operation column verdict actual".
func ownershipLines(outcomes []authorizationOutcome) []string {
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

func TestOwnershipPassesAServerThatChecksOwnership(t *testing.T) {
	outcomes, plan := runTenants(t, &tenantAPI{})
	if want := []string{"admin-b → a", "user-b → a", "admin-a → b", "user-a → b"}; !slices.Equal(plan.Columns, want) {
		t.Errorf("columns %q, want %q", plan.Columns, want)
	}
	want := []string{
		"GET /items admin-b → a pass 200", "GET /items user-b → a pass 200",
		"GET /items admin-a → b pass 200", "GET /items user-a → b pass 200",
		"GET /items/{id} admin-b → a pass 404", "GET /items/{id} user-b → a pass 404",
		"GET /items/{id} admin-a → b pass 404", "GET /items/{id} user-a → b pass 404",
		"DELETE /items/{id} admin-b → a pass 404", "DELETE /items/{id} user-b → a skip",
		"DELETE /items/{id} admin-a → b pass 404", "DELETE /items/{id} user-a → b skip",
	}
	if got := ownershipLines(outcomes); !slices.Equal(got, want) {
		t.Fatalf("cells:\n  got  %q\n  want %q", got, want)
	}
	if !strings.Contains(outcomes[9].Unsent, "roles already refuse it") {
		t.Errorf("a role refusal is the authorization phase's: %q", outcomes[9].Unsent)
	}
	if matrix := ownershipMatrix(plan, outcomes); !matrix.Met() || matrix.Title() != "Ownership matrix" {
		t.Errorf("matrix %q met %v", matrix.Title(), matrix.Met())
	}
}

// The done-when of #82: a deliberately removed ownership check is reported as
// a bypass, for a read by id and for a list.
func TestOwnershipReportsARemovedCheckAsABypass(t *testing.T) {
	outcomes, plan := runTenants(t, &tenantAPI{brokenRead: true})
	for _, outcome := range outcomes {
		if outcome.Cell.Operation != "GET /items/{id}" {
			continue
		}
		if !outcome.Bypass || outcome.Result.Status != runner.StatusFail ||
			!strings.Contains(outcome.Result.Errors[0], "ownership bypass") || outcome.Replay == "" {
			t.Errorf("%s: bypass %v status %s errors %q", outcome.Cell.Principal, outcome.Bypass,
				outcome.Result.Status, outcome.Result.Errors)
		}
	}
	matrix := ownershipMatrix(plan, outcomes)
	if matrix.Met() || matrix.Counts().Bypasses != 4 || !strings.Contains(matrix.Verdict(), "4 ownership bypasses") {
		t.Errorf("matrix: %s", matrix.Verdict())
	}

	listed, _ := runTenants(t, &tenantAPI{brokenList: true})
	for _, outcome := range listed[:4] {
		if !outcome.Bypass || !strings.Contains(outcome.Result.Errors[0], "holds tenant") {
			t.Errorf("list as %s: bypass %v errors %q", outcome.Cell.Principal, outcome.Bypass, outcome.Result.Errors)
		}
	}
}

func TestOwnershipOnlyReadsIDFields(t *testing.T) {
	body := `{"items":[{"id":"7","count":"9"},{"account_id":9,"ownerId":"11"}],"page":"3"}`
	if got := idsIn(body, []string{"3", "7", "9", "11"}); !slices.Equal(got, []string{"11", "7", "9"}) {
		t.Errorf("idsIn = %q", got)
	}
	if got := idsIn("not json", []string{"1"}); got != nil {
		t.Errorf("idsIn of text = %q", got)
	}
}

func TestOwnershipDryRunSendsNothing(t *testing.T) {
	plan, err := planOwnership(tenantSettings(), tenantTransactions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	matrix := dryRunOwnership(plan)
	encoded, _ := json.Marshal(matrix.Normal())
	if !strings.Contains(string(encoded), "tenant a's resource, made as admin-a, must be refused with 403 or 404") {
		t.Errorf("dry run: %s", encoded)
	}
	if matrix.Counts().Unsent != matrix.Counts().Cells {
		t.Errorf("a dry run sent a cell: %+v", matrix.Counts())
	}
}

const ownershipSpec = `openapi: 3.1.0
info: {title: Items, version: "1"}
components:
  securitySchemes:
    bearer: {type: http, scheme: bearer}
paths:
  /items:
    get:
      operationId: listItems
      security: [{bearer: [items:read]}]
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
    post:
      operationId: createItem
      security: [{bearer: [admin]}]
      responses:
        "201":
          description: made
          content: {application/json: {schema: {type: object}}}
          links:
            getItem: {operationId: getItem, parameters: {id: $response.body#/id}}
            deleteItem: {operationId: deleteItem, parameters: {id: $response.body#/id}}
  /items/{id}:
    parameters: [{name: id, in: path, required: true, schema: {type: string}, example: "1"}]
    get:
      operationId: getItem
      security: [{bearer: [items:read]}]
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object}}}}
    delete:
      operationId: deleteItem
      security: [{bearer: [admin]}]
      responses:
        "204": {description: gone}
`

func writeOwnershipConfig(t *testing.T, endpoint string) string {
	t.Helper()
	dir := t.TempDir()
	spec := filepath.Join(dir, "openapi.yaml")
	if err := os.WriteFile(spec, []byte(ownershipSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "vertrag.yml")
	settings := fmt.Sprintf(`spec: %s
endpoint: %s
phases: [ownership]
auth: {header: "Authorization: Bearer tok-admin-a"}
principals:
  - {name: admin-a, roles: [items:read, admin], auth: {header: "Authorization: Bearer tok-admin-a"}}
  - {name: user-a, roles: [items:read], auth: {header: "Authorization: Bearer tok-user-a"}}
  - {name: admin-b, roles: [items:read, admin], auth: {header: "Authorization: Bearer tok-admin-b"}}
  - {name: user-b, roles: [items:read], auth: {header: "Authorization: Bearer tok-user-b"}}
tenants:
  a: [admin-a, user-a]
  b: [admin-b, user-b]
`, spec, endpoint)
	if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Through `vertrag run`: a server that checks ownership passes, one that
// forgets the check fails the run, with the bypass in the matrix.
func TestTheOwnershipPhaseThroughRun(t *testing.T) {
	correct := httptest.NewServer(&tenantAPI{})
	defer correct.Close()
	stdout, err := runAuthzCLI(t, "--config", writeOwnershipConfig(t, correct.URL))
	if err != nil {
		t.Fatalf("a correct server: %v\n%s", err, stdout)
	}
	for _, line := range []string{"Ownership matrix", "ownership: 12 cells, 10 passing, 0 failing, 0 errors, 2 unsent"} {
		if !strings.Contains(stdout, line) {
			t.Errorf("stdout lacks %q:\n%s", line, stdout)
		}
	}

	broken := httptest.NewServer(&tenantAPI{brokenRead: true})
	defer broken.Close()
	stdout, err = runAuthzCLI(t, "--config", writeOwnershipConfig(t, broken.URL))
	if err == nil {
		t.Fatalf("a missing ownership check passed:\n%s", stdout)
	}
	if !strings.Contains(stdout, "4 ownership bypasses") || !strings.Contains(stdout, "ownership bypass: GET /items/{id} answered 200") {
		t.Errorf("stdout:\n%s", stdout)
	}
}
