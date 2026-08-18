package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/fuzz"
	"github.com/antimatter-studios/vertrag/hooks"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// probeFlags are the flags `fuzz` and `coverage` share: everything about
// which operations to probe, how to reach the server, and how to report —
// as distinct from how values are chosen, which is each command's own.
type probeFlags struct {
	configPath   string
	endpoint     string
	reporterName string
	output       string
	noColor      bool
	noSanitize   bool
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

	sanitizeHdrs stringList
	transport    transportFlags
}

func addProbeFlags(fs *flag.FlagSet, f *probeFlags, verb string) {
	fs.StringVar(&f.configPath, "config", "", "path to a vertrag.yml (default: the first of vertrag.yml, vertrag.yaml found here)")
	fs.StringVar(&f.endpoint, "endpoint", "", "base URL of the server under test")
	fs.StringVar(&f.reporterName, "reporter", "", "also emit the results through a reporter: cli, dot, markdown, html, or junit")
	fs.StringVar(&f.output, "output", "", "write the --reporter output to a file instead of stdout")
	fs.BoolVar(&f.noColor, "no-color", false, "disable coloured output")
	fs.BoolVar(&f.noSanitize, "no-sanitize", false, "show credential header values in findings instead of <redacted>")
	fs.Var(&f.headers, "header", "extra header to send with every request, as 'Name: value' (repeatable)")
	fs.Var(&f.only, "only", verb+" only the named transaction (repeatable)")
	fs.Var(&f.methods, "method", verb+" only transactions using this method (repeatable)")
	fs.Var(&f.tags, "tag", verb+" only transactions whose operation carries this tag (repeatable)")
	fs.Var(&f.operationIDs, "operation-id", verb+" only transactions of this operationId (repeatable)")
	// The narrowing options are offered here as well as on `run`, and not
	// because symmetry is tidy: the configuration keys behind them are read by
	// every command, so an `exclude-tag` that holds a destructive operation out
	// of a run must hold it out of a probe too — which sends far more requests
	// at it. Having the key work and the flag not exist is the arrangement most
	// likely to end with somebody reaching for the flag under time pressure and
	// getting an "unknown flag" instead of a narrower probe.
	fs.Var(&f.onlyMatching, "only-matching", verb+" only transactions whose name matches this regular expression (repeatable)")
	fs.Var(&f.exclude, "exclude", "leave out the named transaction, whatever else selected it (repeatable)")
	fs.Var(&f.excludeMatching, "exclude-matching", "leave out transactions whose name matches this regular expression (repeatable)")
	fs.Var(&f.excludeMethods, "exclude-method", "leave out transactions using this method (repeatable)")
	fs.Var(&f.excludeTags, "exclude-tag", "leave out transactions whose operation carries this tag (repeatable)")
	fs.Var(&f.excludeOperationIDs, "exclude-operation-id", "leave out transactions of this operationId (repeatable)")
	fs.Var(&f.sanitizeHdrs, "sanitize-header", "also redact this header's value in findings (repeatable)")
	addTransportFlags(fs, &f.transport)
}

// probeSet is everything a probing command needs, prepared: the settings, the
// operations that carry something to probe, and an engine to send with.
type probeSet struct {
	settings config.Config
	// probeable are the transactions with at least one schema to probe;
	// skipped is how many were passed over for having none.
	probeable []compile.Transaction
	skipped   int
	engine    *runner.Runner
	ctx       context.Context
	stop      context.CancelFunc
}

