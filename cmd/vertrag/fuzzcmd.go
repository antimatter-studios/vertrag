package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/fuzz"
	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

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
// description gave the body a schema, every parameter that carries one, and
// every GraphQL argument the query passes.
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

	// A GraphQL request carries no body schema and no parameters; what it does
	// carry is one schema per ARGUMENT, built from the argument's own type. The
	// value goes into the query's `variables`, which is the only part of the
	// request a generated value may occupy — the document itself is vertrag's
	// and a value substituted into it would be a different query.
	for _, argument := range request.GraphQLArguments {
		if strings.TrimSpace(argument.Schema) == "" {
			continue
		}
		subject := fuzz.Subject{
			In: fuzz.InArgument, Name: argument.Name,
			Where: argument.Field, Possessed: argument.Possessed,
		}

		schema, err := decodeSchema(argument.Schema)
		if err != nil {
			unreadable = append(unreadable, subject)
			continue
		}
		targets = append(targets, target{
			subject: subject,
			schema:  schema,
			apply: func(r compile.Request, value any) (compile.Request, error) {
				return r.SetGraphQLArgument(argument, value)
			},
		})
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

// pinnable lists everything in a run a pin could name: the schema of every
// generated body, and the name of every generated GraphQL argument.
//
// Both phases collect it identically because both check the pin identically. A
// pin that engaged on one phase and matched nothing on the other would be a
// pin only half the time, and the half it was not is the half nobody watches.
func pinnable(transactions []compile.Transaction) ([]generate.Schema, []string) {
	var bodies []generate.Schema
	var arguments []string
	for _, transaction := range transactions {
		targets, _ := probeTargets(transaction.Request)
		for _, t := range targets {
			switch t.subject.In {
			case fuzz.InBody:
				bodies = append(bodies, t.schema)
			case fuzz.InArgument:
				arguments = append(arguments, t.subject.Name)
			}
		}
	}
	return bodies, arguments
}

// pinScope names what a run is holding the pin in, so the line it prints
// describes the run in front of it rather than the common case.
func pinScope(bodies []generate.Schema, arguments []string) string {
	switch {
	case len(bodies) == 0:
		return "argument"
	case len(arguments) == 0:
		return "body"
	}
	return "body and argument"
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
	// The pin is checked against every body in the run before the first request
	// goes out. A pin that matches nothing is the failure this guards: it reads
	// exactly like a safety control and holds nothing, so the run would send
	// generated values into the field the caller believed was fixed. Checking
	// after the first request would be checking too late.
	if len(options.Pin) > 0 {
		bodies, arguments := pinnable(transactions)
		if err := fuzz.CheckPins(options.Pin, bodies, arguments); err != nil {
			return nil, err
		}
		options.Engaged = map[string]int{}
		fmt.Printf("pinned in every generated %s: %s\n", pinScope(bodies, arguments), options.Pin.Describe())
	}
	if len(options.Accept) > 0 {
		options.Suppression = &fuzz.Suppression{}
	}

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
		base := baselineWorks(ctx, engine, transaction, options.Pin)
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
					return skipAware(engine.SendGenerated(ctx, attempt))
				}

				var finding fuzz.Finding
				var found bool
				switch target.subject.In {
				case fuzz.InBody:
					finding, found = fuzz.ProbeBody(ctx, target.media, target.schema, mode, send, options)
				case fuzz.InArgument:
					finding, found = fuzz.ProbeArgument(ctx, target.subject, target.schema, mode, send, options)
				default:
					finding, found = fuzz.ProbeParameter(ctx, target.subject, target.schema, mode, send, options)
				}

				switch {
				case !found:
					results = append(results, runner.Result{
						Name:    probeName,
						Status:  runner.StatusPass,
						Request: sentAs(engine, transaction, transaction.Request),
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
						Request: sentAs(engine, transaction, failed),
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
					return skipAware(engine.SendGenerated(ctx, attempt))
				}

				finding, found := fuzz.ProbeWhole(ctx, parts, mode, sendWhole, options)
				if !found {
					results = append(results, runner.Result{
						Name: wholeName, Status: runner.StatusPass,
						Request: sentAs(engine, transaction, transaction.Request),
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
					Request: sentAs(engine, transaction, failed), Errors: []string{finding.Message},
				})
			}
		}
	}

	fmt.Printf("\n%d operation(s) probed over %d body, parameter and argument target(s), %d request(s) sent, %d finding(s)",
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
	// Both of these are reported unconditionally once configured, including
	// when the count is zero, because zero is the answer that matters. A pin
	// engaging nowhere and an acceptance list excusing nothing both look
	// exactly like a clean run from the outside, and only one of those is what
	// the caller thinks they configured.
	if len(options.Pin) > 0 {
		for _, name := range options.Pin.Names() {
			fmt.Printf(", `%s` held on %d generated body(s)", name, options.Engaged[name])
		}
	}
	if options.Suppression != nil {
		fmt.Printf(", %d answer(s) excused by fuzz.accept", options.Suppression.Total)
		if detail := options.Suppression.Describe(); detail != "" {
			fmt.Printf(" (%s)", detail)
		}
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
	// %v rather than %s: a body is text, but a parameter can be a list and a
	// GraphQL argument can be a number, a boolean or an object. %s renders
	// those as `%!s(int64=5)`, which is the line the reader most needs.
	fmt.Printf("  %s %v\n", paint(reporter.Dim, finding.Subject.Describe()+":"), finding.Value)
	fmt.Printf("  %s %s\n", paint(reporter.Dim, "status: "), finding.Status)
	if repro := reporter.Curl(sentAs(engine, transaction, request)); repro != "" {
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
	if repro := reporter.Curl(sentAs(engine, transaction, request)); repro != "" {
		fmt.Printf("  %s %s\n", paint(reporter.Dim, "repro: "), repro)
	}
}

// sentAs renders a compiled request the way the runner sends it, so the
// reproduction line carries the real address and the run-wide headers instead
// of the fragment the description states.
func sentAs(engine *runner.Runner, source compile.Transaction, request compile.Request) runner.Request {
	// Built by the runner rather than rebuilt here, because rebuilding it got
	// it wrong — and got it wrong in the way that matters most.
	//
	// This used to assemble the reported request from the transaction's own
	// headers plus the run-wide `--header` list. It never consulted the
	// credential or the conditional headers, so a probe's repro line omitted
	// exactly the headers the RUN adds and the reader does not know about. The
	// consequence is the worst a report can have: a curl line that does not
	// reproduce. A real project ran one character for character, got a
	// different status from the one the finding claimed, and reasonably
	// concluded the tool had invented the finding — while vertrag had in fact
	// sent a header that the line did not mention.
	//
	// Prepare is what the runner itself calls before sending, and SentRequest
	// is the request as it actually went out. One construction, so the report
	// and the wire cannot disagree.
	source.Request = request
	return engine.PrepareGenerated(source).SentRequest()
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
	// operationOf, not a second notion of the same thing. This used to declare
	// its own key type; the undocumented-status ledger needs exactly the same
	// identity, and two spellings of "which operation is this" would sooner or
	// later disagree about a GraphQL field or a URI template — with one of
	// them silently probing the wrong variant and the other blaming the wrong
	// operation for a status.
	best := map[operation]int{} // index into out
	var out []compile.Transaction

	for _, transaction := range transactions {
		k := operationOf(transaction)
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
	// A GraphQL transaction's operation is its root field, not its path.
	// Every one of them is a POST to the same path, so keying on the path
	// collapses a whole schema into a single operation — which showed up as
	// `vertrag fuzz` reporting that it had passed over one transaction where
	// the schema had offered several.
	if transaction.GraphQL != nil {
		return transaction.Name
	}
	if transaction.Request.Template != "" {
		return transaction.Request.Template
	}
	return transaction.Request.URI
}

// statusOf is the status a transaction expects, as a number.
//
// A range answers with the lowest code in its band, so an operation documented
// as `2XX` is recognised as the success path it is. Reading the text as a
// number gave it zero, and zero is not a success — so probing quietly refused
// to fuzz every operation whose author wrote a band instead of a code.
func statusOf(transaction compile.Transaction) int {
	return validate.StatusBandBase(transaction.Response.Status)
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
//
// The pin applies to this request too, and that is not a detail. This is a real
// request the probing phase sends on its own initiative, one per operation, and
// it carries the description's own example body. An end-to-end test caught it
// leaking: with `dry_run` pinned, 170 of 171 requests were held and one was not
// — the one that had never been generated, so nothing on the generation path
// could have held it. A safety interlock is judged by the request that gets
// through, not by the ones that do not.
func baselineWorks(ctx context.Context, engine *runner.Runner, transaction compile.Transaction, pin fuzz.Pins) baseline {
	if pinned, ok := pinnedBody(transaction.Request, pin); ok {
		transaction.Request = pinned
	}
	reply, err := engine.Send(ctx, transaction)
	if err != nil {
		return baseline{}
	}

	status, err := strconv.Atoi(strings.TrimSpace(reply.StatusCode))
	if err != nil {
		return baseline{}
	}

	// A GraphQL endpoint answers 200 to its own refusals, so the status alone
	// would call every broken operation a working one — and the whole point of
	// the baseline is to know which operations work before anything is blamed
	// on a generated value. It matters most for exactly the operations this
	// round added: `userById` is sent with an id vertrag invented, and on most
	// servers that id names nothing, which is the operation NOT working as
	// documented and every valid-mode finding against it being unattributable.
	if transaction.GraphQL != nil {
		working := status == expectedStatus(transaction) && !graphqlErrored(reply.Body)
		return baseline{ok: working, refused: refused(status, expectedStatus(transaction))}
	}

	// Judged against what the description promised rather than against 2xx: an
	// operation documented as returning 404 is working when it returns one.
	//
	// A documented RANGE is judged as a range. It parses as no number at all,
	// so it used to fall through to the guess below — and "anything under 400
	// is fine" accepts a 302 the document never mentioned as a healthy
	// baseline, which is exactly the pretence the guess exists to admit to.
	if validate.IsStatusRange(transaction.Response.Status) {
		return baseline{
			ok:      validate.StatusMatches(transaction.Response.Status, reply.StatusCode),
			refused: refused(status, validate.StatusBandBase(transaction.Response.Status)),
		}
	}
	expected, err := strconv.Atoi(strings.TrimSpace(transaction.Response.Status))
	switch {
	case err != nil:
		return baseline{ok: status < 400, refused: refused(status, 0)}
	default:
		return baseline{ok: status == expected, refused: refused(status, expected)}
	}
}

// expectedStatus is the status the description promised, or 0 when it promised
// nothing readable.
func expectedStatus(transaction compile.Transaction) int {
	status, err := strconv.Atoi(strings.TrimSpace(transaction.Response.Status))
	if err != nil {
		return 0
	}
	return status
}

// graphqlErrored reports whether a GraphQL reply carried errors.
func graphqlErrored(body string) bool {
	var document struct {
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		return false
	}
	return len(document.Errors) > 0
}

// pinnedBody holds the pinned fields in a request's own body.
//
// Only JSON is handled, and anything that does not parse as a JSON object is
// left exactly as it was rather than guessed at. A form-encoded or multipart
// body edited by string substitution is a corrupt body, and sending one would
// turn a safety feature into a source of findings about vertrag.
//
// The narrowness is worth stating plainly rather than hiding: a project whose
// dangerous endpoint takes a non-JSON body does not get this protection on the
// baseline request, and should keep that operation out of the probing phases
// with `skip`.
func pinnedBody(request compile.Request, pin fuzz.Pins) (compile.Request, bool) {
	if len(pin) == 0 || strings.TrimSpace(request.Body) == "" {
		return request, false
	}
	if len(request.GraphQLArguments) > 0 {
		return pinnedArguments(request, pin)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return request, false
	}

	held := false
	for _, name := range pin.Names() {
		// Only a field the body already carries, for the same reason Pins.Apply
		// only sets a declared property: inventing one would make a documented
		// example into something the description never described.
		if _, present := body[name]; !present {
			continue
		}
		body[name] = pin[name]
		held = true
	}
	if !held {
		return request, false
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return request, false
	}
	request.Body = string(encoded)
	return request, true
}

// pinnedArguments holds the pinned arguments in a GraphQL request's variables.
//
// It differs from pinnedBody in the one way that matters, and the difference is
// deliberate rather than an inconsistency. pinnedBody replaces only a field the
// example already carries, because inventing one would make a documented
// example into something the description never described. A GraphQL argument
// left out of `variables` is not absent from the request — it is the server's
// own default, and `dryRun: Boolean = false` is exactly the shape of default a
// pin exists to override. So a pinned argument the field DECLARES is set
// whether or not the compiled example gave it a value, and one the field does
// not declare is still left alone.
func pinnedArguments(request compile.Request, pin fuzz.Pins) (compile.Request, bool) {
	held := false
	for _, argument := range request.GraphQLArguments {
		value, pinned := pin[argument.Name]
		if !pinned {
			continue
		}
		updated, err := request.SetGraphQLArgument(argument, value)
		if err != nil {
			continue
		}
		request = updated
		held = true
	}
	return request, held
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

// wantedModes turns the mode list the fuzz phase takes into the set the
// coverage phase takes. The two phases were separate commands with separate
// flag parsing until they became phases of one run, and this is the last of
// that difference — kept as a conversion rather than changed in one of them,
// because both shapes are the natural one for their own loop.
func wantedModes(modes []generate.Mode) map[generate.Mode]bool {
	wanted := make(map[generate.Mode]bool, len(modes))
	for _, mode := range modes {
		wanted[mode] = true
	}
	return wanted
}

// pinReach says how much of the description each pinned name actually reaches.
//
// It exists so the safety property is checkable from a cold start, without
// sending the requests the pin is there to guard. A peer hit that exactly: the
// only way to learn whether the interlock had engaged was to fire the thing it
// interlocks, which makes "confirm the pin engaged before you run" impossible
// to satisfy in the order it has to be satisfied in.
//
// It counts declarations rather than applications, and those are different
// numbers — a body declaring the field is a body the pin WILL hold, but only
// the run itself knows how many were drawn. A zero here is decisive though,
// which is what makes it worth printing before anything is sent.
func pinReach(pins fuzz.Pins, bodies []generate.Schema, arguments []string) string {
	parts := make([]string, 0, len(pins))
	for _, name := range pins.Names() {
		reached := 0
		for _, body := range bodies {
			if pins.Covers(body, name) {
				reached++
			}
		}
		for _, argument := range arguments {
			if argument == name {
				reached++
			}
		}
		parts = append(parts, fmt.Sprintf("%s: %d of %d", name, reached, len(bodies)+len(arguments)))
	}
	return strings.Join(parts, ", ")
}

// unpinnedMutations warns when a probing phase will generate values for
// operations that change something, and nothing has been pinned.
//
// The hazard was documented and the control was opt-in, which is the same shape
// as the bug this tool keeps finding in other people's code: a safe path that
// has to be remembered is not a safe path. The advice being given until now was
// "point it at a sandbox that cannot reach anything real" — good advice, and
// advice is exactly the thing an operator can skip, forget, or never read.
//
// A peer's user put it plainly, about their own setup rather than about this:
// needing to construct a throwaway instance for the occasion is a hole in the
// testing strategy, not a precaution, because the safe thing had to be built
// and the unsafe thing was the default. The same criticism applies here, and
// this is the cheapest structural answer — the hazard announces itself at the
// moment it applies, naming what it counted, rather than sitting in a document.
//
// It is one line and only when all three conditions hold, so a read-only API
// and a pinned run both stay silent. Silence here means "nothing generated will
// reach a mutating operation", which is information rather than absence.
func unpinnedMutations(settings config.Config, transactions []compile.Transaction) string {
	probing := false
	for _, phase := range settings.Phases {
		if phase == config.PhaseCoverage || phase == config.PhaseFuzz {
			probing = true
		}
	}
	if !probing {
		return ""
	}

	mutating := map[string]bool{}
	for _, transaction := range transactions {
		switch strings.ToUpper(transaction.Request.Method) {
		case "POST", "PUT", "PATCH", "DELETE":
			targets, _ := probeTargets(transaction.Request)
			if len(targets) > 0 {
				mutating[transaction.Request.Method+" "+transaction.Request.URI] = true
			}
		}
	}
	if len(mutating) == 0 {
		return ""
	}

	return fmt.Sprintf(
		"%d operation(s) that change something will be sent generated values, and `fuzz.pin` "+
			"declares nothing. Generation sends whatever the schema permits, including the value "+
			"that makes a request real — and it cannot tell which one that is. Pin the field that "+
			"decides, `skip` the operation, or point this at an endpoint whose STATE is disposable "+
			"as well as its side effects: a probing run writes records too, and a poisoned journal "+
			"outlives the run that wrote it",
		len(mutating))
}
