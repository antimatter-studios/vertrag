// Package fuzz tests an operation with request bodies drawn from its schema,
// rather than the single example its description happens to show.
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
//   - A body the schema permits should be accepted. A 4xx means the server and
//     its description disagree about what is valid.
//   - A body the schema forbids should be rejected with a 4xx. A 2xx is a
//     validation bypass — the server acted on input it documented as invalid.
//
// Either way a 5xx is its own finding: the server failed rather than disagreed.
package fuzz

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
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
type Sender func(ctx context.Context, body string) (validate.Message, error)

// Options controls how hard a probe looks.
type Options struct {
	// Cases is how many bodies to draw before concluding an operation is
	// sound. More finds more, and costs one request each.
	Cases int

	// Seed reproduces an earlier run. Zero picks one and reports it, so a
	// finding can always be replayed.
	Seed uint64
}

// Finding is a generated request the server mishandled.
//
// The body is the shrunk one: rapid reduces a failing case to the smallest
// input that still fails, which is the difference between a report someone acts
// on and a wall of random characters they close.
type Finding struct {
	Mode    generate.Mode
	Body    string
	Status  string
	Message string
}

// Probe draws bodies from a schema and reports the first case the server
// mishandles, already shrunk.
//
// The second return is false when every generated case behaved, which is the
// result to hope for and says more than a single passing example does.
func Probe(ctx context.Context, schema generate.Schema, mode generate.Mode, send Sender, opts Options) (Finding, bool) {
	if opts.Cases <= 0 {
		opts.Cases = 20
	}

	// The judge compares what the server did against what the body was MEANT to
	// be, so a body that is not what generation intended produces a finding
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
		value := generate.Value(schema, mode).Draw(t, "body")

		encoded, err := json.Marshal(value)
		if err != nil {
			// A value that will not serialise is a generator problem, not a
			// server one, and blaming the server for it would be wrong.
			t.Skipf("generated value does not encode: %v", err)
		}

		// Confirm the value really has the validity it was drawn for, using the
		// same validator that judges the response. Generation cannot always
		// produce a violation — a schema whose only constraint is `const` read
		// under draft-4 forbids nothing, because the keyword did not exist yet
		// — and sending one of those would report a validation bypass that is
		// really a generator limitation. The case is abandoned instead.
		result := validate.AgainstSchema(rawSchema, string(encoded))
		if result.Valid != (mode == generate.Valid) {
			return
		}
		usable++

		reply, err := send(ctx, string(encoded))
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}

		if message, bad := judge(mode, reply.StatusCode); bad {
			found = Finding{
				Mode:    mode,
				Body:    string(encoded),
				Status:  reply.StatusCode,
				Message: message,
			}
			t.Fatalf("%s", message)
		}
	})

	if !collector.failed {
		if usable == 0 {
			// Every drawn body was the opposite of what was asked for, so
			// nothing was actually sent. Reporting this as a clean result would
			// claim the operation was probed when it was not.
			return Finding{
				Mode: mode,
				Message: "nothing could be generated that the schema " +
					verb(mode) + ", so this operation was not probed",
			}, true
		}
		return Finding{}, false
	}
	return found, true
}

func verb(mode generate.Mode) string {
	if mode == generate.Valid {
		return "permits"
	}
	return "forbids"
}

// judge decides whether a status is the right answer to the body that produced
// it.
func judge(mode generate.Mode, status string) (string, bool) {
	code, err := strconv.Atoi(strings.TrimSpace(status))
	if err != nil {
		return fmt.Sprintf("the server answered %q, which is not a status code", status), true
	}

	switch {
	case code >= 500:
		// True whichever body was sent. A server may refuse input, but failing
		// on it is never the documented behaviour.
		return fmt.Sprintf("the server returned %d — it failed rather than rejected", code), true

	case mode == generate.Invalid && code < 400:
		return fmt.Sprintf("the server returned %d for a body its own schema forbids, "+
			"so the documented constraints are not enforced", code), true

	case mode == generate.Valid && code >= 400:
		return fmt.Sprintf("the server returned %d for a body its own schema permits, "+
			"so it disagrees with its description about what is valid", code), true
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