// prepareProbes does the whole preamble both probing commands share: read the
// config, parse and compile the description, filter, build the engine, apply
// auth and skips. It prints what a run prints — the signature, the notes,
// the annotations — and returns nil, nil when there is nothing to probe,
// having said so.
func prepareProbes(f *probeFlags, fs *flag.FlagSet, positional []string) (*probeSet, error) {
	f.transport.noteGiven(fs)

	settings, err := resolveConfig(f.configPath, positional)
	if err != nil {
		return nil, err
	}
	if f.endpoint != "" {
		settings.Endpoint = f.endpoint
	}
	if f.noColor {
		settings.Color = false
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
	f.transport.apply(&settings.Transport)
	reporter.SetSanitize(!f.noSanitize)
	for _, name := range f.sanitizeHdrs {
		reporter.AddRedactedHeader(name)
	}
	if err := settings.Validate(); err != nil {
		return nil, err
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
		return nil, fmt.Errorf("reading the API description: %w", err)
	}
	parsed, err := apidesc.Parse(source, settings.Spec)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", settings.Spec, err)
	}
	result := compile.Compile(parsed.MediaType, parsed.Elements, settings.Spec)

	annotations := reporter.CLI{Out: os.Stdout, Color: settings.Color}
	annotations.Annotations(toAnnotations(result.Annotations))
	if hasErrors(result.Annotations) {
		return nil, fmt.Errorf("the API description could not be read; nothing was run")
	}

	transactions, unmatched, err := filterTransactions(stripAPIName(result.Transactions), settings)
	if err != nil {
		return nil, err
	}
	for _, report := range unmatched {
		fmt.Fprintf(os.Stderr, "vertrag: %s\n", report)
	}

	// An operation whose body and parameters all lack schemas has nothing to
	// probe. Saying how many were passed over, and why, is the difference
	// between a quiet run that tested little and one a reader can trust.
	probeable, skipped := partitionBySchema(transactions)
	if len(probeable) == 0 {
		fmt.Printf("No operation carries a schema for its body or for any parameter, so there is nothing to probe.\n")
		if skipped > 0 {
			fmt.Printf("%d transaction(s) were passed over for that reason.\n", skipped)
		}
		return nil, nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	engine, err := newEngine(settings)
	if err != nil {
		stop()
		return nil, err
	}
	engine.ExtraHeaders = settings.Header

	// Probing needs the credential as much as a run does: without it an
	// authenticated API answers 401 to every case, no transaction passes the
	// baseline check, and the report says nothing was worth probing rather than
	// that the door was locked.
	//
	// The rules are matched against EVERY transaction, not the probeable
	// subset: an `except` or `skip` entry names a transaction by its full
	// name — often an error variant like "> 401 >" — and would be reported
	// as matching nothing if only the success variants were on the list.
	if err := applyConfiguredRules(ctx, engine, settings, transactions); err != nil {
		stop()
		return nil, err
	}

	probeable, configSkipped := withoutSkipped(probeable, engine.Skip)
	if len(configSkipped) > 0 {
		fmt.Printf("%d transaction(s) left out by the skip list in %s.\n", len(configSkipped), settings.Source)
	}
	if len(probeable) == 0 {
		fmt.Printf("Every operation that could be probed is on the skip list, so nothing was sent.\n")
		stop()
		return nil, nil
	}

	// The probing commands load hook files too. They did not, which meant a
	// hook written to hold a dangerous field at a safe value was honoured on
	// the documented requests and ignored on every generated one.
	stopHooks, err := startHooks(ctx, engine, settings)
	if err != nil {
		stop()
		return nil, err
	}

	return &probeSet{
		settings: settings, probeable: probeable, skipped: skipped, engine: engine, ctx: ctx,
		stop: func() { stopHooks(); stop() },
	}, nil
}

// startHooks loads the hook files, if there are any, and attaches the worker
// to the engine.
//
// Starting is a hard failure rather than a warning: a suite whose hooks did
// not load would authenticate nothing, pin nothing and skip nothing, and
// report a wall of failures that say nothing about the API. That reasoning is
// stronger still for the probing commands, where a hook may be the thing
// holding a dangerous field at a safe value.
func startHooks(ctx context.Context, engine *runner.Runner, settings config.Config) (func(), error) {
	if len(settings.Hookfiles) == 0 {
		return func() {}, nil
	}

	client, err := hooks.Start(ctx, hooks.Options{
		Language:    settings.Language,
		Hookfiles:   settings.Hookfiles,
		Host:        settings.HooksWorkerHandlerHost,
		Port:        settings.HooksWorkerHandlerPort,
		Timeout:     settings.HooksWorkerTimeout,
		ConnectWait: settings.HooksWorkerConnectWait,
		Stderr:      os.Stderr,
	})
	if err != nil {
		return func() {}, fmt.Errorf("loading hooks: %w", err)
	}
	engine.Hooks = client
	return client.Stop, nil
}

// skipAware translates the runner's "a hook took this out of the run" into
// the one the generation packages understand.
//
// The two sentinels are separate on purpose: the runner reports what it did,
// and fuzz states what a Sender may tell it, without either package having to
// know the other exists.
func skipAware(reply validate.Message, err error) (validate.Message, error) {
	if errors.Is(err, runner.ErrSkippedByHook) {
		return reply, fuzz.ErrSkipped
	}
	return reply, err
}

// emitThrough sends results through the --reporter, if one was asked for.
// The narrative on stdout is the command's own report; a reporter is for
// machines and pipelines and comes in addition, never instead — and the
// config file's reporter list is deliberately not consulted, because it
// configures `run`, and a junit file of contract results silently replaced
// by probe results would be a surprise in the middle of someone's pipeline.
func emitThrough(f *probeFlags, settings config.Config, results []runner.Result) error {
	if f.reporterName == "" {
		return nil
	}
	emitSettings := settings
	emitSettings.Reporters = []string{f.reporterName}
	emitSettings.Outputs = []string{f.output}
	emit, closeFiles, err := newReporter(emitSettings)
	if err != nil {
		return err
	}
	emit.Report(results)
	closeFiles()
	return nil
}
