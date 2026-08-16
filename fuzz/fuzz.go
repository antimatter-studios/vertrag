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
	"flag"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

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
// The string is whatever the probe generated: the request body, or the value of
// the one parameter being varied. Where it belongs in the request is the
// caller's business, since only the caller knows how to put it there.
type Sender func(ctx context.Context, value string) (validate.Message, error)

// Options controls how hard a probe looks.
type Options struct {
	// Cases is how many values to draw before concluding an operation is
	// sound. More finds more, and costs one request each.
	Cases int

	// Seed reproduces an earlier run. Zero picks one and reports it, so a
	// finding can always be replayed.
	Seed uint64
}

// Locations a generated value can occupy.
const (
	InBody   = "body"
	InPath   = "path"
	InQuery  = "query"
	InHeader = "header"
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
}

// Describe names the subject the way a sentence about it would.
func (s Subject) Describe() string {
	if s.In == InBody || s.In == "" {
		return "body"
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
	Value   string
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
	// Zero tells rapid to choose one, which is also what it reports back, so a
	// finding can always be replayed with --seed.
	_ = flag.Set("rapid.seed", strconv.FormatUint(opts.Seed, 10))

	collector := &collector{}
	var found Finding
	usable := 0

	rapid.Check(collector, func(t *rapid.T) {
		value := generate.Value(schema, mode).Draw(t, subject.label())

		rendered, ok := form.render(value)
		if !ok {
			// The value has no form this subject can carry — see the render
			// functions for what that means and why sending it anyway would
			// test something other than the parameter.
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

		if message, bad := judge(mode, subject, reply.StatusCode); bad {
			found = Finding{
				Mode:    mode,
				Subject: subject,
				Value:   rendered,
				Status:  reply.StatusCode,
				Message: message,
			}
			t.Fatalf("%s", message)
		}
	})

	if !collector.failed {
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

// label names the draw in rapid's output, and is stable per subject so that a
// seed replays to the same case.
func (s Subject) label() string {
	if s.In == InBody || s.In == "" {
		return "body"
	}
	return "parameter"
}

func verb(mode generate.Mode) string {
	if mode == generate.Valid {
		return "permits"
	}
	return "forbids"
}

// judge decides whether a status is the right answer to the request that
// produced it.
func judge(mode generate.Mode, subject Subject, status string) (string, bool) {
	code, err := strconv.Atoi(strings.TrimSpace(status))
	if err != nil {
		return fmt.Sprintf("the server answered %q, which is not a status code", status), true
	}

	switch {
	case code >= 500:
		// True whichever request was sent. A server may refuse input, but
		// failing on it is never the documented behaviour.
		return fmt.Sprintf("the server returned %d for a generated %s — it failed rather than rejected",
			code, subject.Describe()), true

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
