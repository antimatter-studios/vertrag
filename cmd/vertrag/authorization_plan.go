package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/runner"
)

// The authorization phase, planned: which request each principal sends to
// each operation, and what the description says must come back. Planning
// sends nothing. It is a separate step so that a dry run can print the matrix
// and so that every expectation can be tested without a server — the
// executor's only job is to send what the plan says and compare.

// authorizationPrincipal is a principal as the planner needs it: where its
// credential travels, not what the credential is. Nothing here requires
// logging in, so a plan can be made before any request is sent.
type authorizationPrincipal struct {
	Name string

	// Carrier is where the credential travels. Zero for a principal holding
	// none, which is the caller every protected operation must answer 401.
	Carrier credentialCarrier

	// Roles are the role names, or scopes, its credential carries.
	Roles []string

	// Except names transactions it sends without its credential, and
	// LoginMethod and LoginPath the operation that granted the credential,
	// which never receives it — exactly as for the run's own credential. A
	// request sent without the credential is judged as an anonymous one.
	Except      map[string]bool
	LoginMethod string
	LoginPath   string
}

// credentialCarrier is where a credential travels: a header, or one named
// cookie. Scheme is the Authorization header's scheme — bearer, basic — and
// empty for any other header.
type credentialCarrier struct {
	Header string
	Cookie string
	Scheme string
}

func (c credentialCarrier) none() bool { return c.Header == "" && c.Cookie == "" }

// authorizationPrincipals reads the configured principals into what the
// planner needs, working out each credential's carrier the way auth.Obtain
// will produce it — without obtaining it.
//
// A carrier it cannot work out is an error rather than a guess: a principal
// mistaken for one holding no credential would be expected to get 401, and
// its credential's success reported as a bypass.
func authorizationPrincipals(configured []config.Principal, transactions []compile.Transaction) (
	[]authorizationPrincipal, error) {
	out := make([]authorizationPrincipal, 0, len(configured))
	for _, principal := range configured {
		carrier, err := carrierOf(principal.Auth)
		if err != nil {
			return nil, fmt.Errorf("principal %q: %w", principal.Name, err)
		}
		// auth.except entries that match nothing are Doctor's to report.
		except, _ := exceptedTransactions(principal.Auth.Except, transactions)
		out = append(out, authorizationPrincipal{
			Name:        principal.Name,
			Carrier:     carrier,
			Roles:       slices.Clone(principal.Roles),
			Except:      except,
			LoginMethod: principal.Auth.Login.Method,
			LoginPath:   principal.Auth.Login.Path,
		})
	}
	return out, nil
}

// carrierOf is where the credential an auth block obtains will travel.
func carrierOf(settings config.Auth) (credentialCarrier, error) {
	switch {
	case !settings.Configured():
		return credentialCarrier{}, nil
	case settings.OAuth2.Configured():
		// The token type a server names is almost always Bearer, and is what
		// auth assumes when it names none.
		return credentialCarrier{Header: "Authorization", Scheme: "bearer"}, nil
	case settings.Login.Path != "" && strings.EqualFold(settings.Carry, "cookie"):
		if settings.Cookie == "" {
			return credentialCarrier{}, errors.New(
				"its credential is the login's cookies, unnamed; set auth.cookie to the one that authenticates")
		}
		return credentialCarrier{Cookie: settings.Cookie}, nil
	}

	line := settings.Header
	if line == "" {
		line = "Authorization: Bearer"
	}
	name, value, ok := strings.Cut(line, ":")
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if !ok || name == "" {
		return credentialCarrier{}, fmt.Errorf("auth.header %q is not a `Name: value` line", settings.Header)
	}
	switch {
	case strings.EqualFold(name, "Cookie"):
		cookies := splitCookieLine(value)
		if len(cookies) != 1 {
			return credentialCarrier{}, errors.New("its Cookie header must carry exactly one cookie to be one credential")
		}
		return credentialCarrier{Cookie: cookies[0]}, nil
	case strings.EqualFold(name, "Authorization"):
		scheme, _, _ := strings.Cut(value, " ")
		return credentialCarrier{Header: name, Scheme: strings.ToLower(scheme)}, nil
	default:
		return credentialCarrier{Header: name}, nil
	}
}

