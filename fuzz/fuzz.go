// Package fuzz tests an operation with requests drawn from its schemas, rather
// than the single example its description happens to show.
//
// A contract test built from a description sends one request per operation and
// checks the reply. That establishes the happy path works and nothing else:
// whether the server copes with a string at its length limit, rejects a number
// below its minimum, or survives a missing required property is never asked.
// Those are the cases where handlers actually break, because the example is the
// input the author had in mind while writing them.
//
// Two properties are checked, and they fail in opposite directions:
//
//   - A request the schema permits should be accepted. A 4xx means the server
//     and its description disagree about what is valid.
//   - A request the schema forbids should be rejected with a 4xx. A 2xx is a
//     validation bypass — the server acted on input it documented as invalid.
//
// Either way a 5xx is its own finding: the server failed rather than disagreed.
//
// The same two questions are asked of a request body and of each path, query and
// header parameter, but a parameter is the harder of the two to ask honestly.
// A body is JSON, so a value and the bytes carrying it are the same thing. A
// parameter is a string on the wire whatever its schema says, so `{type:
// integer}` describes the value the server will parse OUT of that string, not
// the string. Judging the string against the schema directly would call every
// correct server broken, and judging only the value drawn would report bypasses
// that are not there — a value of 12345 drawn to violate `{type: string}`
// arrives as "12345", which is a perfectly good string. So a parameter's value
// is judged as the server will see it: see intendedValidity.
package fuzz

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
	"pgregory.net/rapid"
)

// probeMu guards the run: rapid takes its case count and seed from process-wide
// flags, so two probes running at once would read each other's settings.
var probeMu sync.Mutex

var enableOnce sync.Once

// enableOutsideTests makes rapid usable from a normal binary.
//
// rapid.Check consults testing.Short, which panics with "Short called before
// Init" and then "before Parse" until the testing package has registered its
// flags and something has parsed them. Both are satisfied here, parsing an
// EMPTY argument list so that the CLI's own arguments are left alone — they are
// read by per-command flag sets, not by flag.CommandLine.
//
// testing.Init is idempotent and flag.CommandLine is already parsed under `go
// test`, so this is a no-op there.
func enableOutsideTests() {
	enableOnce.Do(func() {
		testing.Init()
		if !flag.CommandLine.Parsed() {
			flag.CommandLine.Parse(nil)
		}
		// A failing case is reported and shrunk in the output; writing a
		// replay file into whatever directory the user happened to run from is
		// not something a command-line tool should do uninvited.
		_ = flag.Set("rapid.nofailfile", "true")
	})
}

// Sender performs one generated request and reports what came back.
//
// The value is whatever the probe generated: the request body, or the value of
// the one parameter being varied. Where it belongs in the request is the
// caller's business, since only the caller knows how to put it there.
// The value is `any` because a parameter may be a list, which has no single
// text form — the URI template decides whether it becomes a repeated key or a
// comma-separated one, and rendering it here would reimplement that beside it.
type Sender func(ctx context.Context, value any) (validate.Message, error)

// ErrSkipped is what a Sender returns when the request was deliberately not
// sent — a hook took it out of the run.
//
// It is not a finding and not a transport failure. A hook exists precisely to
// say "not this one", and reporting the server for a request that never
// reached it would be the tool blaming the API for the tester's own
// instruction. The case is abandoned and counted.
var ErrSkipped = errors.New("the request was skipped before it was sent")

// Options controls how hard a probe looks.
type Options struct {
	// Cases is how many values to draw before concluding an operation is
	// sound. More finds more, and costs one request each.
	Cases int

	// Seed reproduces an earlier run. Zero lets rapid pick one and keep it to
	// itself, so a caller that wants replayable findings must choose the seed
	// and tell the user what it chose.
	Seed uint64

	// Deadline, when set, is the moment the whole run's time budget ends. A
	// probe that reaches it stops drawing and reports what it did; the caller
	// reports what it did not get to. Zero means no budget.
	Deadline time.Time

	// Pin holds named body fields at fixed values, applied to every drawn
	// value before it is rendered. See Pins: this is a safety interlock, not
	// a generation hint.
	Pin Pins

	// Accept lists statuses that are not findings. See Accept for why this is
	// counted rather than silent.
	Accept Accept

	// Suppression, when set, is where excused answers are counted. It is a
	// pointer because the count has to be taken as each decision is made —
	// after the fact an excused answer looks exactly like one that never
	// happened.
	Suppression *Suppression

	// Engaged, when set, collects the names of pins that actually held a
	// field on this probe, so a run can report where its interlock reached
	// rather than only that it was configured.
	Engaged map[string]int

	// Workers is how many operations to probe at once. It is read by the
	// coverage phase only: fuzz is serialised whatever this says, because
	// rapid takes its case count and seed from process-global flags and two
	// probes at once would overwrite each other's seed. Coverage uses no
	// rapid, so nothing is shared between two operations.
	Workers int
}

