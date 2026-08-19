// Package runner executes compiled transactions against a live server.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/validate"
)

// Status is the outcome of one transaction.
type Status string

const (
	// StatusPass means the response matched what the description promised.
	StatusPass Status = "pass"
	// StatusFail means it did not.
	StatusFail Status = "fail"
	// StatusSkip means a hook took the transaction out of the run.
	StatusSkip Status = "skip"
	// StatusError means the request could not be made at all — the server was
	// unreachable, the URL was unusable. This is distinct from a failure: the
	// API was never asked, so nothing was learned about it.
	StatusError Status = "error"
)

// Result is one executed transaction.
type Result struct {
	Name     string
	Status   Status
	Request  Request
	Expected validate.Message
	Actual   validate.Message
	// Errors explains a failure or an error, whichever occurred.
	Errors []string
	// Beyond holds failures from checks Dredd does not make. They are kept
	// apart so a report can say so: a project upgrading from Dredd meets these
	// for the first time, and an unexplained new failure reads as a regression
	// rather than as a contract violation that was going unnoticed.
	Beyond     []string
	Validation validate.Result
	Duration   time.Duration

	// ResponseTime is how long the server took: the request going out, the
	// response coming back, its body read. Duration is the whole transaction
	// and answers "how long did this cost me"; this answers "how long did the
	// server take", and only the second of those is a fact about the API.
	//
	// They are separate because the bound in `checks.max-response-time` is
	// judged against this one. Judged against Duration, a run pacing itself
	// with `transport.delay` to spare a throttled server spent that courtesy
	// against the bound and reported the server as slow when the server had
	// answered at once — so the two settings could not be used together at all.
	// A retry's backoff and the hooks are excluded for the same reason: none of
	// them is time the server spent.
	//
	// Zero when no response arrived: a transaction a hook skipped, one the plan
	// never reached, a request that failed before it went out.
	ResponseTime time.Duration

	// Started is when the transaction began. A cassette needs a timestamp per
	// exchange, and a HAR viewer draws its waterfall from this one — every
	// entry stamped with the moment the report was written renders as a single
	// stacked bar, which is a picture of nothing.
	//
	// It is zero for a result assembled rather than run: a transaction the plan
	// never reached, or one of the results the probing commands build by hand.
	// A reporter that needs a time for those supplies its own.
	Started time.Time
}

// Request is what was sent, after hooks had their say.
type Request struct {
	Method  string
	URI     string
	Headers map[string]string
	Body    string

	// URL is the absolute address the request went to. URI is relative and
	// only meaningful next to the endpoint, which a report does not otherwise
	// repeat — a reproduction line needs the whole address.
	URL string
}

// Runner sends transactions to a server and judges the responses.
type Runner struct {
	Endpoint string
	Client   *http.Client

	// Transport is what the client was built from, kept for the retry and
	// pacing decisions send makes per request.
	Transport Transport

	// paceMu guards lastSend, which is when the next request may go out. It is
	// shared state on purpose: a delay bounds the run's request stream, and
	// under workers that can only be enforced across all of them.
	paceMu   sync.Mutex
	lastSend time.Time

	// Header lines, as `Name: value`, added to every request. They come from
	// the command line and are how a run supplies credentials the description
	// does not mention.
	ExtraHeaders []string

	// Hooks, when set, is given each transaction before and after it runs.
	Hooks Hooks

	// Checks selects the checks Dredd does not make.
	Checks Checks

	// Plan, when set, decides the order transactions run in and may fill in
	// values from earlier responses. Nil means document order and no rewriting,
	// which is what `vertrag run` does unless asked otherwise.
	Plan Plan

	// Auth is the credential obtained for this run, sent on every request but
	// the ones that must go without.
	Auth Credential

	// Skip takes transactions out of the run before anything is sent, keyed by
	// name and carrying the reason to report. A hook could do the same, but a
	// skip list is where a suite's debt collects and it is worth being able to
	// read the whole of it in one place.
	Skip map[string]string

	// ConditionalHeaders are added to the transactions they match.
	ConditionalHeaders []ConditionalHeader

	// Workers is how many transactions to send at once. Zero or one is
	// sequential, which is the default and what every run did before this
	// existed.
	//
	// It is ignored for a run with a plan (`--sequence`) or hooks: both are
	// ordering contracts, and a step that takes its values from another's
	// response cannot overlap it. The report is unaffected either way —
	// results are collected by original position and printed in document
	// order — so a parallel run and a sequential one of the same suite
	// produce the same report.
	Workers int

	// MaxFailures stops sending once this many transactions have failed or
	// errored. Zero means never stop. What has not run is reported as skipped,
	// with the reason, so the report still names every transaction and its
	// totals still add up — a truncated report reads as a shorter suite.
	MaxFailures int
}

// ConditionalHeader is a header added only to the transactions it matches.
//
// The conditions are properties of the transaction the description already
// fixed — the status it expects, the method it uses — and nothing about the
// response, because these are decided before the request is sent. Anything
// needing to look at what came back is a hook.
type ConditionalHeader struct {
	Name  string
	Value string

	// Status matches the response status the transaction expects. Empty matches
	// every transaction.
	//
	// This is the condition worth having. A mock told which failure to simulate
	// is how a suite reaches the error responses its description promises, and
	// which failure to ask for follows from which response is expected.
	Status string

	// Method matches the request method. Empty matches every transaction.
	Method string
}

