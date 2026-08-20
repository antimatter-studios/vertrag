package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/fuzz"
	"github.com/antimatter-studios/vertrag/link"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/server"
	"github.com/antimatter-studios/vertrag/shape"
)

// runFlags is everything `vertrag run` accepts on the command line.
//
// Named rather than a row of locals so that reading the settings and applying
// them are separate jobs — the merge below has one rule, and it is easier to
// see that it is applied consistently when the inputs arrive as one value.
type runFlags struct {
	configPath        string
	endpoint          string
	dryRun            bool
	details           bool
	noColor           bool
	sorted            bool
	sequence          bool
	checkHeaderSchema bool
	checkIgnoredAuth  bool
	maxResponseTime   time.Duration
	workers           int
	reporterName      string
	output            string

	headers      stringList
	only         stringList
	methods      stringList
	tags         stringList
	operationIDs stringList

	onlyMatching        stringList
	exclude             stringList
	excludeMatching     stringList
	excludeMethods      stringList
	excludeTags         stringList
	excludeOperationIDs stringList

	maxFailures  int
	failFast     bool
	noSanitize   bool
	sanitizeHdrs stringList
	phases       string

	transport transportFlags

	// The probing settings, which were `vertrag fuzz`'s and `vertrag
	// coverage`'s own flags until those commands became phases.
	cases        int
	seed         uint64
	maxTime      time.Duration
	mode         string
	wholeRequest bool
	graphql      graphqlFlags

	// positional are the arguments left after the flags: a description and an
	// endpoint, either of which a config file may supply instead.
	positional []string
}

func parseRunFlags(args []string) (runFlags, error) {
	var f runFlags

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.StringVar(&f.configPath, "config", "", "path to a vertrag.yml (default: the first of vertrag.yml, vertrag.yaml found here)")
	fs.StringVar(&f.endpoint, "endpoint", "", "base URL of the server under test")
	fs.BoolVar(&f.dryRun, "dry-run", false, "compile and report the transactions without sending them")
	fs.BoolVar(&f.details, "details", false, "print the request and response of passing transactions too")
	fs.BoolVar(&f.noColor, "no-color", false, "disable coloured output")
	fs.BoolVar(&f.sorted, "sorted", false, "run transactions grouped by method rather than in document order")
	fs.BoolVar(&f.sequence, "sequence", false, "order the run by the links the description declares, filling each step's parameters from the response of the step it follows")
	fs.BoolVar(&f.checkHeaderSchema, "check-header-schema", false, "validate response header values against the schemas the description gives them")
	fs.BoolVar(&f.checkIgnoredAuth, "check-ignored-auth", false, "re-send each authenticated request without the credential and report any endpoint that answers it anyway")
	fs.DurationVar(&f.maxResponseTime, "max-response-time", 0, "report any response that took longer than this to arrive, e.g. 750ms; --delay and retry backoff are not counted (0 = do not time them)")
	fs.IntVar(&f.workers, "workers", 1, "send this many transactions at once; refused with --sequence or hooks, which are ordering contracts")
	fs.StringVar(&f.reporterName, "reporter", "", "output format: cli, dot, markdown, html, junit, har or vcr (overrides the config)")
	fs.StringVar(&f.output, "output", "", "write the report to a file instead of stdout")
	fs.Var(&f.headers, "header", "extra header to send with every request, as 'Name: value' (repeatable)")
	fs.Var(&f.only, "only", "run only the named transaction (repeatable)")
	fs.Var(&f.methods, "method", "run only transactions using this method (repeatable)")
	fs.Var(&f.tags, "tag", "run only transactions whose operation carries this tag (repeatable)")
	fs.Var(&f.operationIDs, "operation-id", "run only transactions of this operationId (repeatable)")
	fs.Var(&f.onlyMatching, "only-matching", "run only transactions whose name matches this regular expression (repeatable)")
	fs.Var(&f.exclude, "exclude", "leave out the named transaction, whatever else selected it (repeatable)")
	fs.Var(&f.excludeMatching, "exclude-matching", "leave out transactions whose name matches this regular expression (repeatable)")
	fs.Var(&f.excludeMethods, "exclude-method", "leave out transactions using this method (repeatable)")
	fs.Var(&f.excludeTags, "exclude-tag", "leave out transactions whose operation carries this tag (repeatable)")
	fs.Var(&f.excludeOperationIDs, "exclude-operation-id", "leave out transactions of this operationId (repeatable)")
	fs.IntVar(&f.maxFailures, "max-failures", 0, "stop sending after this many failures or errors; the rest are reported as skipped (0 = never)")
	fs.BoolVar(&f.failFast, "fail-fast", false, "stop at the first failure — the same as --max-failures 1")
	fs.BoolVar(&f.noSanitize, "no-sanitize", false, "show credential header values in reports instead of <redacted>")
	fs.Var(&f.sanitizeHdrs, "sanitize-header", "also redact this header's value in reports (repeatable)")
	fs.StringVar(&f.phases, "phases", "", "what to run, comma-separated: examples (always), coverage, fuzz, stateful — e.g. examples,stateful")
	fs.IntVar(&f.cases, "cases", 0, "fuzz: distinct values to try per body, parameter and argument (0 = the default 50)")
	fs.Uint64Var(&f.seed, "seed", 0, "fuzz: replay a previous run (0 picks one and reports it)")
	fs.DurationVar(&f.maxTime, "max-time", 0, "probing: stop after this long, e.g. 2m; what was not reached is reported as skipped (0 = no limit)")
	fs.StringVar(&f.mode, "mode", "", "probing: which values to send — valid, invalid, or both (the default)")
	fs.BoolVar(&f.wholeRequest, "whole-request", false, "fuzz: also draw every parameter and the body together per case, to reach bugs in their interaction")
	addTransportFlags(fs, &f.transport)
	addGraphQLFlags(fs, &f.graphql)

	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return f, err
	}
	f.positional = positional
	f.transport.noteGiven(fs)
	return f, nil
}

