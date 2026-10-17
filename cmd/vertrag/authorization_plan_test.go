package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/runner"
)

var (
	bearerScheme = compile.SecurityScheme{Name: "bearerAuth", Type: "http", Scheme: "bearer"}
	basicScheme  = compile.SecurityScheme{Name: "basicAuth", Type: "http", Scheme: "basic"}
	keyScheme    = compile.SecurityScheme{Name: "apiKey", Type: "apiKey", In: "header", Parameter: "X-Api-Key"}
	cookieScheme = compile.SecurityScheme{Name: "session", Type: "apiKey", In: "cookie", Parameter: "sid"}
	queryScheme  = compile.SecurityScheme{Name: "queryKey", Type: "apiKey", In: "query", Parameter: "key"}
	oauthScheme  = compile.SecurityScheme{Name: "oauth", Type: "oauth2"}
	undefined    = compile.SecurityScheme{Name: "missing"}

	anonymous = authorizationPrincipal{Name: "anonymous"}
	reader    = authorizationPrincipal{Name: "reader",
		Carrier: credentialCarrier{Header: "Authorization", Scheme: "bearer"}, Roles: []string{"mail:read"}}
	admin = authorizationPrincipal{Name: "admin",
		Carrier: credentialCarrier{Header: "Authorization", Scheme: "Bearer"}, Roles: []string{"mail:read", "mail:write", "admin"}}
	keyHolder = authorizationPrincipal{Name: "key",
		Carrier: credentialCarrier{Header: "x-api-key"}}
	cookieHolder = authorizationPrincipal{Name: "cookie", Carrier: credentialCarrier{Cookie: "sid"}, Roles: []string{"admin"}}
	basicHolder  = authorizationPrincipal{Name: "basic",
		Carrier: credentialCarrier{Header: "Authorization", Scheme: "basic"}, Roles: []string{"admin"}}
)

func need(scheme compile.SecurityScheme, roles ...string) compile.Need {
	return compile.Need{Scheme: scheme, Roles: roles}
}

func requires(requirements ...compile.Requirement) compile.Security {
	return compile.Security{Declared: true, Requirements: requirements}
}

func transactionFor(method, template, status string, security compile.Security) compile.Transaction {
	return compile.Transaction{
		Name:     template + " > " + method + " > " + status,
		Request:  compile.Request{Method: method, URI: template, Template: template},
		Response: compile.Response{Status: status},
		Security: security,
	}
}