// matches reports whether the header applies to a transaction.
func (c ConditionalHeader) matches(transaction compile.Transaction) bool {
	if c.Status != "" && c.Status != strings.TrimSpace(transaction.Response.Status) {
		return false
	}
	if c.Method != "" && !strings.EqualFold(c.Method, transaction.Request.Method) {
		return false
	}
	return true
}

// Credential is a header carrying an obtained credential, and the transactions
// it must be withheld from.
type Credential struct {
	// Header is a `Name: value` line, empty when the run is unauthenticated.
	Header string

	// Except names transactions to send without the credential. A login
	// endpoint's own 401 case is untestable while holding a valid one.
	Except map[string]bool

	// LoginMethod and LoginPath identify the operation that GRANTED the
	// credential, which never receives it.
	//
	// Sending a freshly minted cookie back to the request that minted it is
	// at best noise and at worst a different exchange from the one the
	// description documents: a server may take the cookie as "already
	// authenticated" and answer a login it never performed. It was harmless
	// on the suite that found it — the server ignored the cookie — but the
	// suite had to spell the exclusion out in `except`, and it should not
	// have to. This is definitional, not configuration.
	LoginMethod string
	LoginPath   string
}

// GrantedBy reports whether this transaction is the one that obtained the
// credential. Exported so a caller can check the exclusion engaged at all —
// see the warning in applyConfiguredRules and the reason it exists.
func (c Credential) GrantedBy(transaction compile.Transaction) bool { return c.grantedBy(transaction) }

// grantedBy reports whether this transaction is the one that obtained the
// credential.
func (c Credential) grantedBy(transaction compile.Transaction) bool {
	if c.LoginPath == "" {
		return false
	}
	uri := transaction.Request.URI
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		uri = uri[:i]
	}
	if uri != c.LoginPath {
		return false
	}
	method := c.LoginMethod
	if method == "" {
		method = http.MethodPost
	}
	return strings.EqualFold(transaction.Request.Method, method)
}

// configuredSkipReason labels a skip as the configuration's doing.
//
// Without the label a reader cannot tell a transaction the config removed from
// one a hook removed, and those are fixed in different files.
func configuredSkipReason(reason string) string {
	if reason == "" {
		return "skipped by configuration"
	}
	return "skipped by configuration: " + reason
}

// headersFor returns the extra headers for one transaction: the run-wide ones,
// plus the credential unless this transaction is one that must go without.
func (r *Runner) headersFor(transaction compile.Transaction) []string {
	return r.headers(transaction, true, true)
}

// headers builds a transaction's extra headers, with or without the
// credential. The explicit form exists because the ignored-auth check must
// send the same request bare: it used to copy the Runner to do that, which
// `go vet` rightly refused once the Runner held a mutex — and copying a
// runner to change one decision was the wrong shape regardless.
func (r *Runner) headers(transaction compile.Transaction, withCredential, staged bool) []string {
	authenticated := withCredential &&
		r.Auth.Header != "" &&
		!r.Auth.Except[transaction.Name] &&
		!r.Auth.grantedBy(transaction)

	var conditional []string
	for _, header := range r.ConditionalHeaders {
		// A header conditioned on the STATUS is a staging instruction: it says
		// "when the documented answer is this, send that", and its purpose is
		// to make a mock produce the documented answer. A generated request has
		// no documented answer — that is the point of generating it — so the
		// condition is about a request nobody sent.
		//
		// Attaching it anyway told the server which status to return and then
		// reported the server for returning it. Against a real project that
		// manufactured the most alarming sentence this tool can emit: "the
		// server returned 200 for a login body with no password", on an API
		// whose login endpoint answers 401 to exactly that request when asked
		// without the header. The staging header was doing what it was written
		// to do; the probe was the thing that had no business carrying it.
		//
		// Method-conditional headers are kept: a probe does not change the
		// method, so that condition still means what it said.
		if !staged && header.Status != "" {
			continue
		}
		if header.matches(transaction) {
			conditional = append(conditional, header.Name+": "+header.Value)
		}
	}

	if !authenticated && len(conditional) == 0 {
		return r.ExtraHeaders
	}
	// Copied rather than appended to in place. `append(r.ExtraHeaders, …)` would
	// write into ExtraHeaders' backing array whenever it has spare capacity, and
	// that array is shared by every transaction. Nothing visible goes wrong
	// today — every caller writes the same credential into the same slot — so
	// this is not a bug being fixed but a dependence on the slice's capacity
	// being removed, for the price of one allocation per transaction.
	headers := make([]string, 0, len(r.ExtraHeaders)+len(conditional)+1)
	headers = append(headers, r.ExtraHeaders...)
	// Conditional headers come after the run-wide ones so a rule aimed at some
	// transactions can override a value set for all of them, which is the only
	// order in which stating both is useful.
	headers = append(headers, conditional...)
	if authenticated {
		headers = append(headers, r.Auth.Header)
	}
	return headers
}

// Hooks is the part of the hook system the runner needs.
//
// It is an interface so a run without hooks needs no worker process, and so the
// runner can be tested without one.
type Hooks interface {
	BeforeAll(transactions []*Transaction) error
	BeforeEach(transaction *Transaction) error
	BeforeEachValidation(transaction *Transaction) error
	AfterEach(transaction *Transaction) error
	AfterAll(transactions []*Transaction) error
}