// parseInterspersed parses flags that appear before, after or between the
// positional arguments, and returns the positional ones.
//
// Go's flag package stops at the first argument that is not a flag, so
// `vertrag run api.yml http://host --details` puts `--details` in the
// positional list and silently ignores it. Dredd accepts flags anywhere, its
// own documentation writes them last, and a flag that is quietly dropped is
// indistinguishable from one that had no effect.
//
// The loop is the standard idiom: parse, take the first leftover as positional,
// parse again from what follows. Flag values are still consumed by Parse, so
// `--only NAME` keeps its argument rather than NAME becoming positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// settingsFor merges the config file with the command line.
//
// One rule throughout: the file records what a project normally does, the flags
// what this run should do instead, so a flag that was given wins. The boolean
// flags are one-way for the same reason — `--no-color` says "not this time" and
// there is no spelling of a flag that means "go back to whatever the file said".
func settingsFor(f runFlags) (config.Config, error) {
	settings, err := resolveConfig(f.configPath, f.positional)
	if err != nil {
		return settings, err
	}

	if f.endpoint != "" {
		noteOverride(&settings, "the endpoint", f.endpoint, settings.Endpoint)
		settings.Endpoint = f.endpoint
	}
	if f.dryRun {
		settings.DryRun = true
	}
	if f.details {
		settings.Details = true
	}
	if f.noColor {
		settings.Color = false
	}
	if f.sorted {
		settings.Sorted = true
	}
	if f.checkHeaderSchema {
		settings.Checks.HeaderSchema = true
	}
	if f.checkIgnoredAuth {
		settings.Checks.IgnoredAuth = true
	}
	if f.maxResponseTime > 0 {
		settings.Checks.MaxResponseTime = f.maxResponseTime
	}
	if f.workers > 1 {
		settings.Workers = f.workers
	}
	settings.Header = append(settings.Header, f.headers...)
	settings.Only = append(settings.Only, f.only...)
	settings.Method = append(settings.Method, f.methods...)
	settings.Tag = append(settings.Tag, f.tags...)
	settings.OperationID = append(settings.OperationID, f.operationIDs...)
	settings.OnlyMatching = append(settings.OnlyMatching, f.onlyMatching...)
	settings.Exclude = append(settings.Exclude, f.exclude...)
	settings.ExcludeMatching = append(settings.ExcludeMatching, f.excludeMatching...)
	settings.ExcludeMethod = append(settings.ExcludeMethod, f.excludeMethods...)
	settings.ExcludeTag = append(settings.ExcludeTag, f.excludeTags...)
	settings.ExcludeOperationID = append(settings.ExcludeOperationID, f.excludeOperationIDs...)
	if f.maxFailures > 0 {
		settings.MaxFailures = f.maxFailures
	}
	if f.failFast {
		settings.MaxFailures = 1
	}
	if f.cases > 0 {
		settings.Fuzz.Cases = f.cases
	}
	if f.seed != 0 {
		settings.Fuzz.Seed = f.seed
	}
	if f.wholeRequest {
		settings.Fuzz.WholeRequest = true
	}
	f.transport.apply(&settings.Transport)
	f.graphql.apply(&settings.GraphQL)
	reporter.SetSanitize(!f.noSanitize)
	for _, name := range f.sanitizeHdrs {
		reporter.AddRedactedHeader(name)
	}
	if f.phases != "" {
		phases, err := config.NormalisePhases(strings.Split(f.phases, ","))
		if err != nil {
			return settings, err
		}
		settings.Phases = phases
	}

	// A reporter named on the command line replaces the file's list rather than
	// adding to it: someone asking for one format wants that format, not it and
	// whatever else was configured.
	if f.reporterName != "" {
		settings.Reporters = []string{f.reporterName}
		settings.Outputs = nil
	}
	if f.output != "" {
		settings.Outputs = []string{f.output}
	}

	if err := settings.Validate(); err != nil {
		return settings, err
	}
	return settings, nil
}