func TestAuthorizationExpectations(t *testing.T) {
	adminOnly := requires(compile.Requirement{need(bearerScheme, "admin")})
	cases := []struct {
		name      string
		security  compile.Security
		principal authorizationPrincipal
		expect    authorizationExpectation
		status    string
		sends     bool
		unsent    string // a substring of the reason it is not sent; empty when it is sent
	}{
		{"public admits the anonymous", compile.Security{Declared: true, Public: true},
			anonymous, expectAllowed, "200", false, ""},
		{"public admits a credential too, which still goes out", compile.Security{Declared: true, Public: true},
			reader, expectAllowed, "200", true, ""},
		{"optional {} among alternatives is public",
			compile.Security{Declared: true, Public: true, Requirements: []compile.Requirement{{need(bearerScheme, "admin")}}},
			anonymous, expectAllowed, "200", false, ""},
		{"silence is not public", compile.Security{},
			reader, expectUndetermined, "", true, "does not say"},
		{"declared with no scheme decides nothing", compile.Security{Declared: true},
			anonymous, expectUndetermined, "", false, "names no scheme"},
		{"no credential is 401", adminOnly, anonymous, expectUnauthenticated, "401", false, ""},
		{"the roles are held", adminOnly, admin, expectAllowed, "200", true, ""},
		{"a credential without the roles is 403", adminOnly, reader, expectForbidden, "403", true, ""},
		{"no roles listed: any credential for the scheme", requires(compile.Requirement{need(bearerScheme)}),
			reader, expectAllowed, "200", true, ""},
		{"every role listed is required",
			requires(compile.Requirement{need(bearerScheme, "mail:read", "mail:write")}),
			reader, expectForbidden, "403", true, ""},
		{"a credential of another scheme is not accepted: 401", adminOnly,
			keyHolder, expectUnauthenticated, "401", true, ""},
		{"basic is not bearer", adminOnly, basicHolder, expectUnauthenticated, "401", true, ""},
		{"basic satisfies basic", requires(compile.Requirement{need(basicScheme, "admin")}),
			basicHolder, expectAllowed, "200", true, ""},
		{"OR: the other alternative admits the key",
			requires(compile.Requirement{need(bearerScheme, "admin")}, compile.Requirement{need(keyScheme)}),
			keyHolder, expectAllowed, "200", true, ""},
		{"OR: the best alternative decides, so lacking roles stays 403",
			requires(compile.Requirement{need(keyScheme)}, compile.Requirement{need(bearerScheme, "admin")}),
			reader, expectForbidden, "403", true, ""},
		{"AND: one credential cannot meet two schemes",
			requires(compile.Requirement{need(bearerScheme), need(keyScheme)}),
			admin, expectUndetermined, "", true, "together"},
		{"AND: but the anonymous is still 401",
			requires(compile.Requirement{need(bearerScheme), need(keyScheme)}),
			anonymous, expectUnauthenticated, "401", false, ""},
		{"AND that one alternative cannot meet does not hide another it can",
			requires(compile.Requirement{need(bearerScheme), need(keyScheme)}, compile.Requirement{need(bearerScheme, "admin")}),
			admin, expectAllowed, "200", true, ""},
		{"a named cookie is a carrier", requires(compile.Requirement{need(cookieScheme, "admin")}),
			cookieHolder, expectAllowed, "200", true, ""},
		{"oauth2 scopes are roles", requires(compile.Requirement{need(oauthScheme, "mail:write")}),
			reader, expectForbidden, "403", true, ""},
		{"a query key cannot be isolated, so no refusal is asked of it",
			requires(compile.Requirement{need(queryScheme)}, compile.Requirement{need(bearerScheme, "admin")}),
			anonymous, expectUnauthenticated, "401", false, "query"},
		{"nor a 403",
			requires(compile.Requirement{need(queryScheme)}, compile.Requirement{need(bearerScheme, "admin")}),
			reader, expectForbidden, "403", true, "query"},
		{"but a principal the description admits is still sent",
			requires(compile.Requirement{need(queryScheme)}, compile.Requirement{need(bearerScheme, "admin")}),
			admin, expectAllowed, "200", true, ""},
		{"an undefined scheme is not presentable", requires(compile.Requirement{need(undefined)}),
			anonymous, expectUnauthenticated, "401", false, "not defined"},
		{"auth.except sends no credential: 401", adminOnly,
			withExcept(admin, "/admin > GET > 200"), expectUnauthenticated, "401", false, ""},
		{"nor to the operation that granted it", adminOnly,
			withLogin(admin, "GET", "/admin"), expectUnauthenticated, "401", false, ""},
		{"an excluded credential on a public operation is still allowed", compile.Security{Declared: true, Public: true},
			withExcept(admin, "/admin > GET > 200"), expectAllowed, "200", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cell := expectAuthorization(c.security, c.principal, transactionFor("GET", "/admin", "200", c.security))
			if cell.Expect != c.expect || cell.Status != c.status || cell.Credential != c.sends {
				t.Errorf("got %s %q credential=%v (%s), want %s %q credential=%v",
					cell.Expect, cell.Status, cell.Credential, cell.Why, c.expect, c.status, c.sends)
			}
			switch {
			case c.unsent == "" && cell.Unsent != "":
				t.Errorf("unsent %q, want it sent", cell.Unsent)
			case c.unsent != "" && !strings.Contains(cell.Unsent, c.unsent):
				t.Errorf("unsent %q, want a reason mentioning %q", cell.Unsent, c.unsent)
			}
			if cell.Expect == expectUndetermined && cell.Unsent == "" {
				t.Error("an undetermined cell must never be sent")
			}
		})
	}
}

func withExcept(principal authorizationPrincipal, names ...string) authorizationPrincipal {
	principal.Except = map[string]bool{}
	for _, name := range names {
		principal.Except[name] = true
	}
	return principal
}

func withLogin(principal authorizationPrincipal, method, path string) authorizationPrincipal {
	principal.LoginMethod, principal.LoginPath = method, path
	return principal
}