// New returns a runner with a client suited to testing.
func New(endpoint string) *Runner {
	r, _ := NewWithTransport(endpoint, Transport{})
	return r
}

// NewWithTransport is New with the network knobs a CI job turns. It errors
// only when the transport itself cannot be built — an unreadable CA bundle,
// an unparsable proxy URL — which is worth stopping for before any request.
func NewWithTransport(endpoint string, transport Transport) (*Runner, error) {
	client, err := transport.client()
	if err != nil {
		return nil, err
	}
	return &Runner{
		Endpoint: strings.TrimRight(endpoint, "/"),
		// On by default: a contract violation is worth reporting even when the
		// tool a project came from would have missed it.
		Checks:    Checks{ServerError: true, ContentType: true},
		Client:    client,
		Transport: transport,
	}, nil
}

// Send performs one transaction's request and returns what came back, without
// validating it, running hooks, or recording a result.
//
// It exists for generation, which sends many bodies through one operation and
// judges them by status alone. Reusing this rather than a fresh HTTP client
// keeps the URL resolution, extra headers and redirect policy identical to a
// normal run, so a finding is reproducible by `vertrag run`.
func (r *Runner) Send(ctx context.Context, source compile.Transaction) (validate.Message, error) {
	transaction := newTransaction(source, r.Endpoint, r.headersFor(source))

	// Hooks run here too, and it took a safety review to notice they did not.
	//
	// This is the path generation sends through — fuzz, coverage, stateful —
	// and it was written as "just send this and give me the reply", so hooks
	// were simply never wired into it. Nobody decided that probing should
	// bypass them. The consequence is the opposite of what anyone would
	// choose: a hook exists precisely to pin the value a generator must not
	// touch, and the moment that matters is when a generator is drawing
	// values. A project whose hook forces `dry_run: true` had it honoured on
	// the requests its description documents and ignored on the thousands of
	// generated ones.
	//
	// Only beforeEach and its named hooks run. There is no response yet, so
	// the validation hooks have nothing to act on, and a probe is judged by
	// its status rather than by validation. A hook that skips or fails a
	// transaction is honoured: skip means do not send this probe.
	if r.Hooks != nil {
		if err := r.Hooks.BeforeEach(transaction); err != nil {
			return validate.Message{}, fmt.Errorf("before hook: %w", err)
		}
		if transaction.Skip {
			return validate.Message{}, ErrSkippedByHook
		}
		if transaction.Fail != "" {
			return validate.Message{}, fmt.Errorf("failed by hook: %s", transaction.Fail)
		}
	}

	reply, _, err := r.send(ctx, transaction)
	return reply, err
}

// sendPrepared is the half of Send that follows preparation, so Send and
// SendGenerated differ only in the headers they attach and cannot drift in
// anything else — hooks, skip handling and the send itself are shared.
func (r *Runner) sendPrepared(ctx context.Context, transaction *Transaction) (validate.Message, error) {
	drawn := transaction.Request.Body

	if r.Hooks != nil {
		if err := r.Hooks.BeforeEach(transaction); err != nil {
			return validate.Message{}, fmt.Errorf("before hook: %w", err)
		}
		if transaction.Skip {
			return validate.Message{}, ErrSkippedByHook
		}
		if transaction.Fail != "" {
			return validate.Message{}, fmt.Errorf("failed by hook: %s", transaction.Fail)
		}
	}

	// A hook that REPLACED the generated body has ended the probe, and the
	// probe has to say so rather than judge what came back.
	//
	// Hooks run on generated requests deliberately: a hook may be the thing
	// holding a dangerous field at a safe value, and the moment that matters
	// is when a generator is drawing values. The other direction was not
	// thought through. A login hook that fills in credentials — which is what
	// nearly every hook file inherited from Dredd does — overwrites the body
	// the generator drew, the server quite correctly answers 200 to the valid
	// credentials it was handed, and the probe reports that the server
	// accepted a body its schema forbids while DISPLAYING the body that was
	// never sent.
	//
	// That produced a false security finding against a real API — "returned
	// 200 for a login body with no password" — which its owners escalated and
	// then could not reproduce, because the request they were shown was not
	// the request that went out.
	//
	// Only the body is compared. A hook adding a header is ordinary and
	// changes nothing about what the body tests.
	if transaction.Request.Body != drawn {
		return validate.Message{}, ErrChangedByHook
	}

	reply, _, err := r.send(ctx, transaction)
	return reply, err
}

// ErrChangedByHook reports that a hook rewrote the body of a generated request,
// so what was sent is not what was drawn. Like ErrSkippedByHook it is neither a
// finding nor a transport failure: the caller counts it and says so, because a
// probe that tested the hook's value proves nothing about the generator's.
var ErrChangedByHook = errors.New("the generated body was replaced by a hook")

// ErrSkippedByHook reports that a hook took a generated request out of the
// run before it was sent. It is not a finding and not a transport failure:
// the caller counts it and says so, rather than reporting the server for
// something that never reached it.
var ErrSkippedByHook = errors.New("skipped by a hook")

// PrepareGenerated is Prepare for a request whose content was generated rather
// than documented: the same thing without the status-conditional headers, which
// stage a documented answer that a generated request is not asking for. See
// headers.
func (r *Runner) PrepareGenerated(source compile.Transaction) *Transaction {
	return newTransaction(source, r.Endpoint, r.headers(source, true, false))
}