// runRun is `vertrag run`: read a description, derive the transactions, send
// them at a server, and report what came back.
func runRun(args []string) error {
	flags, err := parseRunFlags(args)
	if err != nil {
		return err
	}

	settings, err := settingsFor(flags)
	if err != nil {
		return err
	}

	report, closeReport, err := newReporter(settings)
	if err != nil {
		return err
	}
	defer closeReport()

	// Diagnostics about the description go to the terminal whatever the report
	// format is: they are about the document, not the run.
	annotations := reporter.CLI{Out: os.Stdout, Color: settings.Color && flags.output == ""}

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

	result, withheld, err := transactionsFor(source, settings.Spec, settings)
	if err != nil {
		return err
	}
	// What a GraphQL schema offered and this run is not sending. Said before
	// the results rather than after them, and on every run rather than only
	// when something fails: a run that quietly tested eleven of nineteen
	// operations reports eleven passes, and reads as success.
	for _, note := range withheld {
		fmt.Fprintf(os.Stderr, "vertrag: %s\n", note)
	}
	for _, note := range graphqlPhaseNotes(result.MediaType, settings.Phases) {
		fmt.Fprintf(os.Stderr, "vertrag: %s\n", note)
	}

	annotations.Annotations(toAnnotations(result.Annotations))
	if hasErrors(result.Annotations) {
		return fmt.Errorf("the API description could not be read; nothing was run")
	}

	// Everything the DESCRIPTION defines, kept apart from what this run
	// selects, because two checks need the difference and both would be wrong
	// without it: what an operation documents is a fact about the document, so
	// a run limited to one tag must not conclude that the responses it left out
	// were never written down — and a link naming an operation the run filtered
	// away is not a link naming an operation the description lacks.
	compiled := stripAPIName(result.Transactions)

	statuses := newStatusLedger(compiled)

	selected, unmatched, err := filterTransactions(compiled, settings)
	if err != nil {
		return err
	}
	for _, report := range unmatched {
		fmt.Fprintf(os.Stderr, "vertrag: %s\n", report)
	}

	transactions := sortTransactions(selected, settings.Sorted)
	if len(transactions) == 0 {
		fmt.Fprintln(os.Stdout, "No transactions to run.")
		return nil
	}

	reportMissingCredentials(transactions, settings.Header)

	// The pin is validated whenever it is declared, whatever phases this run
	// will do — including none of the probing ones.
	//
	// It used to be checked inside the probing phases, which meant a typo'd pin
	// passed silently through a plain `vertrag run`: all the transactions ran,
	// no warning, normal exit. A peer found it by changing `dry_run` to
	// `dry_runn` and watching nothing happen. `run` is the command most people
	// type, so a configuration that reads exactly like a safety control sat
	// there unvalidated in the most-used entry point — present, inert and
	// silent, in the tool whose own documentation makes that argument.
	//
	// Validating costs one pass over the compiled transactions and no requests,
	// so there is no reason to make it conditional on anything.
	if len(settings.Fuzz.Pin) > 0 {
		pins := fuzz.Pins(settings.Fuzz.Pin)
		bodies, arguments := pinnable(transactions)
		if err := fuzz.CheckPins(pins, bodies, arguments); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "pin: %s — declared by %s\n",
			pins.Describe(), pinReach(pins, bodies, arguments))
	} else if warning := unpinnedMutations(settings, transactions); warning != "" {
		fmt.Fprintf(os.Stderr, "vertrag: %s\n", warning)
	}

	if settings.DryRun {
		for _, transaction := range transactions {
			fmt.Fprintf(os.Stdout, "skip: %s %s %s\n",
				transaction.Request.Method, transaction.Request.URI, transaction.Name)
		}
		fmt.Fprintf(os.Stdout, "\n%d transaction(s), none sent (dry run)\n", len(transactions))
		return nil
	}

	// A run should stop promptly on Ctrl-C rather than working through every
	// remaining transaction.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The server under test comes up before anything else touches it, which
	// includes the engine: `auth.login` sends a request of its own before the
	// first transaction does.
	stopServer, err := startServer(ctx, settings)
	if err != nil {
		return err
	}
	defer stopServer()

	engine, err := newEngine(settings)
	if err != nil {
		return err
	}
	engine.ExtraHeaders = settings.Header

	if err := applyConfiguredRules(ctx, engine, settings, transactions); err != nil {
		return err
	}

	// Installed after the rules, so the request that OBTAINS the credential is
	// not counted: `auth.login` sends its own exchange, judged by the auth
	// package against what it needs rather than against the description, and a
	// 401 from a login attempt is the credential being wrong, not the document.
	// Everything the run sends from here on goes through the runner, and the
	// runner calls this from the one place that touches the wire.
	engine.AddObserver(statuses.observe)
	// The examples phase always runs and always runs first, so the ledger opens
	// in it rather than defaulting to a phase name nothing chose.
	statuses.phase(config.PhaseExamples)

	// Concurrency is refused rather than silently ignored where it would break
	// an ordering contract: a sequenced step takes its values from another
	// step's response, and a hook worker is one process handling one
	// transaction at a time. Saying so is the difference between a run that
	// went slower than asked and a run the reader believes went faster.
	if settings.Workers > 1 {
		switch {
		case flags.sequence:
			fmt.Fprintln(os.Stderr, "vertrag: workers is ignored with --sequence, which orders the run by the links between transactions")
		case len(settings.Hookfiles) > 0:
			fmt.Fprintln(os.Stderr, "vertrag: workers is ignored while hookfiles are loaded, since the worker process handles one transaction at a time")
		default:
			engine.Workers = settings.Workers
		}
	}

	// Sequencing is opt-in because it reorders a run, and a suite whose hooks
	// were written against document order would notice. It is not exploratory —
	// the plan is fixed by the description, so the same document always
	// produces the same order — which is why it belongs on `run` rather than
	// in a mode of its own.
	//
	// The sequencer outlives the block because the link check reads its
	// exchanges afterwards: they are the only record of the values a link put
	// INTO a request, which is what `$request.path.id` means in a sequenced
	// run.
	var sequencer *link.Sequencer
	if flags.sequence {
		sequencer = link.NewSequencer(transactions)
		for _, note := range sequencer.Notes() {
			fmt.Fprintf(os.Stderr, "vertrag: %s\n", note)
		}
		engine.Plan = sequencer
	}

	engine.Checks = runner.Checks{
		ServerError:     settings.Checks.ServerError,
		ContentType:     settings.Checks.ContentType,
		HeaderSchema:    settings.Checks.HeaderSchema,
		IgnoredAuth:     settings.Checks.IgnoredAuth,
		MaxResponseTime: settings.Checks.MaxResponseTime,
	}

	// Hook files run in a worker process. Starting it is a hard failure: a
	// suite whose hooks did not load would authenticate nothing and skip
	// nothing, and report a wall of failures that say nothing about the API.
	stopHooks, err := startHooks(ctx, engine, settings)
	if err != nil {
		return err
	}
	defer stopHooks()

	// The pin reaches the documented examples too, and it has to.
	//
	// This was written down as a limitation — "the examples phase sends the
	// description's own example, unchanged" — and it was survivable while
	// probing was a separate command, because a pinned `vertrag fuzz` sent no
	// examples at all. Folding the probing phases into `run` removed that
	// accident: examples always runs first, so a pinned run would have sent one
	// UNPINNED documented body per operation before any generated one. For an
	// API where the pinned field is what makes a request real, that is one real
	// request per operation, introduced by a refactor whose entire argument was
	// that one code path is safer than two. The test that had proved the pin
	// held caught it, reporting 1 of 18.
	//
	// So the interlock is applied here as well, and it says when it changed
	// something. A caller who has declared that a field must never be sent as
	// anything else has said something about every request, not only the
	// generated ones — and an example that contradicts the pin is worth
	// knowing about rather than silently obeying or silently overriding.
	if len(settings.Fuzz.Pin) > 0 {
		pins := fuzz.Pins(settings.Fuzz.Pin)
		held := 0
		for i := range transactions {
			if pinned, changed := pinnedBody(transactions[i].Request, pins); changed {
				transactions[i].Request = pinned
				held++
			}
		}
		if held > 0 {
			fmt.Fprintf(os.Stderr,
				"vertrag: %d documented example(s) set a pinned field to something else; the pin holds. "+
					"An example that contradicts the pin is worth fixing in the description\n", held)
		}
	}

	// One recorder for the whole run, because the finding it exists to make
	// only exists across phases.
	//
	// A FastAPI service that translates a domain error to 422 answers the
	// examples phase with `detail` as a string and the probing phases with
	// `detail` as an array — same operation, same status, same media type, and
	// nothing in the description saying which a client will get. Neither
	// response is wrong on its own, so neither phase can see it; only a run
	// that remembers both can. Recording it costs a JSON parse per response and
	// no requests.
	shapes := &shape.Recorder{}
	// Registered once, reading currentPhase, which the phase loop sets. See
	// the comment there for why a plain variable is safe.
	currentPhase := config.PhaseExamples
	engine.AddObserver(observing(shapes, func() string { return currentPhase }))

	results, err := engine.Run(ctx, transactions)
	if err != nil {
		return err
	}
	examplesPassed := passed(results)

	// Every Link Object the description declares, resolved against the
	// responses that just arrived.
	//
	// It is here rather than in a phase because it sends nothing: the
	// exchanges are already in hand, so the argument that makes a check
	// opt-in — it doubles the traffic, it needs a server that can be knocked
	// about — does not apply. A link is a written claim about two operations,
	// and checking a written claim is the same job as checking a response
	// against its schema; requiring a flag for it would put the document's own
	// words behind an option nobody knows about.
	//
	// Before this, the only thing that resolved a link was the stateful phase,
	// which needs a create-and-delete lifecycle and asserts things OpenAPI
	// cannot state. The documentary half was gated behind the inferential one.
	linkFindings := false
	if settings.Checks.LinkResolution {
		linkResults, found := runLinkCheck(transactions, compiled, results, sequencer, settings.Color)
		results = append(results, prefixed("links", linkResults)...)
		linkFindings = found
	}

	// The probing phases, when asked for, run over the SAME engine — same
	// auth, same transport, same skips — and land in the same report, so a
	// pipeline gets one file. Their results are named by phase, and their
	// verdict is kept apart from the examples': a documented transaction that
	// no longer passes is a regression to block on; a boundary the server
	// mishandles is a bug to file. Same report, different exit code.
	probeFindings := false
	// One refusals for the whole run: the login operation is the same
	// operation in both phases, and explaining it twice would read as two
	// different things having happened.
	refused := newRefusals(settings)

	// The probing phases probe what the run would SEND, which is not every
	// compiled transaction: an operation the config skipped must not be
	// generated for either.
	//
	// The examples phase gets this from the runner, which reports a skipped
	// transaction as skipped. The probing phases build their own list from the
	// compiled transactions and had no such filter — the standalone commands
	// applied it themselves, and when they became phases the filter was left
	// behind with them. `withoutSkipped` was then a function with no callers,
	// which I saw while deleting the rest of that scaffolding and did not ask
	// why.
	//
	// What it cost: an operation marked `skip` with the reason "must never be
	// sent" took five generated requests. A skip list is where a suite records
	// what it has decided not to touch, and some of those decisions are about
	// what an endpoint DOES — one project skips an operation that would forward
	// a credential to any host a caller names. Generating for it is worse than
	// running it once as documented.
	probeableTransactions, skippedFromProbing := withoutSkipped(transactions, engine.Skip)
	if len(skippedFromProbing) > 0 && len(settings.Phases) > 1 {
		fmt.Fprintf(os.Stderr,
			"vertrag: %d transaction(s) are skipped, so the probing phases do not generate for them\n",
			len(skippedFromProbing))
	}

	// --mode and --max-time were `vertrag fuzz`'s and `vertrag coverage`'s;
	// they mean the same thing here and are read once for both phases.
	modes, err := parseModes(flags.mode)
	if err != nil {
		return err
	}
	maxTime := flags.maxTime

	// The probing phases share one deadline, because --max-time bounds the RUN
	// and not each phase in turn: two phases each given the whole budget is two
	// budgets, and the number somebody wrote was the one they were willing to
	// wait.
	var deadline time.Time
	if maxTime > 0 {
		deadline = time.Now().Add(maxTime)
	}
	for _, phase := range settings.Phases {
		statuses.phase(phase)
		// The shape recorder is registered once, before the loop, and reads
		// this. It cannot be re-registered per phase now that observers are a
		// list — appending one per phase would record every response as many
		// times as there are phases left.
		//
		// A plain assignment is enough for the same reason the observer that
		// wanted reassignment gave: phases are sequential, and every worker of
		// one has finished before the next begins, so nothing reads this while
		// it is being written.
		currentPhase = phase
		switch phase {
		case config.PhaseCoverage:
			probeable, _ := partitionBySchema(probeableTransactions)
			phaseResults, phaseErr := coverAll(ctx, engine, probeable,
				wantedModes(modes), 0, settings.Color, refused,
				fuzz.Options{Pin: settings.Fuzz.Pin, Accept: settings.Fuzz.Accept,
					Workers: settings.Workers, Deadline: deadline})
			results = append(results, prefixed("coverage", phaseResults)...)
			if phaseErr != nil && phaseErr != errFailed {
				return phaseErr
			}
			probeFindings = probeFindings || phaseErr == errFailed
		case config.PhaseStateful:
			phaseResults, phaseErr := runStateful(ctx, engine, transactions, settings.Color)
			results = append(results, prefixed("stateful", phaseResults)...)
			if phaseErr != nil && phaseErr != errFailed {
				return phaseErr
			}
			probeFindings = probeFindings || phaseErr == errFailed
		case config.PhaseFuzz:
			probeable, _ := partitionBySchema(probeableTransactions)
			seed := settings.Fuzz.Seed
			for seed == 0 {
				seed = rand.Uint64()
			}
			// Named with the flag rather than the config key, because the
			// flag is what somebody can paste straight back. It only became
			// the better advice when `run` gained --seed; before that, the
			// phase could only point at the file.
			fmt.Printf("seed: %d (replay with --seed %d)\n", seed, seed)
			phaseResults, phaseErr := probeAll(ctx, engine, probeable,
				modes, 0,
				fuzz.Options{Cases: settings.Fuzz.Cases, Seed: seed, Pin: settings.Fuzz.Pin,
					Accept: settings.Fuzz.Accept, Deadline: deadline},
				settings.Color, settings.Fuzz.WholeRequest, refused)
			results = append(results, prefixed("fuzz", phaseResults)...)
			if phaseErr != nil && phaseErr != errFailed {
				return phaseErr
			}
			probeFindings = probeFindings || phaseErr == errFailed
		}
	}

	report.Report(results)

	// Last, and to the terminal whatever --reporter was asked for, because
	// these are diagnostics about the description rather than results of the
	// run — the same place the annotations go. A junit file describes
	// transactions; these describe the document they came from, and there is no
	// element in that format that means "your API does things you never wrote
	// down". None of them is a finding, so the exit code below cannot see them.
	statuses.report(os.Stdout)
	reportDivergences(os.Stdout, shapes.Divergences(), settings.Color)
	noteIgnoredAuthIsOff(settings, engine, transactions)

	switch {
	case !examplesPassed:
		return errFailed
	case probeFindings || linkFindings:
		return errFindings
	}
	return nil
}