// OutOfTime reports whether the deadline has passed.
func (o Options) OutOfTime() bool {
	return !o.Deadline.IsZero() && time.Now().After(o.Deadline)
}

// Locations a generated value can occupy.
const (
	InBody   = "body"
	InPath   = "path"
	InQuery  = "query"
	InHeader = "header"
	// InArgument is a GraphQL field argument, whose value travels in the
	// request's `variables`. It is its own location rather than a body because
	// nothing about judging one is the same: the value is one entry of a
	// document vertrag composed, and the server states its verdict in the reply
	// BODY rather than in the status. See judgeGraphQL.
	InArgument = "argument"
	// InWhole is the subject of a whole-request finding: every part drawn
	// together, no single one to blame.
	InWhole = "whole request"
)

// Subject says which part of a request a probe varied.
//
// It reaches the report, because "the server accepted an over-long username"
// and "the server accepted a negative page number in the query string" are
// different bugs in different code, and a finding that does not say which is one
// the reader has to reproduce before they can start on it.
type Subject struct {
	// In is InBody, or where the parameter travels: InPath, InQuery or
	// InHeader.
	In string
	// Name is the parameter's name, empty for a body.
	Name string

	// Style is the parameter's serialisation style when it is one RFC 6570
	// cannot express ("spaceDelimited", "pipeDelimited", "deepObject"), else
	// empty. It decides whether an object value has a wire form at all.
	Style string

	// Where, for a GraphQL argument, is the field it belongs to. `first` alone
	// names nothing on a schema where nine fields paginate.
	Where string

	// Possessed marks a subject whose value must NAME something that already
	// exists rather than only being well formed — an `ID` argument, in
	// practice. Generation can produce anything the caller must SHAPE and
	// nothing they must POSSESS, so a server refusing a made-up identifier is
	// doing its job. It is the same exemption a path parameter's 404 gets, and
	// judge applies it in the same place.
	Possessed bool

	// byBody says the server states its verdict in the reply's body rather
	// than in its status. It is unexported because it is not a caller's
	// decision: an argument implies it, and only wholeSubject sets it
	// otherwise.
	byBody bool
}

// judgedByBody reports whether the reply's status settles the verdict.
//
// A GraphQL endpoint answers 200 to a query it refused, so for those the answer
// is no, and judge reads the body instead.
func (s Subject) judgedByBody() bool { return s.In == InArgument || s.byBody }

// Describe names the subject the way a sentence about it would.
func (s Subject) Describe() string {
	switch s.In {
	case InBody, "":
		return "body"
	case InWhole:
		return "whole request"
	case InArgument:
		if s.Where == "" {
			return fmt.Sprintf("argument %q", s.Name)
		}
		return fmt.Sprintf("argument %q of %s", s.Name, s.Where)
	}
	return fmt.Sprintf("%s parameter %q", s.In, s.Name)
}

// Finding is a generated request the server mishandled.
//
// The value is the shrunk one: rapid reduces a failing case to the smallest
// input that still fails, which is the difference between a report someone acts
// on and a wall of random characters they close.
type Finding struct {
	Mode    generate.Mode
	Subject Subject
	Value   any
	Status  string
	Message string

	// Unprobeable marks the outcome where nothing could be drawn that had the
	// validity being asked for, so no request was sent at all. It is a finding
	// about the description and this tool rather than about the server, and it
	// is reported rather than swallowed: a silent pass would claim the subject
	// was tested when it was not.
	Unprobeable bool
}

// Probe draws bodies from a schema and reports the first case the server
// mishandles, already shrunk.
//
// The second return is false when every generated case behaved, which is the
// result to hope for and says more than a single passing example does.
func Probe(ctx context.Context, schema generate.Schema, mode generate.Mode, send Sender, opts Options) (Finding, bool) {
	return probe(ctx, Subject{In: InBody}, schema, mode, bodyForm(), send, opts)
}

// ProbeBody is Probe for a body of a given media type: JSON, form-encoded or
// multipart. The second return is false with no probing done when the media
// type is one generation cannot lay a value out in.
func ProbeBody(ctx context.Context, mediaType string, schema generate.Schema, mode generate.Mode, send Sender, opts Options) (Finding, bool) {
	form, ok := BodyForm(mediaType, schema)
	if !ok {
		return Finding{}, false
	}
	return probe(ctx, Subject{In: InBody}, schema, mode, form, send, opts)
}