// SendGenerated is Send for a generated request. Every probing phase sends
// through it, so that a staging header cannot decide what a probe is judged by.
func (r *Runner) SendGenerated(ctx context.Context, source compile.Transaction) (validate.Message, error) {
	return r.sendPrepared(ctx, r.PrepareGenerated(source))
}

// Prepare builds the transaction that would be sent for a source, with the
// endpoint resolved and the run's headers and credential attached.
//
// It is exported for the stateful phase, which sends a chain of transactions
// with values threaded between them and so needs the prepared form before it
// goes out — the same form hooks and the sequencer already act on.
func (r *Runner) Prepare(source compile.Transaction) *Transaction {
	return newTransaction(source, r.Endpoint, r.headersFor(source))
}

// Deliver sends a prepared transaction and returns what came back, without
// validating it, running hooks, or recording a result. Send is the same thing
// from an unprepared source; this is for a caller that had to prepare first.
func (r *Runner) Deliver(ctx context.Context, transaction *Transaction) (validate.Message, error) {
	reply, _, err := r.send(ctx, transaction)
	return reply, err
}

// SentRequest is the request as it actually went out — see sentRequest.
func (t *Transaction) SentRequest() Request { return t.sentRequest() }

// OperationID is how the description names this transaction's operation, or
// "" when it gave none. Hooks select transactions by it.
func (t *Transaction) OperationID() string { return t.source.OperationID }

// Run executes every transaction in order and returns the results.
//
// Order is the document's order, which is what makes a description that creates
// a resource before reading it work.
func (r *Runner) Run(ctx context.Context, transactions []compile.Transaction) ([]Result, error) {
	prepared := make([]*Transaction, 0, len(transactions))
	for i := range transactions {
		prepared = append(prepared, newTransaction(transactions[i], r.Endpoint, r.headersFor(transactions[i])))
	}

	if r.Hooks != nil {
		if err := r.Hooks.BeforeAll(prepared); err != nil {
			return nil, fmt.Errorf("beforeAll hook: %w", err)
		}
	}

	// Results are collected against their original positions and reported in
	// the document's order however the plan chose to run them. A report that
	// reordered itself would be unreadable against the description, and a diff
	// between two runs would be noise.
	completed := map[int]Result{}
	order := r.sequence(len(prepared))

	if r.Workers > 1 && r.Plan == nil {
		r.runConcurrently(ctx, prepared, order, completed)
	} else {
		r.runSequentially(ctx, prepared, order, completed)
	}

	results := make([]Result, 0, len(prepared))
	for i := range prepared {
		results = append(results, completed[i])
	}

	if r.Hooks != nil {
		if err := r.Hooks.AfterAll(prepared); err != nil {
			return results, fmt.Errorf("afterAll hook: %w", err)
		}
	}

	return results, nil
}

// runSequentially is the original loop, and the only one used when a plan
// orders the run: a sequenced run's steps take their values from each other's
// responses, so they cannot overlap by construction.
func (r *Runner) runSequentially(ctx context.Context, prepared []*Transaction, order []int, completed map[int]Result) {
	failures := 0
	for _, index := range order {
		transaction := prepared[index]

		if skipped, reason := r.excluded(transaction, failures); skipped {
			completed[index] = transaction.skippedResult(reason)
			continue
		}
		if r.Plan != nil {
			if reason, ok := r.Plan.Prepare(index, transaction, completed); !ok {
				completed[index] = transaction.skippedResult(reason)
				continue
			}
		}

		result := r.runOne(ctx, transaction)
		// Asked after the transaction's own verdict, and only of one that
		// succeeded: the question is whether the credential mattered, which
		// a failed request cannot answer.
		if finding, open := r.checkIgnoredAuth(ctx, transaction.source, result); open {
			result.Beyond = append(result.Beyond, finding)
			result.Status = StatusFail
		}
		if r.Plan != nil {
			r.Plan.Record(index, transaction, result)
		}
		completed[index] = result
		if result.Status == StatusFail || result.Status == StatusError {
			failures++
		}
	}
}

// runConcurrently sends up to Workers transactions at once.
//
// What it does NOT do is reorder or reinterpret anything: results are still
// collected against their original positions and reported in the document's
// order, so two runs of the same suite produce the same report whatever the
// worker count. That is the whole reason the concurrency is here rather than
// in a wrapper — a parallel run whose report shuffled itself would be
// unreadable against the description and undiffable against yesterday.
//
// It is refused for a planned (`--sequence`) run, and for hooks, by the
// caller: both are ordering contracts that concurrency would break rather
// than speed up.
//
// The failure budget is honoured approximately and deliberately so: workers
// already in flight when the budget is reached finish, because cancelling a
// request that is already on the wire tells the reader less than letting it
// answer. What has not STARTED is skipped with the reason, as sequentially.
func (r *Runner) runConcurrently(ctx context.Context, prepared []*Transaction, order []int, completed map[int]Result) {
	var mu sync.Mutex
	var failures int
	var stopped bool

	// A worker takes the next index rather than a fixed share, so one slow
	// transaction cannot leave a worker idle while another has a queue.
	queue := make(chan int)
	var wait sync.WaitGroup

	workers := r.Workers
	if workers > len(order) {
		workers = len(order)
	}
	for w := 0; w < workers; w++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range queue {
				transaction := prepared[index]

				mu.Lock()
				seen, halted := failures, stopped
				mu.Unlock()

				if halted {
					mu.Lock()
					completed[index] = transaction.skippedResult(
						fmt.Sprintf("not run: stopped after %d failure(s)", seen))
					mu.Unlock()
					continue
				}
				if skipped, reason := r.excluded(transaction, seen); skipped {
					mu.Lock()
					completed[index] = transaction.skippedResult(reason)
					mu.Unlock()
					continue
				}

				result := r.runOne(ctx, transaction)
				if finding, open := r.checkIgnoredAuth(ctx, transaction.source, result); open {
					result.Beyond = append(result.Beyond, finding)
					result.Status = StatusFail
				}

				mu.Lock()
				completed[index] = result
				if result.Status == StatusFail || result.Status == StatusError {
					failures++
					if r.MaxFailures > 0 && failures >= r.MaxFailures {
						stopped = true
					}
				}
				mu.Unlock()
			}
		}()
	}

	for _, index := range order {
		select {
		case queue <- index:
		case <-ctx.Done():
			// A cancelled run stops handing out work; what was not handed out
			// is filled in below.
			close(queue)
			wait.Wait()
			r.fillUnrun(prepared, order, completed, "not run: the run was cancelled")
			return
		}
	}
	close(queue)
	wait.Wait()
}