// noteIgnoredAuthIsOff says, once, that this run did not test whether its
// credential mattered.
//
// `--check-ignored-auth` re-sends every authenticated request without the
// credential and reports each endpoint that answers anyway. It is off by
// default for a good reason — it doubles those requests — and the cost of that
// default is that almost nobody knows it exists. At one project it found 56 of
// 117 endpoints answering unauthenticated: both rejection branches in their
// middleware had been commented out for months, in the mock and in the live
// service alike, and it was found only because somebody happened to mention
// the flag. A check nobody knows about is a check nobody runs, which is the
// failure this tool argues against everywhere else.
//
// Said at the END, where a reader is looking at what the run concluded, and in
// one line. It is not printed when there is no credential configured, because
// then there is nothing to withhold and nothing for the check to find — the
// silence has to mean "this was not worth telling you", or it is noise and
// gets filtered.
//
// The count is of requests that actually carried the credential, for the same
// reason: a suite whose every transaction is in `auth.except` has already
// answered the question this line would ask.
func noteIgnoredAuthIsOff(settings config.Config, engine *runner.Runner, transactions []compile.Transaction) {
	if !settings.Auth.Configured() || settings.Checks.IgnoredAuth {
		return
	}

	authenticated := 0
	for _, transaction := range transactions {
		if engine.Auth.Except[transaction.Name] || engine.Auth.GrantedBy(transaction) {
			continue
		}
		authenticated++
	}
	if authenticated == 0 {
		return
	}

	fmt.Fprintf(os.Stderr,
		"vertrag: %d request(s) carried a credential and nothing checked whether it mattered; "+
			"--check-ignored-auth (or `checks.ignored-auth`) re-sends each of them without it and reports "+
			"every endpoint that answers anyway, at the cost of doubling those requests\n", authenticated)
}