// splitCookieLine is the cookie names in a Cookie header's value.
func splitCookieLine(value string) []string {
	var names []string
	for _, pair := range strings.Split(value, ";") {
		if name, _, _ := strings.Cut(strings.TrimSpace(pair), "="); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// presents reports whether the principal's credential goes out on this
// transaction, by the same rules runner.SendAs applies.
func (p authorizationPrincipal) presents(transaction compile.Transaction) bool {
	if p.Carrier.none() || p.Except[transaction.Name] {
		return false
	}
	granted := runner.Credential{LoginMethod: p.LoginMethod, LoginPath: p.LoginPath}
	return !granted.GrantedBy(transaction)
}

// authorizationExpectation is what the description says one cell must answer.
type authorizationExpectation string

const (
	// expectAllowed is the documented success response.
	expectAllowed authorizationExpectation = "allowed"
	// expectUnauthenticated is 401: the caller presented no credential the
	// operation accepts.
	expectUnauthenticated authorizationExpectation = "unauthenticated"
	// expectForbidden is 403: the credential is one the operation accepts,
	// without the roles it requires.
	expectForbidden authorizationExpectation = "forbidden"
	// expectUndetermined is a cell the description does not decide, or
	// decides in a way no request vertrag can send would test. It is never
	// sent and never a pass.
	expectUndetermined authorizationExpectation = "undetermined"
)

// authorizationCell is one operation sent as one principal.
type authorizationCell struct {
	// Operation is the operation's label, and Principal the principal's name:
	// the row and column of the report's matrix.
	Operation string
	Principal string

	// Transaction is the documented success transaction every principal
	// sends for this operation. Zero when the operation documents none.
	Transaction compile.Transaction

	// Expect and Status are what must come back: the documented success
	// status, "401" or "403". Status is empty when Expect is undetermined.
	Expect authorizationExpectation
	Status string

	// Credential says whether the principal's credential goes out.
	Credential bool

	// Unsent, when set, is why this cell is not sent. An unsent cell is
	// reported as such, never as a pass.
	Unsent string

	// Why is the reason for the expectation, for the report.
	Why string
}

// authorizationPlan is the whole matrix, operation by operation in a fixed
// order and, within each, principal by principal in the order configured.
type authorizationPlan struct {
	Operations []string
	Principals []string
	Cells      []authorizationCell

	// Carriers are every place a credential can travel on these operations,
	// for runner.SendAs to clear before it adds the principal's own.
	Carriers runner.Carriers
}

// planAuthorization works out the matrix for the selected transactions.
//
// skip is the run's own safety decision for a transaction — the selection,
// the irreversible-operation guard — and returns why it must not be sent, or
// "". An operation it refuses keeps its expectations and is marked unsent:
// the matrix still says who the description lets in, and makes no claim that
// anyone checked.
func planAuthorization(transactions []compile.Transaction, principals []authorizationPrincipal,
	skip func(compile.Transaction) string) authorizationPlan {
	plan := authorizationPlan{}
	for _, principal := range principals {
		plan.Principals = append(plan.Principals, principal.Name)
	}

	for _, group := range authorizationOperations(transactions) {
		success, documented := documentedSuccess(group)
		label := operationLabel(group[0])
		plan.Operations = append(plan.Operations, label)

		unsent := ""
		switch {
		case !documented:
			unsent = "the operation documents no success response to send"
		case skip != nil:
			unsent = skip(success)
		}
		for _, principal := range principals {
			cell := expectAuthorization(group[0].Security, principal, success)
			cell.Operation, cell.Principal = label, principal.Name
			if documented {
				cell.Transaction = success
			}
			if cell.Unsent == "" {
				cell.Unsent = unsent
			}
			plan.Cells = append(plan.Cells, cell)
		}
	}
	plan.Carriers = authorizationCarriers(transactions, principals)
	return plan
}

// authorizationOperations groups transactions by operation, ordered by path
// and then method, so that the matrix does not depend on the order compiling
// happened to produce.
func authorizationOperations(transactions []compile.Transaction) [][]compile.Transaction {
	index := map[operation]int{}
	var groups [][]compile.Transaction
	for _, transaction := range transactions {
		key := operationOf(transaction)
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], transaction)
	}
	slices.SortStableFunc(groups, func(a, b []compile.Transaction) int {
		if c := strings.Compare(operationKey(a[0]), operationKey(b[0])); c != 0 {
			return c
		}
		return methodRank(a[0].Request.Method) - methodRank(b[0].Request.Method)
	})
	return groups
}