func TestAuthorizationPlanIsDeterministic(t *testing.T) {
	protected := requires(compile.Requirement{need(bearerScheme, "admin")}, compile.Requirement{need(cookieScheme)})
	public := compile.Security{Declared: true, Public: true}
	transactions := []compile.Transaction{
		transactionFor("DELETE", "/users/{id}", "204", protected),
		transactionFor("GET", "/users/{id}", "404", protected),
		transactionFor("GET", "/users/{id}", "201", protected),
		transactionFor("GET", "/users/{id}", "200", protected),
		transactionFor("POST", "/health", "500", public),
		transactionFor("GET", "/health", "200", public),
	}
	principals := []authorizationPrincipal{anonymous, reader, admin, keyHolder}

	plan := planAuthorization(transactions, principals, nil)

	if want := []string{"GET /health", "POST /health", "GET /users/{id}", "DELETE /users/{id}"}; !slices.Equal(plan.Operations, want) {
		t.Errorf("operations %v, want %v", plan.Operations, want)
	}
	if want := []string{"anonymous", "reader", "admin", "key"}; !slices.Equal(plan.Principals, want) {
		t.Errorf("principals %v, want %v", plan.Principals, want)
	}
	if len(plan.Cells) != len(plan.Operations)*len(principals) {
		t.Fatalf("%d cells, want %d", len(plan.Cells), len(plan.Operations)*len(principals))
	}
	for i, cell := range plan.Cells {
		if cell.Operation != plan.Operations[i/len(principals)] || cell.Principal != plan.Principals[i%len(principals)] {
			t.Errorf("cell %d is %s × %s, out of order", i, cell.Operation, cell.Principal)
		}
	}

	// The lowest documented success is the request everyone sends, and the
	// status a caller the description admits must get.
	users := plan.Cells[2*len(principals) : 3*len(principals)]
	for _, cell := range users {
		if cell.Transaction.Response.Status != "200" {
			t.Errorf("%s sends the %s transaction, want the 200", cell.Principal, cell.Transaction.Response.Status)
		}
	}
	got := []string{users[0].Status, users[1].Status, users[2].Status, users[3].Status}
	if want := []string{"401", "403", "200", "401"}; !slices.Equal(got, want) {
		t.Errorf("GET /users/{id} expects %v, want %v", got, want)
	}

	// An operation with no documented success has nothing to send.
	for _, cell := range plan.Cells[len(principals) : 2*len(principals)] {
		if cell.Unsent == "" {
			t.Errorf("POST /health as %s is sent, but documents no success", cell.Principal)
		}
	}

	wantCarriers := runner.Carriers{Headers: []string{"Authorization", "x-api-key"}, Cookies: []string{"sid"}}
	if !reflect.DeepEqual(plan.Carriers, wantCarriers) {
		t.Errorf("carriers %+v, want %+v", plan.Carriers, wantCarriers)
	}

	// The order compiling produced does not change the plan.
	reversed := slices.Clone(transactions)
	slices.Reverse(reversed)
	if again := planAuthorization(reversed, principals, nil); !reflect.DeepEqual(again, plan) {
		t.Error("planning the same transactions in another order gave another plan")
	}
}

func TestAuthorizationPlanKeepsTheRunsSafetyDecisions(t *testing.T) {
	protected := requires(compile.Requirement{need(bearerScheme, "admin")})
	transactions := []compile.Transaction{
		transactionFor("DELETE", "/mailboxes/{id}", "204", protected),
		transactionFor("GET", "/mailboxes/{id}", "200", protected),
	}
	skip := func(transaction compile.Transaction) string {
		if transaction.Request.Method == "DELETE" {
			return "irreversible"
		}
		return ""
	}

	plan := planAuthorization(transactions, []authorizationPrincipal{anonymous, admin}, skip)

	for _, cell := range plan.Cells {
		deleting := strings.HasPrefix(cell.Operation, "DELETE")
		if deleting != (cell.Unsent == "irreversible") {
			t.Errorf("%s as %s: unsent %q", cell.Operation, cell.Principal, cell.Unsent)
		}
		// The matrix still says who the description lets in.
		if cell.Principal == "admin" && cell.Expect != expectAllowed ||
			cell.Principal == "anonymous" && cell.Expect != expectUnauthenticated {
			t.Errorf("%s as %s expects %s", cell.Operation, cell.Principal, cell.Expect)
		}
	}
}