// excluded reports whether a transaction should not be sent, and why: the
// configuration took it out, or the failure budget is spent.
func (r *Runner) excluded(transaction *Transaction, failures int) (bool, string) {
	// Checked before the plan and before hooks: a transaction the config takes
	// out of the run should not be prepared, sequenced, or offered to a hook
	// that might act on it.
	if reason, skipped := r.Skip[transaction.Name]; skipped {
		return true, configuredSkipReason(reason)
	}
	// Past the failure budget nothing more is sent. Skipped rather than
	// dropped: a pipeline that stops early still gets a report naming every
	// transaction, and can tell "did not run" from "passed".
	if r.MaxFailures > 0 && failures >= r.MaxFailures {
		return true, fmt.Sprintf("not run: stopped after %d failure(s)", failures)
	}
	return false, ""
}

// fillUnrun records a reason for every transaction that never ran, so a
// cancelled run still reports one line per transaction.
func (r *Runner) fillUnrun(prepared []*Transaction, order []int, completed map[int]Result, reason string) {
	for _, index := range order {
		if _, ran := completed[index]; !ran {
			completed[index] = prepared[index].skippedResult(reason)
		}
	}
}

// runOne runs a transaction and stamps the result with when it started.
//
// The stamping is here rather than inside each result constructor because
// attempt returns from eight places and a constructor that forgot would leave a
// hole nothing but a cassette would ever notice — and it would notice it as one
// entry silently dated to whenever the report was written.
func (r *Runner) runOne(ctx context.Context, transaction *Transaction) Result {
	started := time.Now()
	result := r.attempt(ctx, transaction, started)
	result.Started = started
	return result
}

// attempt is the run itself. It takes the start rather than reading the clock so
// that every duration it reports and the timestamp runOne stamps are measured
// from the same instant.
func (r *Runner) attempt(ctx context.Context, transaction *Transaction, started time.Time) Result {
	if r.Hooks != nil {
		if err := r.Hooks.BeforeEach(transaction); err != nil {
			return transaction.errorResult(fmt.Sprintf("before hook: %v", err), time.Since(started))
		}
	}

	// A hook may take the transaction out of the run, or fail it outright,
	// without the server ever being asked.
	if transaction.Skip {
		return transaction.hookSkippedResult(time.Since(started))
	}
	if transaction.Fail != "" {
		return transaction.failResult([]string{transaction.Fail}, time.Since(started))
	}

	response, exchange, err := r.send(ctx, transaction)
	if err != nil {
		return transaction.errorResult(err.Error(), time.Since(started))
	}
	transaction.Real = response

	// Stamped here rather than inside each constructor judge returns through,
	// for the reason runOne stamps Started: several paths lead out of it, and
	// one that forgot would report a transaction the server did answer as one
	// it never answered — a zero that reads as a fact rather than an omission.
	result := r.judge(transaction, started, exchange)
	result.ResponseTime = exchange
	return result
}

// judge is everything after the response arrives: the validation hooks, the
// comparison against the description, and the after hook that may overrule it.
// It is split out so that the exchange can be stamped on whichever result comes
// back, in one place instead of at each of its exits.
func (r *Runner) judge(transaction *Transaction, started time.Time, exchange time.Duration) Result {
	if r.Hooks != nil {
		if err := r.Hooks.BeforeEachValidation(transaction); err != nil {
			return transaction.errorResult(fmt.Sprintf("beforeValidation hook: %v", err), time.Since(started))
		}
	}
	if transaction.Skip {
		return transaction.hookSkippedResult(time.Since(started))
	}

	result := transaction.validated(r.Checks, time.Since(started), exchange)

	if r.Hooks != nil {
		if err := r.Hooks.AfterEach(transaction); err != nil {
			return transaction.errorResult(fmt.Sprintf("after hook: %v", err), time.Since(started))
		}
		// A hook may fail a transaction the server answered correctly — that is
		// the point of an after hook that checks something the description
		// cannot express.
		if transaction.Fail != "" {
			return transaction.failResult([]string{transaction.Fail}, time.Since(started))
		}
	}

	return result
}

