package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/antimatter-studios/vertrag/auth"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
)

// The authorization phase, executed: every cell the plan sends, sent as its
// principal through runner.SendAs and judged against what the plan says must
// come back. Nothing here decides an expectation — that is the planner's —
// and nothing here reports; a cell's outcome is an ordinary runner.Result,
// so the reporters that already render results can render these.

// authorizationOutcome is what became of one cell.
type authorizationOutcome struct {
	Cell authorizationCell

	// Result is the request as sent and the reply, judged: pass, fail or
	// error. A cell that was not sent is skip, with the reason in Unsent and
	// in Errors — never pass, because nobody checked.
	Result runner.Result
	Unsent string

	// Bypass marks a refusal the server did not make: the description
	// requires 401 or 403 and the server answered with success.
	Bypass bool

	// Replay is a command that repeats a failing request, with every
	// credential the phase sends replaced by <redacted>.
	Replay string
}

// authorizationCredentials logs each principal in, once, and checks that the
// credential it got travels where the plan said it would.
//
// The check has to come first. The plan's expectations follow from the
// carrier: an OAuth2 server that names a token_type other than Bearer, or a
// login whose header template is not what the plan read, hands back a
// credential the plan judged as something else. Sending it would turn the
// server's correct answer to an unexpected credential into a false verdict —
// at worst a bypass nobody made. So a principal whose credential arrives
// elsewhere is unsupported: every cell that would carry it stays unsent, with
// the reason. A principal that cannot log in at all ends the phase: there is
// no identity to test.
func authorizationCredentials(ctx context.Context, client *http.Client, endpoint string,
	configured []config.Principal, principals []authorizationPrincipal) (
	map[string]runner.Credential, map[string]string, error) {
	settings := make(map[string]config.Auth, len(configured))
	for _, principal := range configured {
		settings[principal.Name] = principal.Auth
	}

	credentials := make(map[string]runner.Credential, len(principals))
	unsupported := map[string]string{}
	for _, principal := range principals {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		configuredAuth, ok := settings[principal.Name]
		if !ok {
			return nil, nil, fmt.Errorf("principal %q is planned but not configured", principal.Name)
		}
		line := ""
		if configuredAuth.Configured() {
			// Registered as the run's own auth is: its secrets before the
			// exchange that uses them, and what came back as soon as it does,
			// so no report of any principal's request can carry either.
			registerAuthSecrets(configuredAuth)
			obtained, err := auth.Obtain(ctx, client, endpoint, configuredAuth)
			if err != nil {
				return nil, nil, fmt.Errorf("principal %q: obtaining its credential: %w", principal.Name, err)
			}
			reporter.RegisterCredential(obtained)
			line = obtained
		}
		observed, err := observedCarrier(line)
		if err != nil {
			unsupported[principal.Name] = err.Error()
		} else if !sameCarrier(observed, principal.Carrier) {
			unsupported[principal.Name] = fmt.Sprintf(
				"its credential arrived as %s, not as %s as planned; vertrag will not guess who it identifies",
				describeCarrier(observed), describeCarrier(principal.Carrier))
		}
		credentials[principal.Name] = runner.Credential{
			Header:      line,
			Except:      maps.Clone(principal.Except),
			LoginMethod: principal.LoginMethod,
			LoginPath:   principal.LoginPath,
		}
	}
	return credentials, unsupported, nil
}

// observedCarrier is where an obtained `Name: value` line puts the credential,
// read the way carrierOf reads a configured one.
func observedCarrier(line string) (credentialCarrier, error) {
	if line == "" {
		return credentialCarrier{}, nil
	}
	name, value, ok := strings.Cut(line, ":")
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if !ok || name == "" {
		return credentialCarrier{}, errors.New("its credential is not a `Name: value` header line")
	}
	switch {
	case strings.EqualFold(name, "Cookie"):
		cookies := splitCookieLine(value)
		if len(cookies) != 1 {
			return credentialCarrier{}, fmt.Errorf("its credential arrived as %d cookies, not one", len(cookies))
		}
		return credentialCarrier{Cookie: cookies[0]}, nil
	case strings.EqualFold(name, "Authorization"):
		scheme, _, _ := strings.Cut(value, " ")
		return credentialCarrier{Header: name, Scheme: strings.ToLower(scheme)}, nil
	default:
		return credentialCarrier{Header: name}, nil
	}
}

// sameCarrier compares header names and schemes without case, as HTTP does,
// and cookie names as written.
func sameCarrier(a, b credentialCarrier) bool {
	return strings.EqualFold(a.Header, b.Header) && strings.EqualFold(a.Scheme, b.Scheme) && a.Cookie == b.Cookie
}

func describeCarrier(carrier credentialCarrier) string {
	switch {
	case carrier.Cookie != "":
		return fmt.Sprintf("the cookie %q", carrier.Cookie)
	case carrier.Scheme != "":
		return fmt.Sprintf("%s %s", carrier.Header, carrier.Scheme)
	case carrier.Header != "":
		return fmt.Sprintf("the header %s", carrier.Header)
	}
	return "no credential"
}