// methodRank orders methods as an OpenAPI Path Item lists them, with any
// other method after, so GET comes before DELETE in the matrix.
func methodRank(method string) int {
	order := []string{"GET", "PUT", "POST", "DELETE", "OPTIONS", "HEAD", "PATCH", "TRACE"}
	if i := slices.Index(order, strings.ToUpper(method)); i >= 0 {
		return i
	}
	return len(order)
}

// documentedSuccess is the transaction every principal sends to an operation:
// its lowest documented 2xx, and of those the first by name.
func documentedSuccess(group []compile.Transaction) (compile.Transaction, bool) {
	var best compile.Transaction
	found := false
	for _, transaction := range group {
		status := statusOf(transaction)
		if !isSuccess(status) {
			continue
		}
		if !found || status < statusOf(best) || status == statusOf(best) && transaction.Name < best.Name {
			best, found = transaction, true
		}
	}
	return best, found
}

// expectAuthorization decides one cell from the operation's security: its
// alternatives, any one of which admits a caller, each requiring every scheme
// it names with the roles listed against it.
func expectAuthorization(security compile.Security, principal authorizationPrincipal,
	transaction compile.Transaction) authorizationCell {
	presents := principal.presents(transaction)
	cell := authorizationCell{Credential: presents}

	switch {
	case !security.Declared:
		// Silence is not a statement that the operation is public; see
		// compile.Security.Declared.
		cell.Expect = expectUndetermined
		cell.Unsent = "the description does not say who may call this operation"
		return cell
	case security.Public:
		cell.Expect, cell.Status = expectAllowed, transaction.Response.Status
		cell.Why = "the operation admits a caller with no credential"
		return cell
	case len(security.Requirements) == 0:
		// Declared, not public, and naming no scheme: nothing says what would
		// let a caller in, so neither a refusal nor a success can be asked for.
		cell.Expect = expectUndetermined
		cell.Unsent = "the operation's security names no scheme"
		return cell
	}

	unsupported := ""
	for _, scheme := range security.Schemes() {
		if reason := unsupportedScheme(scheme); reason != "" {
			unsupported = reason
			break
		}
	}

	if !presents {
		cell.Expect, cell.Status = expectUnauthenticated, "401"
		cell.Why = "no credential is presented"
		if !principal.Carrier.none() {
			cell.Why = "the credential is not sent on this operation (auth.except or its login)"
		}
		if unsupported != "" {
			// A credential vertrag cannot take off the request may be on it
			// anyway, from the example, and a refusal cannot be asked for.
			cell.Unsent = unsupported
		}
		return cell
	}

	verdict, why := judgeRequirements(security.Requirements, principal)
	switch verdict {
	case requirementSatisfied:
		cell.Expect, cell.Status, cell.Why = expectAllowed, transaction.Response.Status, why
	case requirementLacksRoles:
		cell.Expect, cell.Status, cell.Why = expectForbidden, "403", why
	case requirementPartial:
		cell.Expect, cell.Why = expectUndetermined, why
		cell.Unsent = why
	default:
		cell.Expect, cell.Status, cell.Why = expectUnauthenticated, "401", why
	}
	if cell.Expect != expectAllowed && unsupported != "" && cell.Unsent == "" {
		cell.Unsent = unsupported
	}
	return cell
}

type requirementVerdict int

// Ordered from the least a principal achieved against a requirement to the
// most; the best across the alternatives decides the cell.
const (
	requirementUnmatched requirementVerdict = iota
	requirementPartial
	requirementLacksRoles
	requirementSatisfied
)

// judgeRequirements returns the best a principal does against any of the
// alternatives, with the reason. A principal presents one credential, so a
// requirement naming two schemes that travel apart can never be met by it: the
// answer to such a request is not one the description decides.
func judgeRequirements(requirements []compile.Requirement, principal authorizationPrincipal) (requirementVerdict, string) {
	best, why := requirementUnmatched, "the credential is not one the operation accepts"
	for _, requirement := range requirements {
		verdict, reason := judgeRequirement(requirement, principal)
		if verdict > best {
			best, why = verdict, reason
		}
	}
	return best, why
}