// send performs the request and records the response, with how long the
// exchange itself took — see Result.ResponseTime for why that is measured apart
// from the transaction it sits in.
func (r *Runner) send(ctx context.Context, transaction *Transaction) (validate.Message, time.Duration, error) {
	var body io.Reader
	if transaction.Request.Body != "" {
		body = strings.NewReader(transaction.Request.Body)
	}

	request, err := http.NewRequestWithContext(ctx,
		transaction.Request.Method, transaction.FullURL(), body)
	if err != nil {
		return validate.Message{}, 0, fmt.Errorf("building the request: %w", err)
	}
	for name, value := range transaction.Request.Headers {
		// Host is not a normal header: net/http ignores it in the header map
		// and takes it from the request instead.
		if strings.EqualFold(name, "host") {
			request.Host = value
			continue
		}
		request.Header.Set(name, value)
	}

	// Pacing happens before the clock starts. The pause is the run's, not the
	// server's, and timing it would be timing a decision the operator made.
	if err := r.pace(ctx); err != nil {
		return validate.Message{}, 0, err
	}
	response, exchange, err := r.do(request)
	if err != nil {
		return validate.Message{}, 0, fmt.Errorf("%s %s: %w",
			transaction.Request.Method, transaction.FullURL(), err)
	}
	defer response.Body.Close()

	// Reading the body is part of how long the answer took. A server that
	// sends its status line at once and then dribbles a megabyte out over four
	// seconds is slow, and a clock stopped at the headers would call it
	// instant — which is the reading of "response time" nobody waiting on the
	// response would recognise.
	reading := time.Now()
	payload, err := readBody(response)
	if err != nil {
		return validate.Message{}, 0, fmt.Errorf("reading the response body: %w", err)
	}
	exchange += time.Since(reading)

	headers := make(map[string]string, len(response.Header))
	for name, values := range response.Header {
		// Repeated headers are joined the way HTTP allows them to be sent,
		// so a set-cookie pair is not silently reduced to one.
		headers[strings.ToLower(name)] = strings.Join(values, ", ")
	}

	return validate.Message{
		StatusCode: strconv.Itoa(response.StatusCode),
		Headers:    headers,
		Body:       payload,
	}, exchange, nil
}

// Bounds on reading a streaming response.
//
// A stream does not end — that is what makes it a stream — so reading one to EOF
// waits out the client timeout and then reports the transaction as an error,
// which says the API was never asked when in fact it answered immediately and
// correctly. These bounds stop the read instead, and whatever arrived by then is
// taken as the body.
//
// Two seconds is the time bound because it is far longer than a server needs to
// produce its opening events — a stream that has sent nothing in two seconds is
// not merely slow — and short enough that a description covering a dozen
// streaming endpoints costs seconds rather than the six minutes the thirty
// second client timeout would have cost.
//
// A megabyte is the byte bound because a fast producer can push a great deal in
// two seconds, and the whole of an expected body is written inline in the
// description, so no expectation can be longer than what has already been read.
// Reading past it buys nothing and spends the memory of whatever machine is
// running the tests.
const (
	streamReadBudget = 2 * time.Second
	streamReadLimit  = 1 << 20
)

// streamingMediaTypes are the media types whose responses are not meant to end.
//
// The list is explicit rather than inferred from a missing Content-Length,
// because chunked encoding is ordinary: a JSON body of unknown length arrives
// that way and still finishes. Types such as `application/x-ndjson` are left off
// for the same reason — they arrive in pieces but they do stop, and cutting one
// short would report a mismatch against a body the server sent in full.
var streamingMediaTypes = map[string]bool{
	"text/event-stream":         true,
	"multipart/x-mixed-replace": true,
}

// readBody reads the response body, bounding the read when the media type is one
// that streams.
//
// The bounds are deliberately not applied to every response. An ordinary body
// ends on its own, and truncating a large but finite payload would report a
// difference the server is not responsible for.
func readBody(response *http.Response) (string, error) {
	if !streamingMediaTypes[baseMediaType(response.Header.Get("Content-Type"))] {
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			return "", err
		}
		return string(payload), nil
	}

	var collected bytes.Buffer
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		io.Copy(&collected, io.LimitReader(response.Body, streamReadLimit))
	}()

	timer := time.NewTimer(streamReadBudget)
	defer timer.Stop()

	select {
	case <-finished:
		// The stream ended, or the byte bound was reached, on its own.
	case <-timer.C:
		// Closing the body is the only way to interrupt a read already blocked
		// in the kernel. Waiting for the copy afterwards is not politeness: it is
		// what makes reading the buffer below safe, since otherwise the copy is
		// still appending to it while the response is being validated.
		response.Body.Close()
		<-finished
	}

	// A read error is dropped rather than returned. Ending the read early is
	// this side's decision, so the error that follows describes vertrag rather
	// than the server; and a stream cut short by the network is indistinguishable
	// from one cut short here, which is the normal outcome for a stream anyway.
	return collected.String(), nil
}

// Transaction is a compiled transaction as it moves through a run: hooks may
// rewrite the request, the expectation, or remove it altogether.
type Transaction struct {
	Name    string
	Request Request

	// source is the compiled transaction this was prepared from, kept so a
	// check that must send the SAME request again — ignored-auth re-sends it
	// without the credential — can prepare it afresh rather than reverse a
	// prepared one back into its inputs.
	source   compile.Transaction
	Expected validate.Message
	Real     validate.Message

	// FullPath is the path the request is actually sent to. It is derived once,
	// when the transaction is prepared, and only a hook writing to it moves the
	// request — editing Request.URI does not. Keeping it separate is what makes
	// that true: recomputing it from Request.URI would let a late hook silently
	// redirect a request that has already been sent.
	FullPath string

	// Skip removes the transaction from the run; Fail marks it failed without
	// consulting the response. Both are set by hooks.
	Skip bool
	Fail string

	endpoint string
	fullURL  string
}

