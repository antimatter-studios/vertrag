package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/fuzz"
	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// runFuzz is `vertrag fuzz`: test each operation with bodies and parameter
// values drawn from its schemas instead of the single example the description
// shows.
//
// It is a separate command rather than a flag on `run` because the two answer
// different questions. `run` is deterministic and comparable against Dredd —
// the same description produces the same requests every time, which is what
// makes it usable in CI as a regression gate. Generation is exploratory: it
// finds things a fixed set of requests cannot, and a run that discovers a new
// failure on a Tuesday is a feature there and a broken build here.
func runFuzz(args []string) error {
	fs := flag.NewFlagSet("fuzz", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to a vertrag.yml (default: the first of vertrag.yml, vertrag.yaml, dredd.yml found here)")
	endpoint := fs.String("endpoint", "", "base URL of the server under test")
	cases := fs.Int("cases", 20, "values to try per body and per parameter")
	seed := fs.Uint64("seed", 0, "replay a previous run (0 picks one and reports it)")
	mode := fs.String("mode", "both", "which values to send: valid, invalid, or both")
	noColor := fs.Bool("no-color", false, "disable coloured output")
	var headers stringList
	fs.Var(&headers, "header", "extra header to send with every request, as 'Name: value' (repeatable)")
	var only stringList
	fs.Var(&only, "only", "probe only the named transaction (repeatable)")
	var methods stringList
	fs.Var(&methods, "method", "probe only transactions using this method (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	modes, err := parseModes(*mode)
	if err != nil {
		return err
	}

	settings, err := resolveConfig(*configPath, fs.Args())
	if err != nil {
		return err
	}
	if *endpoint != "" {
		settings.Endpoint = *endpoint
	}
	if *noColor {
		settings.Color = false
	}
	settings.Header = append(settings.Header, headers...)
	settings.Only = append(settings.Only, only...)
	settings.Method = append(settings.Method, methods...)

	if err := settings.Validate(); err != nil {
		return err
	}

	source, err := os.ReadFile(settings.Blueprint)
	if err != nil {
		return fmt.Errorf("reading the API description: %w", err)
	}
	parsed, err := apidesc.Parse(source, settings.Blueprint)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", settings.Blueprint, err)
	}
	result := compile.Compile(parsed.MediaType, parsed.Elements, settings.Blueprint)

	annotations := reporter.CLI{Out: os.Stdout, Color: settings.Color}
	annotations.Annotations(toAnnotations(result.Annotations))
	if hasErrors(result.Annotations) {
		return fmt.Errorf("the API description could not be read; nothing was run")
	}

	transactions := filterTransactions(stripAPIName(result.Transactions), settings)

	// An operation whose body and parameters all lack schemas has nothing to
	// draw from. Saying how many were passed over, and why, is the difference
	// between a quiet run that tested little and one a reader can trust.
	probeable, skipped := partitionBySchema(transactions)
	if len(probeable) == 0 {
		fmt.Printf("No operation carries a schema for its body or for any parameter, so there is nothing to generate from.\n")
		if skipped > 0 {
			fmt.Printf("%d transaction(s) were passed over for that reason.\n", skipped)
		}
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	engine := runner.New(settings.Endpoint)
	engine.ExtraHeaders = settings.Header

	return probeAll(ctx, engine, probeable, modes, skipped, fuzz.Options{
		Cases: *cases,
		Seed:  *seed,
	}, settings.Color)
}

// target is one part of a request generation can vary, with the way to put a
// generated value back into the request it came from.
//
// Body and parameter differ in where the value goes and in nothing else, so
// they are probed by the same loop: a run that swept bodies thoroughly and
// parameters occasionally would find whichever bugs the loop happened to be
// written for.
type target struct {
	subject fuzz.Subject
	schema  generate.Schema
	apply   func(compile.Request, any) (compile.Request, error)
}

// probeTargets lists what can be generated for a request: its body, when the
// description gave the body a schema, and every parameter that carries one.
//
// A parameter whose schema describes an array or an object is left out rather
// than attempted, because there is no single string that unambiguously carries
// one — see fuzz.Probeable. A schema that will not parse is different, and is
// returned separately: that is a description someone should look at, not a
// decision this made.
func probeTargets(request compile.Request) (targets []target, unreadable []fuzz.Subject) {
	// A body is generated as JSON, so it can only be sent where JSON is what
	// the operation takes. A multipart schema describes the PARTS of a body
	// rather than a document, and carrying it — which generation needs, to
	// assemble those parts — must not be mistaken for permission to post a JSON
	// object at an upload endpoint. Doing so gets a 400 that says nothing about
	// the schema.
	if strings.TrimSpace(request.Schema) != "" && acceptsJSONBody(request) {
		schema, err := decodeSchema(request.Schema)
		switch {
		case err != nil:
			unreadable = append(unreadable, fuzz.Subject{In: fuzz.InBody})
		default:
			targets = append(targets, target{
				subject: fuzz.Subject{In: fuzz.InBody},
				schema:  schema,
				apply: func(r compile.Request, value any) (compile.Request, error) {
					body, ok := value.(string)
					if !ok {
						// Only a parameter can be a list; a body is always the
						// JSON text the generator serialised.
						return r, fmt.Errorf("a request body must be text, got %T", value)
					}
					r.Body = body
					return r, nil
				},
			})
		}
	}

	for _, parameter := range request.Parameters {
		if strings.TrimSpace(parameter.Schema) == "" {
			continue
		}
		subject := fuzz.Subject{In: parameter.In, Name: parameter.Name}

		schema, err := decodeSchema(parameter.Schema)
		if err != nil {
			unreadable = append(unreadable, subject)
			continue
		}
		if !fuzz.Probeable(schema) {
			continue
		}

		targets = append(targets, target{
			subject: subject,
			schema:  schema,
			apply: func(r compile.Request, value any) (compile.Request, error) {
				return r.SetParameter(parameter, value)
			},
		})
	}

	return targets, unreadable
}

// probeAll runs every target of every operation through every requested mode and
// reports.
func probeAll(
	ctx context.Context,
	engine *runner.Runner,
	transactions []compile.Transaction,
	modes []generate.Mode,
	skipped int,
	options fuzz.Options,
	color bool,
) error {
	findings := 0
	requests := 0
	probed := 0
	unprobeable := 0
	unattributable := 0

	for _, transaction := range transactions {
		targets, unreadable := probeTargets(transaction.Request)
		for _, subject := range unreadable {
			fmt.Printf("%s: the %s schema could not be read, so it was not probed\n",
				transaction.Name, subject.Describe())
		}

		// Does the operation work at all, sent exactly as the description
		// describes it? Established once, before anything is generated.
		//
		// Without this, an operation that fails for its own reasons reports one
		// finding per parameter, every one of them blaming a parameter that had
		// nothing to do with it. Against a real API the shape was unmistakable:
		// a download endpoint answering 404 because nothing had been read
		// produced three findings saying the server disagreed with its
		// description about a `format` value — while returning exactly the same
		// 404 for the documented value.
		//
		// It only silences the VALID half. A server accepting input its own
		// schema forbids is a validation bypass whether or not the operation
		// works, and that is the finding generation exists for.
		baselinePassed := baselineWorks(ctx, engine, transaction)

		for _, target := range targets {
			probed++

			for _, mode := range modes {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if mode == generate.Valid && !baselinePassed {
					unattributable++
					continue
				}

				send := func(ctx context.Context, value any) (validate.Message, error) {
					request, err := target.apply(transaction.Request, value)
					if err != nil {
						return validate.Message{}, err
					}
					attempt := transaction
					attempt.Request = request
					requests++
					return engine.Send(ctx, attempt)
				}

				var finding fuzz.Finding
				var found bool
				if target.subject.In == fuzz.InBody {
					finding, found = fuzz.Probe(ctx, target.schema, mode, send, options)
				} else {
					finding, found = fuzz.ProbeParameter(ctx, target.subject, target.schema, mode, send, options)
				}

				switch {
				case !found:
				case finding.Unprobeable:
					// Not a server mistake, so it does not fail the run — but it
					// is counted, because a probe that sent nothing proves
					// nothing and the summary would otherwise imply it did.
					unprobeable++
				default:
					findings++
					printFinding(transaction, target, finding, color)
				}
			}
		}
	}

	fmt.Printf("\n%d operation(s) probed over %d body and parameter target(s), %d request(s) sent, %d finding(s)",
		len(transactions), probed, requests, findings)
	if unattributable > 0 {
		fmt.Printf(", %d valid-input probe(s) skipped because the operation fails as documented",
			unattributable)
	}
	if unprobeable > 0 {
		fmt.Printf(", %d probe(s) had nothing to send and tested nothing", unprobeable)
	}
	if skipped > 0 {
		fmt.Printf(", %d transaction(s) skipped for having no schema to generate from", skipped)
	}
	fmt.Println()

	if findings > 0 {
		return errFailed
	}
	return nil
}

func printFinding(transaction compile.Transaction, target target, finding fuzz.Finding, color bool) {
	paint := func(code, text string) string { return reporter.Paint(color, code, text) }

	// The request shown is the one that produced the finding, not the compiled
	// one: for a path or query parameter they differ, and the whole value of
	// the report is being able to repeat the request that failed.
	request := transaction.Request
	if sent, err := target.apply(request, finding.Value); err == nil {
		request = sent
	}

	fmt.Printf("\n%s %s\n", paint(reporter.Red, "finding:"), transaction.Name)
	fmt.Printf("  %s\n", finding.Message)
	fmt.Printf("  %s %s %s\n", paint(reporter.Dim, "request:"), request.Method, request.URI)
	fmt.Printf("  %s %s\n", paint(reporter.Dim, finding.Subject.Describe()+":"), finding.Value)
	fmt.Printf("  %s %s\n", paint(reporter.Dim, "status: "), finding.Status)
}

// partitionBySchema separates the operations generation can work on from those
// it cannot, returning how many were left out.
func partitionBySchema(transactions []compile.Transaction) ([]compile.Transaction, int) {
	var probeable []compile.Transaction
	skipped := 0
	for _, transaction := range transactions {
		targets, unreadable := probeTargets(transaction.Request)
		if len(targets) == 0 && len(unreadable) == 0 {
			skipped++
			continue
		}
		probeable = append(probeable, transaction)
	}
	return probeable, skipped
}

func decodeSchema(raw string) (generate.Schema, error) {
	var schema generate.Schema
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		return nil, err
	}
	return schema, nil
}

func parseModes(name string) ([]generate.Mode, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "valid":
		return []generate.Mode{generate.Valid}, nil
	case "invalid":
		return []generate.Mode{generate.Invalid}, nil
	case "both", "":
		return []generate.Mode{generate.Valid, generate.Invalid}, nil
	default:
		return nil, fmt.Errorf("unknown mode %q; use valid, invalid, or both", name)
	}
}

