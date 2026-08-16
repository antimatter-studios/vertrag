package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/hooks"
	"github.com/antimatter-studios/vertrag/link"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
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
	reporterName      string
	output            string

	headers stringList
	only    stringList
	methods stringList

	// positional are the arguments left after the flags: a description and an
	// endpoint, either of which a config file may supply instead.
	positional []string
}

func parseRunFlags(args []string) (runFlags, error) {
	var f runFlags

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.StringVar(&f.configPath, "config", "", "path to a vertrag.yml (default: the first of vertrag.yml, vertrag.yaml, dredd.yml found here)")
	fs.StringVar(&f.endpoint, "endpoint", "", "base URL of the server under test")
	fs.BoolVar(&f.dryRun, "dry-run", false, "compile and report the transactions without sending them")
	fs.BoolVar(&f.details, "details", false, "print the request and response of passing transactions too")
	fs.BoolVar(&f.noColor, "no-color", false, "disable coloured output")
	fs.BoolVar(&f.sorted, "sorted", false, "run transactions grouped by method rather than in document order")
	fs.BoolVar(&f.sequence, "sequence", false, "order the run by the links the description declares, filling each step's parameters from the response of the step it follows")
	fs.BoolVar(&f.checkHeaderSchema, "check-header-schema", false, "validate response header values against the schemas the description gives them")
	fs.StringVar(&f.reporterName, "reporter", "", "output format: cli, dot, markdown, html or junit (overrides the config)")
	fs.StringVar(&f.output, "output", "", "write the report to a file instead of stdout")
	fs.Var(&f.headers, "header", "extra header to send with every request, as 'Name: value' (repeatable)")
	fs.Var(&f.only, "only", "run only the named transaction (repeatable)")
	fs.Var(&f.methods, "method", "run only transactions using this method (repeatable)")

	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return f, err
	}
	f.positional = positional
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
	settings.Header = append(settings.Header, f.headers...)
	settings.Only = append(settings.Only, f.only...)
	settings.Method = append(settings.Method, f.methods...)

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

	// Reading a Dredd file works, and saying so is how a reader learns the
	// rename is available. It is said once, not per run of the day.
	if config.IsDreddFile(settings.Source) {
		fmt.Fprintf(os.Stderr,
			"vertrag: read %s. Renaming it to vertrag.yml enables vertrag's own settings; every key you have keeps working.\n",
			settings.Source)
	}

	report, closeReport, err := newReporter(settings)
	if err != nil {
		return err
	}
	defer closeReport()

	// Diagnostics about the description go to the terminal whatever the report
	// format is: they are about the document, not the run.
	annotations := reporter.CLI{Out: os.Stdout, Color: settings.Color && flags.output == ""}

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

	annotations.Annotations(toAnnotations(result.Annotations))
	if hasErrors(result.Annotations) {
		return fmt.Errorf("the API description could not be read; nothing was run")
	}

	transactions := sortTransactions(filterTransactions(stripAPIName(result.Transactions), settings), settings.Sorted)
	if len(transactions) == 0 {
		fmt.Fprintln(os.Stdout, "No transactions to run.")
		return nil
	}

	reportMissingCredentials(transactions, settings.Header)

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

	engine := runner.New(settings.Endpoint)
	engine.ExtraHeaders = settings.Header

	if err := applyConfiguredRules(ctx, engine, settings, transactions); err != nil {
		return err
	}

	// Sequencing is opt-in because it reorders a run, and a suite whose hooks
	// were written against document order would notice. It is not exploratory —
	// the plan is fixed by the description, so the same document always
	// produces the same order — which is why it belongs on `run` rather than
	// in a mode of its own.
	if flags.sequence {
		sequencer := link.NewSequencer(transactions)
		for _, note := range sequencer.Notes() {
			fmt.Fprintf(os.Stderr, "vertrag: %s\n", note)
		}
		engine.Plan = sequencer
	}

	engine.Checks = runner.Checks{
		ServerError:  settings.Checks.ServerError,
		ContentType:  settings.Checks.ContentType,
		HeaderSchema: settings.Checks.HeaderSchema,
	}

	// Hook files run in a worker process. Starting it is a hard failure: a
	// suite whose hooks did not load would authenticate nothing and skip
	// nothing, and report a wall of failures that say nothing about the API.
	if len(settings.Hookfiles) > 0 {
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
			return fmt.Errorf("loading hooks: %w", err)
		}
		defer client.Stop()
		engine.Hooks = client
	}

	results, err := engine.Run(ctx, transactions)
	if err != nil {
		return err
	}

	if !report.Report(results) {
		return errFailed
	}
	return nil
}

// Reporter renders a run's results and says whether it passed.
type Reporter interface {
	Report(results []runner.Result) bool
}

// newReporter builds the reporters the settings ask for, writing each to its
// paired output file or to stdout when it has none.
//
// A report written to a file is for a machine, so it never carries colour.
func newReporter(settings config.Config) (Reporter, func(), error) {
	var reporters []Reporter
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
			reporters = append(reporters, reporter.JUnit{Out: destination})
		default:
			closeAll()
			return nil, closeAll, fmt.Errorf(
				"unknown reporter %q; vertrag has cli, dot, markdown, html and junit", name)
		}
	}

	if len(reporters) == 0 {
		reporters = append(reporters, reporter.CLI{Out: os.Stdout, Color: settings.Color})
	}
	return multiReporter(reporters), closeAll, nil
}

// multiReporter runs several reporters over the same results, which is how a
// pipeline gets a readable terminal log and a machine-readable file from one
// run.
type multiReporter []Reporter

func (m multiReporter) Report(results []runner.Result) bool {
	passed := true
	for _, r := range m {
		if !r.Report(results) {
			passed = false
		}
	}
	return passed
}

// errFailed reports a run whose tests failed, as opposed to one that could not
// be carried out. The caller turns it into a non-zero exit status without
// printing it as an error message.
var errFailed = fmt.Errorf("some transactions failed")

// resolveConfig loads a dredd.yml if one is named, given, or simply present.
func resolveConfig(path string, positional []string) (config.Config, error) {
	settings := config.Default()

	if path == "" {
		path = config.Discover()
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

// filterTransactions applies the options that narrow a run.
func filterTransactions(transactions []compile.Transaction, settings config.Config) []compile.Transaction {
	names := toSet(settings.Only)
	methods := make(map[string]bool, len(settings.Method))
	for _, method := range settings.Method {
		methods[strings.ToUpper(method)] = true
	}

	filtered := make([]compile.Transaction, 0, len(transactions))
	for _, transaction := range transactions {
		if len(names) > 0 && !names[transaction.Name] {
			continue
		}
		if len(methods) > 0 && !methods[strings.ToUpper(transaction.Request.Method)] {
			continue
		}
		filtered = append(filtered, transaction)
	}
	return filtered
}

func toSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
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
// key travelling in the query or a cookie, it is how they learn no flag will do
// it at all.
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