func newTransaction(source compile.Transaction, endpoint string, extraHeaders []string) *Transaction {
	headers := make(map[string]string, len(source.Request.Headers))
	for _, header := range source.Request.Headers {
		headers[header.Name] = header.Value
	}
	for _, line := range extraHeaders {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if strings.EqualFold(name, "Cookie") {
			addCookies(headers, name, value)
			continue
		}
		headers[name] = value
	}

	expectedHeaders := make(map[string]string, len(source.Response.Headers))
	for _, header := range source.Response.Headers {
		expectedHeaders[header.Name] = header.Value
	}

	var schema json.RawMessage
	if source.Response.Schema != "" {
		schema = json.RawMessage(source.Response.Schema)
	}

	return &Transaction{
		Name:   source.Name,
		source: source,
		Request: Request{
			Method:  source.Request.Method,
			URI:     source.Request.URI,
			Headers: headers,
			Body:    source.Request.Body,
		},
		Expected: validate.Message{
			StatusCode:    source.Response.Status,
			Headers:       expectedHeaders,
			Body:          source.Response.Body,
			BodySchema:    schema,
			HeaderSchemas: source.Response.HeaderSchemas,
		},
		FullPath: source.Request.URI,
		endpoint: endpoint,
		fullURL:  endpoint + source.Request.URI,
	}
}

// addCookies merges a Cookie header line into the ones a transaction already
// carries, instead of replacing it.
//
// Every other header a run adds replaces what was there, which is right: two
// Authorization lines are not both meant. A Cookie header is different — it is
// a LIST of independent cookies sharing one line, and the two sources of that
// list mean different things. The description's cookies are parameters of the
// operation; the run's are the credential a login produced, or a `--header
// 'Cookie: …'` the tester supplied. Replacing the line dropped whichever came
// first: a run that logged in silently stopped sending every documented cookie
// parameter, and a description with cookie parameters silently deauthenticated
// every request. Neither is a choice anybody made.
//
// On a name collision the ADDED cookie wins, and the order of the run's
// headers decides the rest: run-wide `--header`, then conditional headers,
// then the credential, so the credential beats everything. That is the right
// way round. The description's value for a session cookie is an example — a
// string somebody typed into a YAML file — while the credential is the live
// session the whole run depends on; sending the example instead would log the
// run out at the first operation that happened to document its own session
// cookie.
func addCookies(headers map[string]string, name, added string) {
	existing, key := "", name
	for candidate, value := range headers {
		if strings.EqualFold(candidate, "Cookie") {
			existing, key = value, candidate
			break
		}
	}
	if existing == "" {
		headers[key] = added
		return
	}

	pairs := splitCookies(existing)
	for _, pair := range splitCookies(added) {
		replaced := false
		for i := range pairs {
			if cookieName(pairs[i]) == cookieName(pair) {
				pairs[i] = pair
				replaced = true
				break
			}
		}
		if !replaced {
			pairs = append(pairs, pair)
		}
	}
	headers[key] = strings.Join(pairs, "; ")
}

// splitCookies breaks a Cookie header into its pairs. Empty segments are
// dropped, so a trailing separator does not become a nameless cookie.
func splitCookies(line string) []string {
	var pairs []string
	for _, pair := range strings.Split(line, ";") {
		if pair = strings.TrimSpace(pair); pair != "" {
			pairs = append(pairs, pair)
		}
	}
	return pairs
}

// cookieName is the part of a pair before the `=`. Cookie names are
// case-sensitive, unlike header names, so they are compared as written.
func cookieName(pair string) string {
	name, _, _ := strings.Cut(pair, "=")
	return strings.TrimSpace(name)
}

// FullURL is the address the request is sent to.
//
// It is fixed when the transaction is prepared and does NOT track later edits to
// Request.URI. That is Dredd's behaviour: it resolves the address before hooks
// run, so a hook assigning `transaction.request.uri` changes what the hook and
// the report see but not where the request goes. Following the edited URI here
// would be more intuitive and would make the same hook file send different
// requests under each tool.
//
// A hook that really means to redirect the request sets `fullPath`.
func (t *Transaction) FullURL() string {
	return t.fullURL
}

// SetFullURL overrides the address, as a hook does.
func (t *Transaction) SetFullURL(url string) { t.fullURL = url }

// sentRequest is the request as it actually went out.
//
// Request.URI is whatever the hooks last wrote there, which is not necessarily
// where the request went — a hook may edit it without redirecting anything. The
// report has to show the address that was really used, or a failing test points
// the reader at a URL nobody requested.
func (t *Transaction) sentRequest() Request {
	sent := t.Request
	sent.URI = t.FullPath
	sent.URL = t.FullURL()
	return sent
}

// Endpoint is the server the transaction is aimed at.
func (t *Transaction) Endpoint() string { return t.endpoint }

