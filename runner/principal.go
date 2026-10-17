package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/antimatter-studios/vertrag/compile"
)

// Carriers are where a credential can travel: the header names, and the
// cookie names inside a Cookie header. The authorization phase gives the
// runner every carrier the description's schemes declare, so that a request
// sent as one principal carries that principal's credential and nobody
// else's.
type Carriers struct {
	Headers []string
	Cookies []string
}

// ErrCredentialChangedByHook reports that a hook set, replaced or removed a
// credential on a request the authorization phase was sending as a
// particular principal. Like ErrChangedByHook it is neither a finding nor a
// transport failure: the request no longer said who it came from, so what
// came back says nothing about the principal it was meant to test.
var ErrCredentialChangedByHook = errors.New("a hook changed the credential")

// SendAs sends a documented transaction as a principal: with the credential
// given — or none, for a principal that holds none — in place of the run's
// own, and with any other credential the run would have attached taken off.
//
// That last part is the one that matters. A run can carry a credential in
// three places besides `auth`: a run-wide `--header 'Authorization: …'`,
// which is the advice vertrag itself prints for a missing one; a header rule
// in the configuration; and the description's own example, which may document
// a cookie parameter that happens to be the session. Left in place, any of
// them goes out on the request meant to be anonymous, the server quite
// rightly answers it, and the phase reports a bypass the tester made.
//
// Status-conditional headers are not attached, for the reason probes do not
// get them: such a header tells a mock which documented answer to give, and
// a request whose expected answer is a refusal must not be told to succeed.
//
// The before hooks run, as they do for every probe, because a hook may be
// what holds a dangerous field at a safe value. A hook that changes a
// credential ends the request with ErrCredentialChangedByHook, unsent.
//
// The Result carries the request as sent, the documented expectation, the
// reply and the timings. Its Status is left empty: whether the reply is right
// depends on who was asking, which the caller knows and the runner does not.
func (r *Runner) SendAs(ctx context.Context, name string, source compile.Transaction, credential Credential,
	carriers Carriers) (Result, error) {
	started := time.Now()
	// The transaction keeps its own name, because that is what a hook
	// addresses it by; only the result is named for the principal.
	transaction := newTransaction(source, r.Endpoint, r.headersCarrying(source, Credential{}, false))
	carriers.strip(transaction.Request.Headers)
	if presented := credentialFor(source, credential); presented != "" {
		applyHeaderLine(transaction.Request.Headers, presented)
	}

	before := carriers.collect(transaction.Request.Headers)
	if r.Hooks != nil {
		if err := r.Hooks.BeforeEach(transaction); err != nil {
			return Result{}, fmt.Errorf("before hook: %w", err)
		}
		if transaction.Skip {
			return Result{}, ErrSkippedByHook
		}
		if transaction.Fail != "" {
			return Result{}, fmt.Errorf("failed by hook: %s", transaction.Fail)
		}
		if carriers.collect(transaction.Request.Headers) != before {
			return Result{}, ErrCredentialChangedByHook
		}
	}

	reply, exchange, err := r.send(ctx, transaction)
	result := Result{
		Name:         name,
		Request:      transaction.sentRequest(),
		Expected:     transaction.Expected,
		Actual:       reply,
		Duration:     time.Since(started),
		ResponseTime: exchange,
		Started:      started,
	}
	return result, err
}

// credentialFor is the header line a principal presents on this transaction:
// its credential, unless excluded by auth.except or this is the operation
// that granted it — see Credential.LoginPath.
func credentialFor(source compile.Transaction, credential Credential) string {
	if credential.Header == "" || credential.Except[source.Name] || credential.grantedBy(source) {
		return ""
	}
	return credential.Header
}

// applyHeaderLine sets one `Name: value` line on a header map, merging a
// Cookie line into the cookies already there as every other request does.
func applyHeaderLine(headers map[string]string, line string) {
	name, value, ok := strings.Cut(line, ":")
	if !ok {
		return
	}
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if strings.EqualFold(name, "Cookie") {
		addCookies(headers, name, value)
		return
	}
	for existing := range headers {
		if strings.EqualFold(existing, name) {
			delete(headers, existing)
		}
	}
	headers[name] = value
}

// strip takes every credential carrier off a request's headers: the headers
// outright, and the named cookies out of the Cookie line, leaving the cookies
// that are parameters of the operation where they were.
func (c Carriers) strip(headers map[string]string) {
	for existing, value := range headers {
		if c.carriesHeader(existing) {
			delete(headers, existing)
			continue
		}
		if !strings.EqualFold(existing, "Cookie") || len(c.Cookies) == 0 {
			continue
		}
		var kept []string
		for _, pair := range splitCookies(value) {
			if !c.carriesCookie(cookieName(pair)) {
				kept = append(kept, pair)
			}
		}
		if len(kept) == 0 {
			delete(headers, existing)
			continue
		}
		headers[existing] = strings.Join(kept, "; ")
	}
}

// collect writes down every credential a request carries, in a fixed order,
// so that two readings can be compared to tell whether something changed one.
func (c Carriers) collect(headers map[string]string) string {
	var lines []string
	for existing, value := range headers {
		if c.carriesHeader(existing) {
			lines = append(lines, strings.ToLower(existing)+": "+value)
			continue
		}
		if strings.EqualFold(existing, "Cookie") {
			for _, pair := range splitCookies(value) {
				if c.carriesCookie(cookieName(pair)) {
					lines = append(lines, "cookie "+pair)
				}
			}
		}
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

func (c Carriers) carriesHeader(name string) bool {
	for _, carrier := range c.Headers {
		if strings.EqualFold(carrier, name) {
			return true
		}
	}
	return false
}

// carriesCookie compares as written: cookie names are case-sensitive, unlike
// header names.
func (c Carriers) carriesCookie(name string) bool {
	for _, carrier := range c.Cookies {
		if carrier == name {
			return true
		}
	}
	return false
}
