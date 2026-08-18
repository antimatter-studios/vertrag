package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

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
	var shared probeFlags
	addProbeFlags(fs, &shared, "probe")
	cases := fs.Int("cases", 50, "distinct values to try per body and per parameter")
	maxTime := fs.Duration("max-time", 0, "stop probing after this long, e.g. 2m; what was not reached is reported as skipped (0 = no limit)")
	seed := fs.Uint64("seed", 0, "replay a previous run (0 picks one and reports it)")
	mode := fs.String("mode", "both", "which values to send: valid, invalid, or both")
	whole := fs.Bool("whole-request", false, "also draw every parameter and the body together per case, to reach bugs in their interaction (findings then name the request, not one part)")

	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	modes, err := parseModes(*mode)
	if err != nil {
		return err
	}

	set, err := prepareProbes(&shared, fs, positional)
	if err != nil || set == nil {
		return err
	}
	defer set.stop()

	// rapid reports the seed it picks only through a log nothing surfaces, so a
	// zero seed is chosen here instead — the printed value is then, by
	// construction, the one every probe used.
	for *seed == 0 {
		*seed = rand.Uint64()
	}
	fmt.Printf("seed: %d (replay with --seed %d)\n", *seed, *seed)

	options := fuzz.Options{Cases: *cases, Seed: *seed}
	if *maxTime > 0 {
		options.Deadline = time.Now().Add(*maxTime)
	}
	results, runErr := probeAll(set.ctx, set.engine, set.probeable, modes, set.skipped, options,
		set.settings.Color, *whole, newRefusals(set.settings))

	if err := emitThrough(&shared, set.settings, results); err != nil {
		return err
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
	// media is the body's bare media type; empty for a parameter.
	media string
	apply func(compile.Request, any) (compile.Request, error)
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
	// A body is generated in the layout its content type asks for: JSON,
	// form-encoded, or multipart. A media type generation cannot speak is
	// left alone rather than sent as JSON — posting a JSON object at an
	// upload endpoint gets a 400 that says nothing about the schema.
	if strings.TrimSpace(request.Schema) != "" {
		schema, err := decodeSchema(request.Schema)
		media := bodyMediaType(request)
		switch {
		case err != nil:
			unreadable = append(unreadable, fuzz.Subject{In: fuzz.InBody})
		case !fuzz.SpeaksBody(media, schema):
			// Not an error and not unreadable: a description asking for a
			// body vertrag has no layout for. Counted with the schema-less
			// operations by the caller.
		default:
			targets = append(targets, target{
				subject: fuzz.Subject{In: fuzz.InBody},
				schema:  schema,
				media:   media,
				apply: func(r compile.Request, value any) (compile.Request, error) {
					body, ok := value.(string)
					if !ok {
						// Only a parameter can be a list; a body is always the
						// text the wire form serialised.
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
		subject := fuzz.Subject{In: parameter.In, Name: parameter.Name, Style: parameter.Style}

		schema, err := decodeSchema(parameter.Schema)
		if err != nil {
			unreadable = append(unreadable, subject)
			continue
		}
		if !fuzz.Probeable(schema, parameter.Style) {
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
	whole bool,
	refused *refusals,
) ([]runner.Result, error) {
	findings := 0
	requests := 0
	probed := 0
	unprobeable := 0
	unattributable := 0
	loginExempt := 0
	outOfTime := 0
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
			refused.note(transaction)
		}
		// The operation that grants the credential is exempt from valid-input
		// probing — see refusals.isLogin.
		isLogin := refused.isLogin(transaction)

		for _, target := range targets {
			probed++

			for _, mode := range modes {
				if ctx.Err() != nil {
					return results, ctx.Err()
				}
				if mode == generate.Valid && (!base.ok || isLogin) {
					if isLogin {
						loginExempt++
					} else {
						unattributable++
					}
					continue
				}
				probeName := transaction.Name + " · " + target.subject.Describe() + " · " + modeName(mode)

				// Past the time budget nothing more is drawn. Reported as
				// skipped rather than dropped, for the reason --max-failures
				// gives: a report that stopped early must still name what
				// it did not reach, or it reads as a shorter run that passed.
				if options.OutOfTime() {
					outOfTime++
					results = append(results, runner.Result{
						Name: probeName, Status: runner.StatusSkip,
						Errors: []string{"not probed: the --max-time budget was spent"},
					})
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
					finding, found = fuzz.ProbeBody(ctx, target.media, target.schema, mode, send, options)
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

		// The second pass, when asked for: every part drawn together, so a
		// bug that only shows when two inputs meet is reachable. Skipped
		// when the operation offers fewer than two parts — the per-target
		// pass has already asked everything there is to ask.
		if whole && len(targets) >= 2 {
			for _, mode := range modes {
				if ctx.Err() != nil {
					return results, ctx.Err()
				}
				if mode == generate.Valid && (!base.ok || isLogin) {
					continue
				}
				wholeName := transaction.Name + " · whole request · " + modeName(mode)
				if options.OutOfTime() {
					outOfTime++
					results = append(results, runner.Result{
						Name: wholeName, Status: runner.StatusSkip,
						Errors: []string{"not probed: the --max-time budget was spent"},
					})
					continue
				}
				parts := make([]fuzz.Part, 0, len(targets))
				byLabel := map[string]target{}
				for _, tg := range targets {
					label := fuzz.PartLabel(tg.subject)
					parts = append(parts, fuzz.Part{Subject: tg.subject, Schema: tg.schema, Media: tg.media})
					byLabel[label] = tg
				}

				sendWhole := func(ctx context.Context, values map[string]any) (validate.Message, error) {
					request := transaction.Request
					for label, value := range values {
						var err error
						if request, err = byLabel[label].apply(request, value); err != nil {
							return validate.Message{}, err
						}
					}
					attempt := transaction
					attempt.Request = request
					requests++
					return engine.Send(ctx, attempt)
				}

				finding, found := fuzz.ProbeWhole(ctx, parts, mode, sendWhole, options)
				if !found {
					results = append(results, runner.Result{
						Name: wholeName, Status: runner.StatusPass,
						Request: sentAs(engine, transaction.Request),
					})
					continue
				}
				findings++
				printWholeFinding(engine, transaction, byLabel, finding, color)
				failed := transaction.Request
				for label, value := range finding.Values {
					if r, err := byLabel[label].apply(failed, value); err == nil {
						failed = r
					}
				}
				results = append(results, runner.Result{
					Name: wholeName, Status: runner.StatusFail,
					Request: sentAs(engine, failed), Errors: []string{finding.Message},
				})
			}
		}
	}

	fmt.Printf("\n%d operation(s) probed over %d body and parameter target(s), %d request(s) sent, %d finding(s)",
		len(transactions), probed, requests, findings)
	if unattributable > 0 {
		fmt.Printf(", %d valid-input probe(s) skipped because the operation fails as documented",
			unattributable)
	}
	if loginExempt > 0 {
		fmt.Printf(", %d skipped on the login operation", loginExempt)
	}
	if unprobeable > 0 {
		fmt.Printf(", %d probe(s) had nothing to send and tested nothing", unprobeable)
	}
	if outOfTime > 0 {
		fmt.Printf(", %d probe(s) not reached before --max-time ran out", outOfTime)
	}
	if skipped > 0 {
		fmt.Printf(", %d transaction(s) skipped for having no schema to generate from", skipped)
	}
	// Said last and said plainly: every other number above is close to
	// meaningless when the server never let the probe in.
	refused.report()
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

// printWholeFinding is printFinding for a request drawn whole: every part's
// value is shown, and the culprit — the one drawn invalid — is named when
// there is one, so the reader still has somewhere to start.
func printWholeFinding(engine *runner.Runner, transaction compile.Transaction, byLabel map[string]target, finding fuzz.WholeFinding, color bool) {
	paint := func(code, text string) string { return reporter.Paint(color, code, text) }

	request := transaction.Request
	labels := make([]string, 0, len(finding.Values))
	for label := range finding.Values {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		if sent, err := byLabel[label].apply(request, finding.Values[label]); err == nil {
			request = sent
		}
	}

	fmt.Printf("\n%s %s\n", paint(reporter.Red, "finding:"), transaction.Name)
	fmt.Printf("  %s\n", finding.Message)
	fmt.Printf("  %s %s %s\n", paint(reporter.Dim, "request:"), request.Method, request.URI)
	for _, label := range labels {
		mark := ""
		if label == finding.Culprit {
			mark = " " + paint(reporter.Red, "(drawn invalid)")
		}
		fmt.Printf("  %s %v%s\n", paint(reporter.Dim, label+":"), finding.Values[label], mark)
	}
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
	for _, transaction := range successVariants(transactions) {
		targets, unreadable := probeTargets(transaction.Request)
		if len(targets) == 0 && len(unreadable) == 0 {
			skipped++
			continue
		}
		probeable = append(probeable, transaction)
	}
	return probeable, skipped
}

// successVariants keeps one transaction per operation: the one that expects
// success.
//
// A description yields a transaction per documented response — the 200, the
// 400, the 404, the 500 — and a suite reaches each error variant by some
// trigger of its own: a conditional header that tells a mock which failure
// to stage, an `except` entry that withholds the credential. Those triggers
// ride on the transaction, so a probe sent THROUGH the 500 variant goes out
// with "please break" attached and the server dutifully breaks — and the
// finding says the server returned 500 for a generated value, which is true
// and means nothing. Probing asks how an operation handles input it did not
// expect, and that question is only meaningful against the request that is
// supposed to succeed. So: the lowest 2xx variant of each operation, or the
// first variant when none is 2xx (an operation documented only as failing).
//
// Found the hard way against a real suite, whose mock is told which failure
// to stage by a header keyed on the expected status: 94 of 105 findings were
// the mock doing exactly as it was asked.
func successVariants(transactions []compile.Transaction) []compile.Transaction {
	type key struct{ method, template string }
	best := map[key]int{} // index into out
	var out []compile.Transaction

	for _, transaction := range transactions {
		k := key{transaction.Request.Method, operationKey(transaction)}
		status := statusOf(transaction)
		if i, seen := best[k]; seen {
			// Prefer a 2xx over anything, and the lowest 2xx over a higher
			// one; otherwise keep the first seen.
			current := statusOf(out[i])
			if isSuccess(status) && (!isSuccess(current) || status < current) {
				out[i] = transaction
			}
			continue
		}
		best[k] = len(out)
		out = append(out, transaction)
	}
	return out
}

// operationKey identifies an operation independently of which response
// variant a transaction is: the URI template when there is one (it names the
// operation, not the expanded example), else the URI.
func operationKey(transaction compile.Transaction) string {
	if transaction.Request.Template != "" {
		return transaction.Request.Template
	}
	return transaction.Request.URI
}

func statusOf(transaction compile.Transaction) int {
	n, err := strconv.Atoi(strings.TrimSpace(transaction.Response.Status))
	if err != nil {
		return 0
	}
	return n
}

func isSuccess(status int) bool { return status >= 200 && status < 300 }

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

// bodyMediaType is the request's bare Content-Type — parameters such as
// charset and boundary removed — or empty when none is stated, which
// generation treats as JSON.
func bodyMediaType(request compile.Request) string {
	for _, header := range request.Headers {
		if !strings.EqualFold(header.Name, "Content-Type") {
			continue
		}
		return strings.ToLower(strings.TrimSpace(strings.SplitN(header.Value, ";", 2)[0]))
	}
	return ""
}
