package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
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
	reporterName := fs.String("reporter", "", "also emit the probe results through a reporter: cli, dot, markdown, html, or junit")
	output := fs.String("output", "", "write the --reporter output to a file instead of stdout")
	noColor := fs.Bool("no-color", false, "disable coloured output")
	var headers stringList
	fs.Var(&headers, "header", "extra header to send with every request, as 'Name: value' (repeatable)")
	var only stringList
	fs.Var(&only, "only", "probe only the named transaction (repeatable)")
	var methods stringList
	fs.Var(&methods, "method", "probe only transactions using this method (repeatable)")
	var tags stringList
	fs.Var(&tags, "tag", "probe only transactions whose operation carries this tag (repeatable)")
	var transport transportFlags
	addTransportFlags(fs, &transport)

	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	transport.noteGiven(fs)

	modes, err := parseModes(*mode)
	if err != nil {
		return err
	}

	settings, err := resolveConfig(*configPath, positional)
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
	settings.Tag = append(settings.Tag, tags...)
	transport.apply(&settings.Transport)

	if err := settings.Validate(); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, signature(settings))

	for _, key := range settings.Unsupported {
		fmt.Fprintf(os.Stderr, "vertrag: `%s` is set but not supported yet; it is being ignored\n", key)
	}
	for _, note := range settings.Notes {
		fmt.Fprintf(os.Stderr, "vertrag: %s\n", note)
	}

	source, err := os.ReadFile(settings.Spec)
	if err != nil {
		return fmt.Errorf("reading the API description: %w", err)
	}
	parsed, err := apidesc.Parse(source, settings.Spec)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", settings.Spec, err)
	}
	result := compile.Compile(parsed.MediaType, parsed.Elements, settings.Spec)

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

	engine, err := newEngine(settings)
	if err != nil {
		return err
	}
	engine.ExtraHeaders = settings.Header

	// Probing needs the credential as much as a run does: without it an
	// authenticated API answers 401 to every case, no transaction passes the
	// baseline check, and the report says nothing was worth probing rather than
	// that the door was locked.
	if err := applyConfiguredRules(ctx, engine, settings, probeable); err != nil {
		return err
	}

	probeable, configSkipped := withoutSkipped(probeable, engine.Skip)
	if len(configSkipped) > 0 {
		fmt.Printf("%d transaction(s) left out by the skip list in %s.\n",
			len(configSkipped), settings.Source)
	}
	if len(probeable) == 0 {
		fmt.Printf("Every operation that could be probed is on the skip list, so nothing was sent.\n")
		return nil
	}

	// rapid reports the seed it picks only through a log nothing surfaces, so a
	// zero seed is chosen here instead — the printed value is then, by
	// construction, the one every probe used.
	for *seed == 0 {
		*seed = rand.Uint64()
	}
	fmt.Printf("seed: %d (replay with --seed %d)\n", *seed, *seed)

	results, runErr := probeAll(ctx, engine, probeable, modes, skipped, fuzz.Options{
		Cases: *cases,
		Seed:  *seed,
	}, settings.Color)

	// The narrative above is the fuzz report; a --reporter is for machines
	// and pipelines, so it comes in addition rather than instead. The config
	// file's reporter list is deliberately not consulted: it configures `run`,
	// and a junit file of contract results silently replaced by probe results
	// would be a surprise in the middle of someone's pipeline.
	if *reporterName != "" {
		emitSettings := settings
		emitSettings.Reporters = []string{*reporterName}
		emitSettings.Outputs = []string{*output}
		emit, closeFiles, err := newReporter(emitSettings)
		if err != nil {
			return err
		}
		emit.Report(results)
		closeFiles()
	}
	return runErr
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

