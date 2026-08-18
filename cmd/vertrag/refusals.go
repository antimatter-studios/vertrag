package main

import (
	"fmt"
	"strings"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
)

// refusals collects the operations that answered 401 or 403 to their
// documented request, and reports them in a way the reader can act on.
//
// The old line said only how many there were, and always gave the same
// advice: set `auth`. That advice is wrong twice over for the operation that
// GRANTS the credential. A login endpoint answers 401 to a generated body
// because it is checking credentials and the prober made some up — the
// endpoint working exactly as it should — and a reader whose `auth` block is
// already set and already working is told to set it. Two opposite situations,
// one message, no operation named, nothing to do about it.
//
// So: the login operation is recognised and reported as the correct
// behaviour it is, everything else is NAMED, and the advice is printed only
// when it applies.
type refusals struct {
	login     loginOperation
	locked    []string
	loginSeen bool
	authSet   bool
}

func newRefusals(settings config.Config) *refusals {
	return &refusals{
		login: loginOperation{
			method: strings.ToUpper(strings.TrimSpace(settings.Auth.Login.Method)),
			path:   strings.TrimSpace(settings.Auth.Login.Path),
		},
		authSet: settings.Auth.Configured(),
	}
}

// note records that this transaction's documented request was refused.
func (r *refusals) note(transaction compile.Transaction) {
	if r.login.matches(transaction) {
		r.noteLogin()
		return
	}
	r.locked = append(r.locked, transaction.Name)
}

// noteLogin records that the login operation was met, refused or not.
func (r *refusals) noteLogin() { r.loginSeen = true }

// isLogin reports whether this is the operation that grants the credential.
//
// Valid-input probing of it is meaningless whether or not its documented
// example works: the schema promises which requests are WELL FORMED, not
// which credentials exist, so a 401 to a generated username is the endpoint
// doing its job. It is the same exemption a path parameter's 404 already
// gets — a well-formed identifier that names nothing — for the same reason.
//
// The general rule the two share, worth stating because the next case will
// not look like either: generation can produce anything the caller must
// SHAPE, and nothing the caller must POSSESS. A credential, a lease, a
// one-time token, an idempotency key already spent, an id that names a real
// row — none can be drawn from a schema, and a refusal of an invented one
// says nothing about the contract. Where an operation turns on something
// possessed, valid-mode probing is skipped and invalid-mode probing is not:
// a malformed request is malformed whatever the caller holds, so a 5xx from
// one remains a finding worth having.
func (r *refusals) isLogin(transaction compile.Transaction) bool {
	if !r.login.matches(transaction) {
		return false
	}
	r.noteLogin()
	return true
}

// report prints what was refused, or nothing when nothing was.
func (r *refusals) report() {
	if r.loginSeen {
		// Precisely what happened, not a comfortable summary: the valid half
		// is skipped because a 401 to made-up credentials says nothing about
		// whether the operation accepts what its schema permits; the invalid
		// half still goes out, and a 5xx from a malformed login body would
		// still be a finding worth having.
		fmt.Printf("\n\nThe login operation was not probed with generated valid input: it checks\n" +
			"credentials, and generated ones are not credentials, so a 401 would say nothing.\n" +
			"Invalid input was still sent.")
	}
	if len(r.locked) == 0 {
		return
	}

	fmt.Printf("\n\n%d operation(s) answered 401 or 403 to the documented request, so little was\nlearned about them:", len(r.locked))
	for _, name := range r.locked {
		fmt.Printf("\n  %s", name)
	}
	if r.authSet {
		// The credential exists and was sent; these operations refused it
		// anyway, so "set auth" is not the advice — the credential is, or
		// its scope is, wrong for them.
		fmt.Printf("\nA credential was configured and sent, so these need a different one — a scope,\n" +
			"a role, or an `auth.except` entry if refusing is what they are meant to do.")
		return
	}
	fmt.Printf("\nSet `auth` in your vertrag.yml, or pass --header, to probe behind the credential.")
}

// loginOperation identifies the transaction that obtains the credential.
//
// vertrag already knows the login request — it makes it — so it can tell
// that operation apart from one that merely refused it.
type loginOperation struct {
	method string
	path   string
}

func (l loginOperation) matches(transaction compile.Transaction) bool {
	if l.path == "" {
		return false
	}
	// The compiled URI may carry a query string; the login path never does.
	uri := transaction.Request.URI
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		uri = uri[:i]
	}
	if uri != l.path {
		return false
	}
	method := l.method
	if method == "" {
		// auth.login defaults to POST, and so does this.
		method = "POST"
	}
	return strings.EqualFold(transaction.Request.Method, method)
}