func judgeRequirement(requirement compile.Requirement, principal authorizationPrincipal) (requirementVerdict, string) {
	carried, missingRoles := 0, []string(nil)
	var schemes []string
	for _, need := range requirement {
		schemes = append(schemes, need.Scheme.Name)
		if !carries(principal.Carrier, need.Scheme) {
			continue
		}
		carried++
		for _, role := range need.Roles {
			if !slices.Contains(principal.Roles, role) && !slices.Contains(missingRoles, role) {
				missingRoles = append(missingRoles, role)
			}
		}
	}
	switch {
	case carried == 0:
		return requirementUnmatched, "the credential is not one the operation accepts"
	case carried < len(requirement):
		return requirementPartial, "the operation requires " + strings.Join(schemes, " and ") +
			" together, and a principal presents one credential"
	case len(missingRoles) > 0:
		return requirementLacksRoles, "the credential lacks " + strings.Join(missingRoles, ", ") +
			" for " + strings.Join(schemes, " and ")
	default:
		return requirementSatisfied, "the credential satisfies " + strings.Join(schemes, " and ")
	}
}

// carries reports whether a credential travelling in this carrier is one a
// scheme accepts.
func carries(carrier credentialCarrier, scheme compile.SecurityScheme) bool {
	switch scheme.Type {
	case "http":
		return strings.EqualFold(carrier.Header, "Authorization") && scheme.Scheme != "" &&
			strings.EqualFold(carrier.Scheme, scheme.Scheme)
	case "oauth2", "openIdConnect":
		return strings.EqualFold(carrier.Header, "Authorization") && strings.EqualFold(carrier.Scheme, "bearer")
	case "apiKey":
		switch scheme.In {
		case "header":
			return carrier.Header != "" && strings.EqualFold(carrier.Header, scheme.Parameter)
		case "cookie":
			return carrier.Cookie != "" && carrier.Cookie == scheme.Parameter
		}
	}
	return false
}

// unsupportedScheme says why a scheme cannot be part of a planned cell, or "".
// The runner can put a credential in a header or a named cookie and take it
// off again; anything else could be on a request meant to be anonymous with
// nothing to stop it.
func unsupportedScheme(scheme compile.SecurityScheme) string {
	switch {
	case scheme.Type == "http" && scheme.Scheme != "",
		scheme.Type == "oauth2", scheme.Type == "openIdConnect",
		scheme.Type == "apiKey" && (scheme.In == "header" || scheme.In == "cookie") && scheme.Parameter != "":
		return ""
	case scheme.Type == "apiKey":
		return "scheme " + scheme.Name + " travels in the " + scheme.In + ", which vertrag cannot isolate"
	case scheme.Type == "":
		return "scheme " + scheme.Name + " is not defined in the description"
	default:
		return "scheme " + scheme.Name + " (" + scheme.Type + ") cannot be presented by vertrag"
	}
}

// authorizationCarriers are every carrier a credential can travel in on these
// operations and from these principals.
func authorizationCarriers(transactions []compile.Transaction, principals []authorizationPrincipal) runner.Carriers {
	var headers, cookies []string
	addHeader := func(name string) {
		if name != "" {
			headers = append(headers, name)
		}
	}
	addCookie := func(name string) {
		if name != "" {
			cookies = append(cookies, name)
		}
	}
	for _, transaction := range transactions {
		for _, scheme := range transaction.Security.Schemes() {
			switch {
			case scheme.Type == "http", scheme.Type == "oauth2", scheme.Type == "openIdConnect":
				addHeader("Authorization")
			case scheme.Type == "apiKey" && scheme.In == "header":
				addHeader(scheme.Parameter)
			case scheme.Type == "apiKey" && scheme.In == "cookie":
				addCookie(scheme.Parameter)
			}
		}
	}
	for _, principal := range principals {
		addHeader(principal.Carrier.Header)
		addCookie(principal.Carrier.Cookie)
	}
	// Header names compare without case, so one spelled two ways is one
	// carrier; sorting first makes which spelling is kept independent of the
	// order the transactions came in. Cookie names compare as written.
	slices.Sort(headers)
	slices.SortStableFunc(headers, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	headers = slices.CompactFunc(headers, strings.EqualFold)
	slices.Sort(cookies)
	return runner.Carriers{Headers: headers, Cookies: slices.Compact(cookies)}
}
