// Package runner executes compiled transactions against a live server.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
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
	sentOnce  bool

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
	authenticated := r.Auth.Header != "" && !r.Auth.Except[transaction.Name]

	var conditional []string
	for _, header := range r.ConditionalHeaders {
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
	return r.send(ctx, newTransaction(source, r.Endpoint, r.headersFor(source)))
}

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
	failures := 0
	for _, index := range r.sequence(len(prepared)) {
		transaction := prepared[index]

		// Checked before the plan and before hooks: a transaction the config
		// takes out of the run should not be prepared, sequenced, or offered to
		// a hook that might act on it.
		if reason, skipped := r.Skip[transaction.Name]; skipped {
			completed[index] = transaction.skippedResult(configuredSkipReason(reason))
			continue
		}

		// Past the failure budget nothing more is sent. Skipped rather than
		// dropped: a pipeline that stops early still gets a report naming
		// every transaction, and can tell "did not run" from "passed".
		if r.MaxFailures > 0 && failures >= r.MaxFailures {
			completed[index] = transaction.skippedResult(
				fmt.Sprintf("not run: stopped after %d failure(s)", failures))
			continue
		}

		if r.Plan != nil {
			if reason, ok := r.Plan.Prepare(index, transaction, completed); !ok {
				completed[index] = transaction.skippedResult(reason)
				continue
			}
		}

		result := r.runOne(ctx, transaction)
		if r.Plan != nil {
			r.Plan.Record(index, transaction, result)
		}
		completed[index] = result
		if result.Status == StatusFail || result.Status == StatusError {
			failures++
		}
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

func (r *Runner) runOne(ctx context.Context, transaction *Transaction) Result {
	started := time.Now()

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

	response, err := r.send(ctx, transaction)
	if err != nil {
		return transaction.errorResult(err.Error(), time.Since(started))
	}
	transaction.Real = response

	if r.Hooks != nil {
		if err := r.Hooks.BeforeEachValidation(transaction); err != nil {
			return transaction.errorResult(fmt.Sprintf("beforeValidation hook: %v", err), time.Since(started))
		}
	}
	if transaction.Skip {
		return transaction.hookSkippedResult(time.Since(started))
	}

	result := transaction.validated(r.Checks, time.Since(started))

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

// send performs the request and records the response.
func (r *Runner) send(ctx context.Context, transaction *Transaction) (validate.Message, error) {
	var body io.Reader
	if transaction.Request.Body != "" {
		body = strings.NewReader(transaction.Request.Body)
	}

	request, err := http.NewRequestWithContext(ctx,
		transaction.Request.Method, transaction.FullURL(), body)
	if err != nil {
		return validate.Message{}, fmt.Errorf("building the request: %w", err)
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

	if err := r.pace(ctx); err != nil {
		return validate.Message{}, err
	}
	response, err := r.do(request)
	if err != nil {
		return validate.Message{}, fmt.Errorf("%s %s: %w",
			transaction.Request.Method, transaction.FullURL(), err)
	}
	defer response.Body.Close()

	payload, err := readBody(response)
	if err != nil {
		return validate.Message{}, fmt.Errorf("reading the response body: %w", err)
	}

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
	}, nil
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
	Name     string
	Request  Request
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
		if name, value, ok := strings.Cut(line, ":"); ok {
			headers[strings.TrimSpace(name)] = strings.TrimSpace(value)
		}
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
		Name: source.Name,
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

func (t *Transaction) validated(checks Checks, elapsed time.Duration) Result {
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
	result.Beyond = checks.run(expected, t.Real)
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
	return result
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