// ProbeParameter does the same for one path, query or header parameter.
//
// It is separate from Probe only because a parameter reaches the server as text
// and a body reaches it as JSON. Everything that decides whether the server was
// wrong is shared, deliberately: a parameter probe that judged replies by its
// own rules would drift from the body one, and the two would disagree about
// what a 500 means.
func ProbeParameter(ctx context.Context, subject Subject, schema generate.Schema, mode generate.Mode, send Sender, opts Options) (Finding, bool) {
	return probe(ctx, subject, schema, mode, parameterForm(subject, schema), send, opts)
}

// ProbeArgument does the same for one GraphQL field argument.
//
// It shares the whole of the loop with the other two — the draw, the pin, the
// validity check, the shrink — and differs in the two places GraphQL differs:
// the value stays a JSON value on its way into `variables`, and the verdict is
// read from the reply's body because a GraphQL endpoint answers 200 to its own
// refusals.
func ProbeArgument(ctx context.Context, subject Subject, schema generate.Schema, mode generate.Mode, send Sender, opts Options) (Finding, bool) {
	return probe(ctx, subject, schema, mode, argumentForm(), send, opts)
}

// probe is the whole of the generate-check-send-judge loop, for any subject.
func probe(
	ctx context.Context,
	subject Subject,
	schema generate.Schema,
	mode generate.Mode,
	form wire,
	send Sender,
	opts Options,
) (Finding, bool) {
	if opts.Cases <= 0 {
		opts.Cases = 20
	}

	// The judge compares what the server did against what the request was MEANT
	// to be, so a value that is not what generation intended produces a finding
	// about vertrag rather than the server. Marshalling the schema once here
	// lets every drawn value be checked before it is sent.
	rawSchema, err := json.Marshal(map[string]any(schema))
	if err != nil {
		return Finding{}, false
	}

	enableOutsideTests()

	probeMu.Lock()
	defer probeMu.Unlock()

	_ = flag.Set("rapid.checks", strconv.Itoa(opts.Cases))
	// Zero tells rapid to choose one, but the choice surfaces only in rapid's
	// own log, which nothing here reads — see Options.Seed.
	_ = flag.Set("rapid.seed", strconv.FormatUint(opts.Seed, 10))

	collector := &collector{}
	var found Finding
	usable := 0
	// sent counts the cases that actually reached the server, which is not the
	// same as the cases that were usable: a hook may replace a generated body
	// after the case was accepted, and then nothing was learned from it.
	sent := 0
	// passed remembers every value already sent AND answered acceptably, by
	// its wire text, so a value rapid draws twice costs one request rather
	// than two and every case a user asked for is a DISTINCT probe.
	//
	// Only passing values are remembered, deliberately. Skipping a repeat
	// looks to rapid like a pass, and rapid's shrinker works by re-running
	// smaller candidates: were a value that FAILED ever skipped as a repeat,
	// the shrinker would read the skip as "this smaller input passes" and
	// stop short of the minimum. A failing value never reaches the map — the
	// case fatals first — so a shrink candidate is either genuinely new or a
	// repeat of something that really did pass, and skipping that is exact.
	passed := map[string]bool{}

	rapid.Check(collector, func(t *rapid.T) {
		if opts.OutOfTime() {
			// The budget is spent. Not a failure — the caller reports how
			// far the run got — but nothing more is drawn or sent.
			return
		}

		value := generate.Value(schema, mode).Draw(t, subject.label())

		// Pins are applied here, between the draw and the render, because that
		// is the only point every generated value passes through. Applying them
		// inside the generator would leave the whole-request path uncovered,
		// and applying them after rendering would mean parsing the wire form
		// back to reach a field. A safety interlock with a path around it is
		// not one — see Pins.
		value, engaged := opts.Pin.ApplyTo(subject, schema, value)
		for _, name := range engaged {
			if opts.Engaged != nil {
				opts.Engaged[name]++
			}
		}

		rendered, ok := form.render(value)
		if !ok {
			// The value has no form this subject can carry — see the render
			// functions for what that means and why sending it anyway would
			// test something other than the parameter.
			return
		}
		key := wireKey(rendered)
		if passed[key] {
			return
		}

		// Confirm the request really has the validity it was drawn for, using
		// the same validator that judges the response. Generation cannot always
		// produce a violation — a schema whose only constraint is `const` read
		// under draft-4 forbids nothing, because the keyword did not exist yet
		// — and sending one of those would report a validation bypass that is
		// really a generator limitation. The case is abandoned instead.
		valid, decided := intendedValidity(rawSchema, form.interpret(rendered))
		if !decided || valid != (mode == generate.Valid) {
			return
		}
		usable++

		reply, err := send(ctx, rendered)
		if err == nil {
			sent++
		}
		if errors.Is(err, ErrSkipped) {
			// A hook took this request out of the run. Not the server's doing.
			return
		}
		if err != nil {
			// Reported as a finding in its own right, carrying the value that
			// provoked it. A handler that panics drops the connection without
			// answering, which arrives here as a transport error and not as a
			// status code — so treating this as noise would lose the crash that
			// generation exists to find.
			found = Finding{
				Mode:    mode,
				Subject: subject,
				Value:   rendered,
				Message: fmt.Sprintf("the request could not be completed: %v", err),
			}
			t.Fatalf("request failed: %v", err)
		}

		// An accepted status ends the case before the judge sees it, and is
		// counted on the way past. The count is the whole reason this is safe
		// to offer: see Accept.
		if code, err := strconv.Atoi(strings.TrimSpace(reply.StatusCode)); err == nil && opts.Accept.Excuses(code) {
			opts.Suppression.Record(code)
			passed[key] = true
			return
		}

		if message, bad := judge(mode, subject, reply); bad {
			found = Finding{
				Mode:    mode,
				Subject: subject,
				Value:   rendered,
				Status:  reply.StatusCode,
				Message: message,
			}
			t.Fatalf("%s", message)
		}
		passed[key] = true
	})

	if !collector.failed {
		if usable == 0 && opts.OutOfTime() {
			// Not unprobeable — the budget ran out before anything was
			// drawn. The caller reports that; there is nothing to say here.
			return Finding{}, false
		}
		if usable > 0 && sent == 0 {
			// Values were drawn and accepted, and none of them reached the
			// server — a hook took every one out of the run, or replaced the
			// body with its own.
			//
			// This is reported rather than passed over because the two look
			// identical from the outside and mean opposite things. A hook file
			// inherited from Dredd commonly fills in credentials for a login
			// operation, which silently ends every probe of it; the run would
			// otherwise say the operation was probed and found sound.
			return Finding{
				Mode:    mode,
				Subject: subject,
				Message: "every generated " + subject.Describe() + " was skipped or replaced by a " +
					"hook before it was sent, so the values its schema " + verb(mode) +
					" were never tested. A hook that fills in a value ends the probe of that value",
				Unprobeable: true,
			}, true
		}
		if usable == 0 {
			// Every drawn value was the opposite of what was asked for, or had
			// no form the subject could carry, so nothing was actually sent.
			// Reporting this as a clean result would claim the subject was
			// probed when it was not.
			return Finding{
				Mode:    mode,
				Subject: subject,
				Message: "nothing could be generated for the " + subject.Describe() +
					" that its schema " + verb(mode) + " and that survives being sent, " +
					"so it was not probed",
				Unprobeable: true,
			}, true
		}
		return Finding{}, false
	}
	return found, true
}