// startServer runs the `server:` command, if the config has one, and hands
// back what stops it.
//
// The caller defers that, which is what puts the server down on every way out
// of a run: a clean pass, a failing one, a run cut short by --max-failures, a
// Ctrl-C — which cancels the context and unwinds through the same defer — and
// a panic. The one path nothing can cover is vertrag itself being killed
// outright, because nothing of ours runs then.
//
// Failing to start is a hard failure, for the reason loading hooks is: a suite
// whose server never came up reports a wall of connection errors that say
// nothing about the API, and the cause is a line of somebody's start script
// that this way gets printed instead.
func startServer(ctx context.Context, settings config.Config) (func(), error) {
	if settings.Server == "" {
		return func() {}, nil
	}

	// Said before the wait rather than after it: `server-wait: 30` means a run
	// can sit here for half a minute, and a terminal that has printed nothing
	// for half a minute looks like one that has hung.
	fmt.Fprintf(os.Stderr, "vertrag: starting the server: `%s`\n", settings.Server)

	process, err := server.Start(ctx, server.Options{
		Command:  settings.Server,
		Endpoint: settings.Endpoint,
		Wait:     settings.ServerWait,
	})
	if err != nil {
		return func() {}, err
	}

	return func() {
		if note := process.Stop(); note != "" {
			fmt.Fprintf(os.Stderr, "vertrag: %s\n", note)
		}
	}, nil
}

// passed reports whether every documented transaction passed or was skipped.
func passed(results []runner.Result) bool {
	for _, result := range results {
		if result.Status == runner.StatusFail || result.Status == runner.StatusError {
			return false
		}
	}
	return true
}

// prefixed names probe results by their phase, so a report holding both
// documented transactions and probes reads which is which.
func prefixed(phase string, results []runner.Result) []runner.Result {
	out := make([]runner.Result, 0, len(results))
	for _, result := range results {
		result.Name = phase + ": " + result.Name
		out = append(out, result)
	}
	return out
}