func (t *Transaction) validated(checks Checks, elapsed, exchange time.Duration) Result {
	expected := t.Expected

	// A response that cannot carry a body is not checked for one, however the
	// description describes it.
	//
	// A HEAD response never has a body — that is the method's whole definition
	// — and a description that gives one a schema is describing what a GET to
	// the same resource returns and what headers HEAD will send. Validating
	// against it reported "the body does not parse" for every HEAD endpoint in
	// existence, blaming a server for obeying the protocol. The same holds for
	// 204 and 304, which RFC 9110 forbids a body on.
	if bodiless(t.Request.Method, t.Real.StatusCode) {
		expected.Body = ""
		expected.BodySchema = nil
	}

	validation := validate.Validate(expected, t.Real)

	result := Result{
		Name:       t.Name,
		Status:     StatusPass,
		Request:    t.sentRequest(),
		Expected:   t.Expected,
		Actual:     t.Real,
		Validation: validation,
		Duration:   elapsed,
	}
	result.Beyond = checks.run(expected, t.Real, exchange)
	if len(result.Beyond) > 0 {
		result.Status = StatusFail
	}

	if !validation.Valid {
		result.Status = StatusFail
		// Fields are reported in a fixed order so two runs of the same failure
		// read the same way.
		for _, field := range []string{"statusCode", "headers", "body"} {
			for _, message := range validation.Fields[field].Errors {
				result.Errors = append(result.Errors, field+": "+message)
			}
		}
	}

	// A GraphQL response is judged on its body rather than on its status, for
	// the reason set out in graphql.go: this endpoint answers 200 to errors
	// too, so everything above this line can pass while the server answered
	// nothing at all.
	if findings := graphqlResponseFindings(t.source.GraphQL, expected, t.Real); len(findings) > 0 {
		result.Status = StatusFail
		result.Errors = append(result.Errors, findings...)
	}
	return result
}

// checkIgnoredAuth re-sends a request without the credential and reports an
// endpoint that answered it anyway.
//
// Only for a request that carried a credential and succeeded: a transaction
// documented as failing, or one already excluded from authentication, says
// nothing about whether the credential mattered. The bare attempt must be
// refused — 401 or 403 — and anything else means the endpoint is open.
func (r *Runner) checkIgnoredAuth(ctx context.Context, source compile.Transaction, sent Result) (string, bool) {
	if !r.Checks.IgnoredAuth || r.Auth.Header == "" {
		return "", false
	}
	if r.Auth.Except[source.Name] || r.Auth.grantedBy(source) {
		return "", false
	}
	status, err := strconv.Atoi(strings.TrimSpace(sent.Actual.StatusCode))
	if err != nil || status < 200 || status > 299 {
		return "", false
	}

	// The same transaction, prepared without the credential.
	prepared := newTransaction(source, r.Endpoint, r.headers(source, false, true))

	reply, _, err := r.send(ctx, prepared)
	if err != nil {
		// The server refusing to talk is not an authentication finding.
		return "", false
	}
	bareStatus, err := strconv.Atoi(strings.TrimSpace(reply.StatusCode))
	if err != nil {
		return "", false
	}
	if bareStatus == http.StatusUnauthorized || bareStatus == http.StatusForbidden {
		return "", false
	}
	return fmt.Sprintf("the same request without the credential was answered %d, so this endpoint is not authenticated "+
		"however the description describes it (it answered %d with the credential)", bareStatus, status), true
}

func (t *Transaction) failResult(errors []string, elapsed time.Duration) Result {
	return Result{
		Name: t.Name, Status: StatusFail, Request: t.sentRequest(),
		Expected: t.Expected, Actual: t.Real, Errors: errors, Duration: elapsed,
	}
}

// skippedResult reports a transaction the plan could not run.
//
// It is a skip rather than a failure because nothing was asked of the server.
// A step whose values were to come from a response that never arrived would, if
// run anyway, send the description's own example — 404 against an identifier
// that was never created — and report a second failure with no relation to the
// first. One root cause should produce one finding, and a cascade of them is
// how a reader is taught to ignore a report.
// hookSkippedResult is a transaction a hook took out of the run.
//
// It carries the request, like every other result does. Without it the report
// printed the name with no method — `skip:  /api/v1/thing > ...`, two spaces and
// a gap where every pass and fail line has `GET` — and anything counting the
// report by anchoring on the method, which is the only way to tell a
// transaction line from a detail line, counted no skips at all.
func (t *Transaction) hookSkippedResult(elapsed time.Duration) Result {
	return Result{
		Name: t.Name, Status: StatusSkip, Request: t.sentRequest(),
		Expected: t.Expected, Duration: elapsed,
	}
}

func (t *Transaction) skippedResult(reason string) Result {
	return Result{
		Name: t.Name, Status: StatusSkip, Request: t.sentRequest(),
		Expected: t.Expected, Errors: []string{reason},
	}
}

func (t *Transaction) errorResult(message string, elapsed time.Duration) Result {
	return Result{
		Name: t.Name, Status: StatusError, Request: t.sentRequest(),
		Expected: t.Expected, Errors: []string{message}, Duration: elapsed,
	}
}

// bodiless reports whether the protocol forbids this response a body,
// whatever its description says.
//
// RFC 9110: a HEAD response carries none by definition, and neither does a 204
// or a 304. A description may still give them a content type and a schema —
// that is how a HEAD documents what its headers describe — and checking a body
// against it blames the server for obeying the protocol.
func bodiless(method, status string) bool {
	if strings.EqualFold(strings.TrimSpace(method), "HEAD") {
		return true
	}
	switch strings.TrimSpace(status) {
	case "204", "304":
		return true
	}
	return false
}
