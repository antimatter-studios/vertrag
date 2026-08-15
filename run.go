package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/antimatter-studios/vertrag/internal/apidesc"
	"github.com/antimatter-studios/vertrag/internal/compile"
	"github.com/antimatter-studios/vertrag/internal/config"
	"github.com/antimatter-studios/vertrag/internal/hooks"
	"github.com/antimatter-studios/vertrag/internal/reporter"
	"github.com/antimatter-studios/vertrag/internal/runner"
)

// runRun is `vertrag run`: read a description, derive the transactions, send
// them at a server, and report what came back.
func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to a dredd.yml (default: ./dredd.yml when present)")
	endpoint := fs.String("endpoint", "", "base URL of the server under test")
	dryRun := fs.Bool("dry-run", false, "compile and report the transactions without sending them")
	details := fs.Bool("details", false, "print the request and response of passing transactions too")
	noColor := fs.Bool("no-color", false, "disable coloured output")
	sorted := fs.Bool("sorted", false, "run transactions grouped by method rather than in document order")
	var headers stringList
	fs.Var(&headers, "header", "extra header to send with every request, as 'Name: value' (repeatable)")
	var only stringList
	fs.Var(&only, "only", "run only the named transaction (repeatable)")
	var methods stringList
	fs.Var(&methods, "method", "run only transactions using this method (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	settings, err := resolveConfig(*configPath, fs.Args())
	if err != nil {
		return err
	}

	// Command-line options win over the file: the file records what a project
	// normally does, the flags what this run should do instead.
	if *endpoint != "" {
		settings.Endpoint = *endpoint
	}
	if *dryRun {
		settings.DryRun = true
	}
	if *details {
		settings.Details = true
	}
	if *noColor {
		settings.Color = false
	}
	if *sorted {
		settings.Sorted = true
	}
	settings.Header = append(settings.Header, headers...)
	settings.Only = append(settings.Only, only...)
	settings.Method = append(settings.Method, methods...)

	if err := settings.Validate(); err != nil {
		return err
	}

	report := reporter.CLI{Out: os.Stdout, Color: settings.Color, Details: settings.Details}

	for _, key := range settings.Unsupported {
		fmt.Fprintf(os.Stderr, "vertrag: `%s` is set but not supported yet; it is being ignored\n", key)
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

	report.Annotations(toAnnotations(result.Annotations))
	if hasErrors(result.Annotations) {
		return fmt.Errorf("the API description could not be read; nothing was run")
	}

	transactions := filterTransactions(result.Transactions, settings)
	if len(transactions) == 0 {
		fmt.Fprintln(os.Stdout, "No transactions to run.")
		return nil
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

	engine := runner.New(settings.Endpoint)
	engine.ExtraHeaders = settings.Header

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

// errFailed reports a run whose tests failed, as opposed to one that could not
// be carried out. The caller turns it into a non-zero exit status without
// printing it as an error message.
var errFailed = fmt.Errorf("some transactions failed")

// resolveConfig loads a dredd.yml if one is named, given, or simply present.
func resolveConfig(path string, positional []string) (config.Config, error) {
	settings := config.Default()

	if path == "" {
		if _, err := os.Stat("dredd.yml"); err == nil {
			path = "dredd.yml"
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
	if len(positional) > 0 {
		settings.Blueprint = positional[0]
	}
	if len(positional) > 1 {
		settings.Endpoint = positional[1]
	}
	return settings, nil
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