// newReporter builds the reporters the settings ask for, writing each to its
// paired output file or to stdout when it has none.
//
// A report written to a file is for a machine, so it never carries colour.
func newReporter(settings config.Config) (reporter.Reporter, func(), error) {
	var reporters []reporter.Reporter
	var files []*os.File

	closeAll := func() {
		for _, file := range files {
			file.Close()
		}
	}

	for i, name := range settings.Reporters {
		destination := io.Writer(os.Stdout)
		colour := settings.Color

		if i < len(settings.Outputs) && settings.Outputs[i] != "" {
			file, err := os.Create(settings.Outputs[i])
			if err != nil {
				closeAll()
				return nil, closeAll, fmt.Errorf("opening %s: %w", settings.Outputs[i], err)
			}
			files = append(files, file)
			destination, colour = file, false
		}

		switch name {
		case "cli", "":
			reporters = append(reporters, reporter.CLI{
				Out: destination, Color: colour, Details: settings.Details})
		case "dot":
			reporters = append(reporters, reporter.Dot{Out: destination, Color: colour})
		case "markdown", "md":
			reporters = append(reporters, reporter.Markdown{Out: destination, Details: settings.Details})
		case "html":
			reporters = append(reporters, reporter.HTML{Out: destination, Details: settings.Details})
		case "junit", "xunit":
			// The report is what a pipeline archives, so it carries the run's
			// provenance — the terminal has the signature line, the XML has this.
			reporters = append(reporters, reporter.JUnit{Out: destination, Run: provenance(settings)})
		case "har":
			// A cassette is traffic rather than verdicts, so it carries the
			// same provenance for a stronger reason than the JUnit file does:
			// a recording says what a server answered, and a recording that
			// cannot name the server is a set of answers with no question.
			reporters = append(reporters, reporter.HAR{Out: destination, Run: provenance(settings)})
		case "vcr", "cassette":
			reporters = append(reporters, reporter.VCR{Out: destination, Run: provenance(settings)})
		default:
			closeAll()
			return nil, closeAll, fmt.Errorf(
				"unknown reporter %q; vertrag has cli, dot, markdown, html, junit, har and vcr", name)
		}
	}

	if len(reporters) == 0 {
		reporters = append(reporters, reporter.CLI{Out: os.Stdout, Color: settings.Color})
	}
	return reporter.Multi(reporters), closeAll, nil
}

// errFailed reports a run whose tests failed, as opposed to one that could not
// be carried out. The caller turns it into a non-zero exit status without
// printing it as an error message.
var errFailed = fmt.Errorf("some transactions failed")

// errFindings reports a run whose documented transactions all passed but which
// found something anyway: a probing phase, or a link whose claim did not hold.
// It is a different exit status from errFailed on purpose: a contract
// regression blocks a merge, a discovered bug files an issue, and a pipeline
// that cannot tell them apart treats both as the first — or, more likely,
// learns to ignore both.
var errFindings = fmt.Errorf("the probing phases found something")

// resolveConfig loads the configuration file, whether it was named or found.
func resolveConfig(path string, positional []string) (config.Config, error) {
	settings := config.Default()

	if path == "" {
		path = config.Discover()
	}
	// A `dredd.yml` and nothing else used to be read, and is now refused rather
	// than passed over. Passing over it would mean running with no configuration
	// at all while a file full of it sat in the directory — so the endpoint, the
	// headers and the skips someone wrote would all be silently absent, and the
	// most likely outcome is a run against whatever host the description happens
	// to name. The rename is a one-line fix and this says so; a wrong run
	// diagnosed from a wall of connection errors is not.
	if path == "" {
		if stranded := config.DreddFile(); stranded != "" {
			return settings, fmt.Errorf(
				"found %s and no vertrag configuration. vertrag reads %s: rename it "+
					"(`mv %s vertrag.yml`) and every key you have keeps working, or point at "+
					"it explicitly with --config %s",
				stranded, strings.Join(config.Filenames, " or "), stranded, stranded)
		}
	}
	if path != "" {
		loaded, err := config.Load(path)
		if err != nil {
			return settings, err
		}
		settings = loaded
	}

	// Positional arguments are Dredd's own calling convention:
	//   vertrag run <description> <endpoint>
	//
	// They win over the file. Dredd resolves this the other way — a dredd.yml
	// silently outranks what you typed — and an afternoon has been lost to
	// running `dredd ./api.json http://localhost:4001` in a directory whose
	// dredd.yml named a different endpoint, and reading a hundred connection
	// errors against the port that was not asked for. So where the two disagree,
	// say which one is being used rather than leaving it to be deduced.
	if len(positional) > 0 {
		noteOverride(&settings, "the description", positional[0], settings.Spec)
		settings.Spec = positional[0]
	}
	if len(positional) > 1 {
		noteOverride(&settings, "the endpoint", positional[1], settings.Endpoint)
		settings.Endpoint = positional[1]
	}
	return settings, nil
}

// noteOverride records that an argument displaced a different value from the
// configuration file. Agreement is silent: only a disagreement is surprising.
func noteOverride(settings *config.Config, what, argument, configured string) {
	if settings.Source == "" || configured == "" || configured == argument {
		return
	}
	settings.Notes = append(settings.Notes, fmt.Sprintf(
		"using %s %s from the command line; %s says %s",
		what, argument, settings.Source, configured))
}

// stripAPIName removes the API title from the front of each transaction name.
//
// The compiler builds the full name — "Title > /path > Operation > 200" — and
// that is what `vertrag compile` reports, matching Dredd's compiler exactly.
// But Dredd's RUNNER drops the title before anything sees it, whenever a single
// description document is under test, and that shortened name is what hook
// files address transactions by and what its reports print.
//
// So the strip belongs here rather than in the compiler: the compiler's output
// is a faithful copy of Dredd's, and the runner reproduces the runner's.
// Without it, every `hooks.before('/path > ...')` in an existing project
// silently fails to match — which is exactly what it looked like when this was
// missing.
func stripAPIName(transactions []compile.Transaction) []compile.Transaction {
	stripped := make([]compile.Transaction, 0, len(transactions))
	for _, transaction := range transactions {
		if prefix := transaction.Origin.APIName + " > "; transaction.Origin.APIName != "" {
			// Only the first occurrence, as the reference's replace() does: a
			// path that happens to repeat the title must keep its own copy.
			transaction.Name = strings.Replace(transaction.Name, prefix, "", 1)
		}
		stripped = append(stripped, transaction)
	}
	return stripped
}

// methodOrder is the order `sorted` puts transactions in.
//
// It is Dredd's, verbatim from its own options.json, and the reasoning is in
// the sentence that accompanies it there: requests are sorted "so that objects
// are not modified before they are created". A description lists operations in
// whatever order reads well, which is frequently GET before the POST that makes
// the thing to get; running that order against a real server tests a resource
// that does not exist yet.
var methodOrder = []string{"CONNECT", "OPTIONS", "POST", "GET", "HEAD", "PUT",
	"PATCH", "LINK", "UNLINK", "DELETE", "TRACE"}