// executeAuthorization sends the plan's cells in the plan's order and judges
// each. It stops sending when ctx is done; every cell it did not reach is
// reported unsent, so a cancelled run never reads as a passing one.
func executeAuthorization(ctx context.Context, engine *runner.Runner, plan authorizationPlan,
	credentials map[string]runner.Credential, unsupported map[string]string) []authorizationOutcome {
	// Every carrier is redacted by name in every report of these results, not
	// only the names reporters know by default: a cell's Request holds the
	// credential it was sent with, and an X-Tenant-Key is one too.
	for _, name := range plan.Carriers.Headers {
		reporter.AddRedactedHeader(name)
	}
	outcomes := make([]authorizationOutcome, 0, len(plan.Cells))
	for _, cell := range plan.Cells {
		outcomes = append(outcomes, executeCell(ctx, engine, plan.Carriers, cell,
			credentials[cell.Principal], unsupported[cell.Principal]))
	}
	return outcomes
}

func executeCell(ctx context.Context, engine *runner.Runner, carriers runner.Carriers, cell authorizationCell,
	credential runner.Credential, unsupported string) authorizationOutcome {
	name := cell.Operation + " as " + cell.Principal
	unsent := func(reason string) authorizationOutcome {
		return authorizationOutcome{
			Cell:   cell,
			Unsent: reason,
			Result: runner.Result{Name: name, Status: runner.StatusSkip, Errors: []string{"not sent: " + reason}},
		}
	}
	switch {
	case cell.Unsent != "":
		return unsent(cell.Unsent)
	case cell.Expect == expectUndetermined || cell.Status == "":
		return unsent("the description does not decide this cell")
	case cell.Credential && unsupported != "":
		return unsent(unsupported)
	case ctx.Err() != nil:
		return unsent("the run was cancelled before this cell was sent")
	}

	sent, err := engine.SendAs(ctx, name, cell.Transaction, credential, carriers)
	switch {
	case errors.Is(err, runner.ErrSkippedByHook):
		return unsent("a hook skipped it")
	case errors.Is(err, runner.ErrCredentialChangedByHook):
		return unsent("a hook changed the credential, so the request no longer said who sent it")
	case err != nil && ctx.Err() != nil:
		return unsent("the run was cancelled while this cell was being sent")
	case err != nil:
		sent.Name, sent.Status = name, runner.StatusError
		sent.Errors = append(sent.Errors, err.Error())
		return authorizationOutcome{Cell: cell, Result: sent}
	}

	outcome := judgeAuthorization(engine, cell, sent)
	if outcome.Result.Status == runner.StatusFail {
		outcome.Replay = authorizationReplay(sent.Request, carriers)
	}
	return outcome
}

// judgeAuthorization compares a reply with the cell's expectation. Success is
// judged as any documented success is, body and all; a refusal must be the
// exact one the description requires, and success in its place is a bypass.
func judgeAuthorization(engine *runner.Runner, cell authorizationCell, sent runner.Result) authorizationOutcome {
	status := sent.Actual.StatusCode
	if cell.Expect == expectAllowed {
		judged := engine.JudgeAs(cell.Transaction, sent)
		if strings.HasPrefix(status, "5") {
			judged.Errors = append([]string{fmt.Sprintf("the server failed with %s where %s was documented for %s",
				status, cell.Status, cell.Principal)}, judged.Errors...)
		}
		return authorizationOutcome{Cell: cell, Result: judged}
	}

	outcome := authorizationOutcome{Cell: cell, Result: sent}
	outcome.Result.Status = runner.StatusPass
	switch {
	case status == cell.Status:
		return outcome
	case strings.HasPrefix(status, "2"):
		outcome.Bypass = true
		outcome.Result.Errors = []string{fmt.Sprintf(
			"authorization bypass: %s answered %s for %s where the description requires %s (%s)",
			cell.Operation, status, cell.Principal, cell.Status, cell.Why)}
	case strings.HasPrefix(status, "5"):
		outcome.Result.Errors = []string{fmt.Sprintf("the server failed with %s where it should have refused %s with %s",
			status, cell.Principal, cell.Status)}
	default:
		outcome.Result.Errors = []string{fmt.Sprintf("refused %s with %s where the description requires %s (%s)",
			cell.Principal, status, cell.Status, cell.Why)}
	}
	outcome.Result.Status = runner.StatusFail
	return outcome
}

// authorizationReplay is the curl line that repeats a request, with the
// value of every carrier the phase sends redacted. reporter.Curl already
// redacts the usual credential headers and every cookie value; a carrier the
// description names for itself — an X-Tenant-Key — is redacted here, so that
// a finding never hands its reader somebody's credential.
func authorizationReplay(request runner.Request, carriers runner.Carriers) string {
	headers := make(map[string]string, len(request.Headers))
	for name, value := range request.Headers {
		switch {
		case slices.ContainsFunc(carriers.Headers, func(carrier string) bool { return strings.EqualFold(carrier, name) }):
			value = reporter.Redacted
		case strings.EqualFold(name, "Cookie"):
			value = redactCarriedCookies(value, carriers.Cookies)
		}
		headers[name] = value
	}
	request.Headers = headers
	return reporter.Curl(request)
}

// redactCarriedCookies redacts the value of every cookie that carries a
// credential, whether or not the run sanitizes reports: --no-sanitize is for
// reading one's own credential, and a finding is written for somebody else.
func redactCarriedCookies(line string, carried []string) string {
	pairs := strings.Split(line, ";")
	for i, pair := range pairs {
		name, _, _ := strings.Cut(strings.TrimSpace(pair), "=")
		if slices.Contains(carried, name) {
			pairs[i] = name + "=" + reporter.Redacted
		}
		pairs[i] = strings.TrimSpace(pairs[i])
	}
	return strings.Join(pairs, "; ")
}