// wireKey is the text a rendered value is deduplicated by: the string itself
// for a scalar or a body, and the JSON of a list or object, so two draws that
// would put the same bytes on the wire are one probe.
func wireKey(rendered any) string {
	switch v := rendered.(type) {
	case string:
		return v
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(encoded)
	}
}

// label names the draw in rapid's output, and is stable per subject so that a
// seed replays to the same case.
func (s Subject) label() string {
	switch s.In {
	case InBody, "":
		return "body"
	case InArgument:
		return "argument"
	}
	return "parameter"
}

func verb(mode generate.Mode) string {
	if mode == generate.Valid {
		return "permits"
	}
	return "forbids"
}

// judge decides whether a reply is the right answer to the request that
// produced it.
//
// The reply rather than only its status, because one protocol here does not put
// its verdict there: a GraphQL endpoint answers 200 to a query it refused, so a
// judge holding the status alone would call every refusal an acceptance and
// report a validation bypass for each one. See judgeGraphQL.
func judge(mode generate.Mode, subject Subject, reply validate.Message) (string, bool) {
	code, err := strconv.Atoi(strings.TrimSpace(reply.StatusCode))
	if err != nil {
		return fmt.Sprintf("the server answered %q, which is not a status code", reply.StatusCode), true
	}

	switch {
	case code >= 500:
		// True whichever request was sent. A server may refuse input, but
		// failing on it is never the documented behaviour.
		return fmt.Sprintf("the server returned %d for a generated %s — it failed rather than rejected",
			code, subject.Describe()), true

	case subject.judgedByBody():
		return judgeGraphQL(mode, subject, code, reply.Body)

	case mode == generate.Valid && code == http.StatusNotFound && subject.In == InPath:
		// A well-formed identifier that names nothing is a 404 by design, and
		// says nothing about whether the parameter was understood. The
		// description promises which values are WELL FORMED, not which
		// resources exist, so treating this as a disagreement would report a
		// finding against every server whose database does not happen to hold
		// the generated id.
		return "", false

	case mode == generate.Invalid && code < 400:
		return fmt.Sprintf("the server returned %d for a %s its own schema forbids, "+
			"so the documented constraints are not enforced", code, subject.Describe()), true

	case mode == generate.Valid && code >= 400:
		return fmt.Sprintf("the server returned %d for a %s its own schema permits, "+
			"so it disagrees with its description about what is valid", code, subject.Describe()), true
	}
	return "", false
}