// sortTransactions groups a run by HTTP method when `sorted` is set.
//
// The sort is stable, so transactions sharing a method stay in document order —
// two POSTs that must happen in a particular sequence still do. This is a
// coarse instrument and Dredd is candid that it is: it cannot know that one
// POST feeds another resource entirely. It is the ordering people have, though,
// and vertrag accepted the option, stored it, and then ignored it, which is
// worse than refusing it — a run configured to sort silently did not.
func sortTransactions(transactions []compile.Transaction, sorted bool) []compile.Transaction {
	if !sorted {
		return transactions
	}

	rank := make(map[string]int, len(methodOrder))
	for i, method := range methodOrder {
		rank[method] = i
	}
	// A method the list does not name sorts after every one it does, rather
	// than silently sharing a bucket with CONNECT.
	rankOf := func(method string) int {
		if r, known := rank[strings.ToUpper(method)]; known {
			return r
		}
		return len(methodOrder)
	}

	out := make([]compile.Transaction, len(transactions))
	copy(out, transactions)
	sort.SliceStable(out, func(i, j int) bool {
		return rankOf(out[i].Request.Method) < rankOf(out[j].Request.Method)
	})
	return out
}

// filterTransactions applies the options that narrow a run, and reports every
// filter value that matched nothing.
//
// There are two kinds of option here. An include — `only`, `method`, `tag`,
// `operation-id`, `only-matching` — says what to keep; an exclude says what to
// drop. A transaction survives when it satisfies every include that was given
// and no exclude at all.
//
// Excludes win over includes, and it has to be that way round. The two are
// meant to be written together — `--tag network --exclude-method DELETE` is the
// ordinary case — and the exclude is always the more specific half: nobody
// writes one meaning "unless something else selected it". Resolving the clash
// the other way would make the narrower instruction the one with no effect,
// and the whole point of an exclude is usually that a particular request must
// not be sent.
func filterTransactions(
	transactions []compile.Transaction,
	settings config.Config,
) ([]compile.Transaction, []string, error) {
	filters, err := narrowingFilters(settings)
	if err != nil {
		return nil, nil, err
	}

	filtered := make([]compile.Transaction, 0, len(transactions))
	for _, transaction := range transactions {
		keep := true
		for i := range filters {
			// Every filter is tested against every transaction even once the
			// verdict is settled, rather than breaking out early, because the
			// test is also what records that a value matched something. Short
			// circuit here and `--only a --exclude-tag admin` reports the tag
			// as matching nothing whenever `a` happens not to carry it — a
			// false alarm about the filter that was working perfectly well.
			if filters[i].test(transaction) == filters[i].exclude {
				// An exclude that matched, or an include that did not.
				keep = false
			}
		}
		if keep {
			filtered = append(filtered, transaction)
		}
	}
	return filtered, unmatchedFilters(filters), nil
}

// narrowingFilter is one option that narrows a run: the key it is written
// under, the values it was given, and what counts as a match.
type narrowingFilter struct {
	// key names the option in a diagnostic. It is the configuration key, which
	// is the flag's name without the dashes, so one word finds both.
	key string

	// subject completes "has no transaction …" for a value that matched
	// nothing. The filters match different things, and a message saying
	// "named" about a tag would send somebody looking in the wrong place.
	subject string

	values  []string
	matches func(value string, transaction compile.Transaction) bool

	// exclude inverts the verdict: a transaction this filter matches is
	// dropped rather than being the only kind kept.
	exclude bool

	// matched records, per value, whether any transaction matched it.
	matched []bool
}

// test reports whether a transaction matches any of the filter's values, and
// records which of them it matched.
//
// Any rather than all: `--tag a --tag b` widens a run the way `--method` does,
// and an operation tagged with both should not be required. Excludes read the
// same way — `--exclude-tag a --exclude-tag b` drops anything carrying either,
// because each entry is a thing the reader wants gone.
func (f *narrowingFilter) test(transaction compile.Transaction) bool {
	hit := false
	for i, value := range f.values {
		if f.matches(value, transaction) {
			f.matched[i] = true
			hit = true
		}
	}
	return hit
}

// narrowingFilters builds the filters the settings ask for, in a fixed order so
// the diagnostics of two identical runs are identical.
//
// Only filters that were given anything are returned, so the caller can treat
// every filter it holds as one with an opinion.
func narrowingFilters(settings config.Config) ([]narrowingFilter, error) {
	byName := func(value string, transaction compile.Transaction) bool {
		return value == transaction.Name
	}
	byMethod := func(value string, transaction compile.Transaction) bool {
		return strings.EqualFold(value, transaction.Request.Method)
	}
	byTag := func(value string, transaction compile.Transaction) bool {
		return slices.Contains(transaction.Tags, value)
	}
	byOperationID := func(value string, transaction compile.Transaction) bool {
		// An operation with no operationId matches no named one, rather than
		// matching an empty value nobody can have typed.
		return value != "" && value == transaction.OperationID
	}

	const (
		named     = "named %q"
		method    = "using the method %q"
		tagged    = "carrying the tag %q"
		operation = "whose operationId is %q"
		matching  = "whose name matches %q"
	)

	filters := []narrowingFilter{
		{key: "only", subject: named, values: settings.Only, matches: byName},
		{key: "method", subject: method, values: settings.Method, matches: byMethod},
		{key: "tag", subject: tagged, values: settings.Tag, matches: byTag},
		{key: "operation-id", subject: operation, values: settings.OperationID, matches: byOperationID},
		{key: "exclude", subject: named, values: settings.Exclude, matches: byName, exclude: true},
		{key: "exclude-method", subject: method, values: settings.ExcludeMethod, matches: byMethod, exclude: true},
		{key: "exclude-tag", subject: tagged, values: settings.ExcludeTag, matches: byTag, exclude: true},
		{key: "exclude-operation-id", subject: operation, values: settings.ExcludeOperationID,
			matches: byOperationID, exclude: true},
	}

	// The regular expression filters. A pattern is compiled once here rather
	// than per transaction, and the compiled form is looked up by the pattern
	// text so that the filter still matches by value like every other one and
	// the unmatched report needs no special case.
	for _, regex := range []struct {
		key     string
		values  []string
		exclude bool
	}{
		{"only-matching", settings.OnlyMatching, false},
		{"exclude-matching", settings.ExcludeMatching, true},
	} {
		compiled := make(map[string]*regexp.Regexp, len(regex.values))
		for _, pattern := range regex.values {
			expression, err := regexp.Compile(pattern)
			if err != nil {
				// A pattern that does not compile stops the run and names
				// itself. The tempting alternative is to treat it as an
				// expression that matches nothing, and that is the failure
				// class this project keeps having to fix: an include matching
				// nothing runs no transactions and reads as an API with
				// nothing to test, while an exclude matching nothing sends
				// every request the pattern was written to prevent. Neither
				// says a word about the typo behind it, and the second is the
				// one that reaches a real server.
				return nil, fmt.Errorf(
					"%s: %q is not a valid regular expression: %w", regex.key, pattern, err)
			}
			compiled[pattern] = expression
		}
		filters = append(filters, narrowingFilter{
			key: regex.key, subject: matching, values: regex.values, exclude: regex.exclude,
			matches: func(value string, transaction compile.Transaction) bool {
				// A search, not a whole-name comparison: `--only-matching
				// orders` finds "/orders > List > 200 > application/json"
				// without anybody writing `.*orders.*`. A pattern that means
				// the entire name says so with ^ and $, which is the shorter
				// of the two things to type.
				return compiled[value].MatchString(transaction.Name)
			},
		})
	}

	active := make([]narrowingFilter, 0, len(filters))
	for _, filter := range filters {
		if len(filter.values) == 0 {
			continue
		}
		filter.matched = make([]bool, len(filter.values))
		active = append(active, filter)
	}
	return active, nil
}