// baselineWorks reports whether an operation succeeds sent exactly as its
// description describes it.
//
// One request, before any value is generated. What it buys is attribution: a
// finding that says "the server rejected a value your schema permits" is only
// worth reading if the server would have accepted the documented value, and an
// operation broken for its own reasons would otherwise blame every parameter it
// has.
func baselineWorks(ctx context.Context, engine *runner.Runner, transaction compile.Transaction) bool {
	reply, err := engine.Send(ctx, transaction)
	if err != nil {
		return false
	}

	status, err := strconv.Atoi(strings.TrimSpace(reply.StatusCode))
	if err != nil {
		return false
	}
	// Judged against what the description promised rather than against 2xx: an
	// operation documented as returning 404 is working when it returns one.
	expected, err := strconv.Atoi(strings.TrimSpace(transaction.Response.Status))
	if err != nil {
		return status < 400
	}
	return status == expected
}

// acceptsJSONBody reports whether the request's own content type is JSON.
func acceptsJSONBody(request compile.Request) bool {
	for _, header := range request.Headers {
		if !strings.EqualFold(header.Name, "Content-Type") {
			continue
		}
		media := strings.ToLower(strings.TrimSpace(strings.SplitN(header.Value, ";", 2)[0]))
		return media == "application/json" ||
			strings.HasSuffix(media, "+json")
	}
	// No content type stated and a schema present: JSON is what every other
	// part of this assumes, and generating for it is the useful default.
	return true
}