// judgeGraphQL decides the same question for a GraphQL argument, where the
// server states its verdict in the body.
//
// A GraphQL endpoint answers 200 to nearly everything — a query naming a field
// that does not exist, an argument of the wrong type, and a request served
// perfectly are all 200 with different bodies — and the specification only
// permits a non-200 for a request that could not be processed at all. So
// "rejected" here means a non-empty `errors`, or a 4xx; anything else is the
// server having accepted what it was sent.
//
// The possession exemption sits in the valid branch and nowhere else. A
// generated ID is well formed by construction and names nothing by luck, so a
// server saying so is right and reporting it would be vertrag blaming the API
// for an identifier vertrag invented. The invalid branch keeps its teeth: a
// malformed value is malformed whatever the caller holds, and a server that
// accepts one has a bypass whether or not an id was involved.
func judgeGraphQL(mode generate.Mode, subject Subject, code int, body string) (string, bool) {
	refused, why := graphqlRefusal(code, body)

	switch {
	case mode == generate.Valid && refused:
		if subject.Possessed {
			return "", false
		}
		return fmt.Sprintf("the server refused a generated %s that its own schema permits, "+
			"so it disagrees with its schema about what is valid: %s", subject.Describe(), why), true

	case mode == generate.Invalid && !refused:
		return fmt.Sprintf("the server answered without error for a generated %s its own schema forbids, "+
			"so the type the schema declares is not enforced", subject.Describe()), true
	}
	return "", false
}

// graphqlRefusal reads a reply as an acceptance or a refusal, and says why.
func graphqlRefusal(code int, body string) (bool, string) {
	if code >= 400 {
		// GraphQL over HTTP allows a 4xx for a request the server could not
		// process at all — a malformed document, an unreadable body — and that
		// is a refusal however the body reads.
		return true, fmt.Sprintf("it answered %d", code)
	}

	var document struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(body), &document); err != nil || len(document.Errors) == 0 {
		// A body that is not a GraphQL response is not a refusal of the
		// argument. It is a finding in its own right, and the one the examples
		// phase already makes about the same operation — see
		// runner/graphql.go. Repeating it once per generated value would bury
		// everything else the probe found.
		return false, ""
	}

	message := document.Errors[0].Message
	if message == "" {
		message = "(the server gave no message)"
	}
	return true, message
}

// collector receives rapid's verdict outside a Go test.
//
// rapid.Check takes an interface rather than *testing.T, which is what lets
// generation and shrinking run from the command line. Everything here is a sink
// except Failed, which rapid consults, and the failure text, which is already
// carried on the Finding.
type collector struct {
	failed  bool
	skipped bool
	output  strings.Builder
}

func (c *collector) Helper()      {}
func (c *collector) Name() string { return "fuzz" }

func (c *collector) Logf(format string, args ...any) {
	fmt.Fprintf(&c.output, format+"\n", args...)
}
func (c *collector) Log(args ...any) { fmt.Fprintln(&c.output, args...) }

func (c *collector) Skipf(format string, args ...any) { c.skipped = true }
func (c *collector) Skip(args ...any)                 { c.skipped = true }
func (c *collector) SkipNow()                         { c.skipped = true }

func (c *collector) Errorf(format string, args ...any) { c.fail(format, args...) }
func (c *collector) Error(args ...any)                 { c.failed = true }
func (c *collector) Fatalf(format string, args ...any) { c.fail(format, args...) }
func (c *collector) Fatal(args ...any)                 { c.failed = true }

func (c *collector) FailNow()       { c.failed = true }
func (c *collector) Fail()          { c.failed = true }
func (c *collector) Failed() bool   { return c.failed }
func (c *collector) Output() string { return c.output.String() }

func (c *collector) fail(format string, args ...any) {
	c.failed = true
	fmt.Fprintf(&c.output, format+"\n", args...)
}