// probeAll runs every target of every operation through every requested mode,
// reports as it goes, and returns one result per probe so a --reporter can
// render the run the way it renders a contract run.
func probeAll(
	ctx context.Context,
	engine *runner.Runner,
	transactions []compile.Transaction,
	modes []generate.Mode,
	skipped int,
	options fuzz.Options,
	color bool,
) ([]runner.Result, error) {
	findings := 0
	requests := 0
	probed := 0
	unprobeable := 0
	unattributable := 0
	refusedBaselines := 0
	var results []runner.Result

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
		base := baselineWorks(ctx, engine, transaction)
		if base.refused {
			refusedBaselines++
		}

		for _, target := range targets {
			probed++

			for _, mode := range modes {
				if ctx.Err() != nil {
					return results, ctx.Err()
				}
				if mode == generate.Valid && !base.ok {
					unattributable++
					continue
				}
				probeName := transaction.Name + " · " + target.subject.Describe() + " · " + modeName(mode)

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
					results = append(results, runner.Result{
						Name:    probeName,
						Status:  runner.StatusPass,
						Request: sentAs(engine, transaction.Request),
					})
				case finding.Unprobeable:
					// Not a server mistake, so it does not fail the run — but it
					// is counted, because a probe that sent nothing proves
					// nothing and the summary would otherwise imply it did.
					unprobeable++
					results = append(results, runner.Result{
						Name:   probeName,
						Status: runner.StatusSkip,
						Errors: []string{finding.Message},
					})
				default:
					findings++
					printFinding(engine, transaction, target, finding, color)

					// The result carries the request that provoked the finding,
					// the same one the narrative shows, so a junit consumer can
					// repeat it without the terminal log.
					failed := transaction.Request
					if sent, err := target.apply(failed, finding.Value); err == nil {
						failed = sent
					}
					results = append(results, runner.Result{
						Name:    probeName,
						Status:  runner.StatusFail,
						Request: sentAs(engine, failed),
						Errors:  []string{finding.Message},
					})
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
	// Said last and said plainly: every other number above is close to
	// meaningless when the server never let the probe in.
	if refusedBaselines > 0 {
		fmt.Printf("\n\n%d operation(s) answered 401 or 403 to the documented request, so little was learned about them.\n"+
			"Set `auth` in your vertrag.yml, or pass --header, to probe behind the credential.",
			refusedBaselines)
	}
	if unprobeable > 0 {
		fmt.Printf(", %d probe(s) had nothing to send and tested nothing", unprobeable)
	}
	if skipped > 0 {
		fmt.Printf(", %d transaction(s) skipped for having no schema to generate from", skipped)
	}
	fmt.Println()

	if findings > 0 {
		return results, errFailed
	}
	return results, nil
}

// modeName is the mode as a probe's name says it.
func modeName(mode generate.Mode) string {
	if mode == generate.Valid {
		return "valid"
	}
	return "invalid"
}

func printFinding(engine *runner.Runner, transaction compile.Transaction, target target, finding fuzz.Finding, color bool) {
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
	if repro := reporter.Curl(sentAs(engine, request)); repro != "" {
		fmt.Printf("  %s %s\n", paint(reporter.Dim, "repro: "), repro)
	}
}

// sentAs renders a compiled request the way the runner sends it, so the
// reproduction line carries the real address and the run-wide headers instead
// of the fragment the description states.
func sentAs(engine *runner.Runner, request compile.Request) runner.Request {
	headers := make(map[string]string, len(request.Headers)+len(engine.ExtraHeaders))
	for _, header := range request.Headers {
		headers[header.Name] = header.Value
	}
	for _, extra := range engine.ExtraHeaders {
		if name, value, found := strings.Cut(extra, ":"); found {
			headers[strings.TrimSpace(name)] = strings.TrimSpace(value)
		}
	}
	return runner.Request{
		Method:  request.Method,
		URI:     request.URI,
		Headers: headers,
		Body:    request.Body,
		URL:     engine.Endpoint + request.URI,
	}
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
// It also distinguishes a locked door from a disagreement. An unauthenticated
// probe of an authenticated API fails every baseline with 401, and reporting
// that as "the operation fails as documented" sends the reader to look at their
// handler when what they needed was a credential.
func baselineWorks(ctx context.Context, engine *runner.Runner, transaction compile.Transaction) baseline {
	reply, err := engine.Send(ctx, transaction)
	if err != nil {
		return baseline{}
	}

	status, err := strconv.Atoi(strings.TrimSpace(reply.StatusCode))
	if err != nil {
		return baseline{}
	}

	// Judged against what the description promised rather than against 2xx: an
	// operation documented as returning 404 is working when it returns one.
	expected, err := strconv.Atoi(strings.TrimSpace(transaction.Response.Status))
	switch {
	case err != nil:
		return baseline{ok: status < 400, refused: refused(status, 0)}
	default:
		return baseline{ok: status == expected, refused: refused(status, expected)}
	}
}

// baseline is what one baseline request established.
type baseline struct {
	ok bool
	// refused means the server turned the request away for want of credentials
	// rather than disagreeing with it.
	refused bool
}

// refused reports whether a status means "not allowed in", except where the
// description says that is the documented answer — an endpoint whose 401 is
// under test is working when it gives one.
func refused(status, expected int) bool {
	if status == expected {
		return false
	}
	return status == 401 || status == 403
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