// unmatchedFilters describes every filter value that matched no transaction.
//
// This is the same reasoning as the unmatched `skip` and `auth.except` entries,
// and it is worth as much noise as they get: a value that matches nothing is
// nearly always a typo or a transaction that was renamed, and its effect is to
// run something other than what the configuration reads as. The consequence is
// stated per kind because the two are opposites — an include that matched
// nothing quietly tests less than was asked for, while an exclude that matched
// nothing quietly sends what somebody wrote it down to prevent.
func unmatchedFilters(filters []narrowingFilter) []string {
	var reports []string
	for _, filter := range filters {
		consequence := "it keeps nothing, so the run is narrower than it was meant to be"
		if filter.exclude {
			consequence = "it leaves nothing out, so the run is wider than it was meant to be"
		}
		for i, value := range filter.values {
			if filter.matched[i] {
				continue
			}
			reports = append(reports, fmt.Sprintf("%s has no transaction %s; %s",
				filter.key, fmt.Sprintf(filter.subject, value), consequence))
		}
	}
	return reports
}

func toAnnotations(annotations []compile.Annotation) []reporter.Annotation {
	out := make([]reporter.Annotation, 0, len(annotations))
	for _, annotation := range annotations {
		converted := reporter.Annotation{Type: annotation.Type, Message: annotation.Message}
		if len(annotation.Location) > 0 && len(annotation.Location[0]) > 1 {
			converted.Line, converted.Column = annotation.Location[0][0], annotation.Location[0][1]
		}
		out = append(out, converted)
	}
	return out
}

func hasErrors(annotations []compile.Annotation) bool {
	for _, annotation := range annotations {
		if annotation.Type == "error" {
			return true
		}
	}
	return false
}

// stringList collects a repeatable flag.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ", ") }

func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

// reportMissingCredentials names the schemes a run has nothing to satisfy.
//
// vertrag cannot invent a credential, so a secured API answers 401 to
// everything and the report is a wall of failures that say nothing about the
// contract. Saying which scheme is wanted, and how to supply it, is the
// difference between that and a run someone can fix in one command — and for a
// key travelling in the query, it is how they learn no flag will do it at all.
//
// Said once per scheme rather than per transaction: an API where every
// operation is secured would otherwise bury its own results.
func reportMissingCredentials(transactions []compile.Transaction, headers []string) {
	for _, security := range missingCredentials(transactions, headers) {
		fmt.Fprintf(os.Stderr,
			"vertrag: this description requires the %s credential (%s) and the run has none; %s\n",
			security.Name, describeScheme(security), security.Supplier())
	}
}

// missingCredentials selects the schemes a run has nothing to satisfy.
func missingCredentials(transactions []compile.Transaction, headers []string) []compile.Security {
	supplied := map[string]bool{}
	for _, line := range headers {
		if name, _, ok := strings.Cut(line, ":"); ok {
			supplied[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}

	seen := map[string]bool{}
	var missing []compile.Security

	for _, transaction := range transactions {
		for _, security := range transaction.Security {
			if seen[security.Name] {
				continue
			}

			// A header scheme whose header the run already carries is
			// satisfied as far as anything here can tell.
			switch {
			case security.Type == "apiKey" && security.In == "header" &&
				supplied[strings.ToLower(security.Parameter)]:
				seen[security.Name] = true
				continue
			// A cookie scheme is satisfied by a Cookie header, however many
			// cookies it carries: the run merges them, so the credential is
			// still sent alongside the description's own. Which cookie inside
			// the line is the credential is not checked, for the same reason
			// the header case does not check its value — the point is to stop
			// nagging a run that has clearly been configured.
			case security.Type == "apiKey" && security.In == "cookie" && supplied["cookie"]:
				seen[security.Name] = true
				continue
			case security.Type == "http" && supplied["authorization"]:
				seen[security.Name] = true
				continue
			}

			seen[security.Name] = true
			missing = append(missing, security)
		}
	}

	return missing
}

func describeScheme(security compile.Security) string {
	switch {
	case security.Type == "apiKey":
		return security.Type + " " + security.Parameter + " in the " + security.In
	case security.Scheme != "":
		return security.Type + " " + security.Scheme
	default:
		return security.Type
	}
}