func TestAuthorizationPrincipalsKnowWhereTheirCredentialTravels(t *testing.T) {
	login := config.Login{Method: "POST", Path: "/login"}
	cases := []struct {
		name string
		auth config.Auth
		want credentialCarrier
		err  string
	}{
		{"no auth holds no credential", config.Auth{}, credentialCarrier{}, ""},
		{"a static key", config.Auth{Header: "X-Api-Key: secret"}, credentialCarrier{Header: "X-Api-Key"}, ""},
		{"a static basic", config.Auth{Header: "Authorization: Basic YTpi"},
			credentialCarrier{Header: "Authorization", Scheme: "basic"}, ""},
		{"a static cookie", config.Auth{Header: "Cookie: sid=abc"}, credentialCarrier{Cookie: "sid"}, ""},
		{"two static cookies are not one credential", config.Auth{Header: "Cookie: sid=abc; csrf=x"}, credentialCarrier{}, "exactly one"},
		{"a login defaults to a bearer token", config.Auth{Login: login}, credentialCarrier{Header: "Authorization", Scheme: "bearer"}, ""},
		{"a login's own template", config.Auth{Login: login, Header: "X-Session: {$response.body#/id}"},
			credentialCarrier{Header: "X-Session"}, ""},
		{"a login's named cookie", config.Auth{Login: login, Carry: "cookie", Cookie: "sid"}, credentialCarrier{Cookie: "sid"}, ""},
		{"a login's unnamed cookies cannot be planned", config.Auth{Login: login, Carry: "cookie"}, credentialCarrier{}, "auth.cookie"},
		{"oauth2 is a bearer token", config.Auth{OAuth2: config.OAuth2{TokenURL: "/token", ClientID: "c"}},
			credentialCarrier{Header: "Authorization", Scheme: "bearer"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := carrierOf(c.auth)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err %v, want one mentioning %q", err, c.err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Errorf("got %+v %v, want %+v", got, err, c.want)
			}
		})
	}

	transactions := []compile.Transaction{transactionFor("POST", "/login", "200", compile.Security{Declared: true, Public: true})}
	principals, err := authorizationPrincipals([]config.Principal{
		{Name: "admin", Auth: config.Auth{Login: login, Except: []string{"/login > POST > 200"}}, Roles: []string{"admin"}},
		{Name: "anonymous"},
	}, transactions)
	if err != nil {
		t.Fatal(err)
	}
	if len(principals) != 2 || principals[0].Name != "admin" || !principals[0].Except["/login > POST > 200"] ||
		principals[0].LoginPath != "/login" || !slices.Equal(principals[0].Roles, []string{"admin"}) ||
		!principals[1].Carrier.none() {
		t.Errorf("principals %+v", principals)
	}
	if _, err := authorizationPrincipals([]config.Principal{{Name: "cookies", Auth: config.Auth{Login: login, Carry: "cookie"}}},
		transactions); err == nil || !strings.Contains(err.Error(), `"cookies"`) {
		t.Errorf("err %v, want it to name the principal", err)
	}
}

func TestAuthorizationCarriersAreEveryCredentialPlaceOnce(t *testing.T) {
	transactions := []compile.Transaction{
		transactionFor("GET", "/keys", "200", requires(compile.Requirement{need(keyScheme)})),
		transactionFor("GET", "/me", "200", requires(compile.Requirement{need(oauthScheme)}, compile.Requirement{need(cookieScheme)})),
		transactionFor("GET", "/search", "200", requires(compile.Requirement{need(queryScheme)})),
	}
	principals := []authorizationPrincipal{keyHolder, reader, anonymous,
		{Name: "other", Carrier: credentialCarrier{Cookie: "SID"}}}
	want := runner.Carriers{Headers: []string{"Authorization", "X-Api-Key"}, Cookies: []string{"SID", "sid"}}

	for _, order := range [][]compile.Transaction{transactions, {transactions[2], transactions[1], transactions[0]}} {
		if got := authorizationCarriers(order, principals); !reflect.DeepEqual(got, want) {
			t.Errorf("carriers %+v, want %+v", got, want)
		}
	}
}
