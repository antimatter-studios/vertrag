package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
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

// runFuzz is `vertrag fuzz`: test each operation with bodies drawn from its
// schema instead of the single example the description shows.
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
	cases := fs.Int("cases", 20, "bodies to try per operation")
	seed := fs.Uint64("seed", 0, "replay a previous run (0 picks one and reports it)")
	mode := fs.String("mode", "both", "which bodies to send: valid, invalid, or both")
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

	// An operation with no request schema has nothing to draw from. Saying how
	// many were passed over, and why, is the difference between a quiet run
	// that tested little and one a reader can trust.
	probeable, skipped := partitionBySchema(transactions)
	if len(probeable) == 0 {
		fmt.Printf("No operation carries a request body schema, so there is nothing to generate from.\n")
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

// probeAll runs every operation through every requested mode and reports.
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

	for _, transaction := range transactions {
		schema, err := decodeSchema(transaction.Request.Schema)
		if err != nil {
			fmt.Printf("%s: schema could not be read, skipping (%v)\n", transaction.Name, err)
			continue
		}

		for _, mode := range modes {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			send := func(ctx context.Context, body string) (validate.Message, error) {
				requests++
				attempt := transaction
				attempt.Request.Body = body
				return engine.Send(ctx, attempt)
			}

			finding, found := fuzz.Probe(ctx, schema, mode, send, options)
			if !found {
				continue
			}
			findings++
			printFinding(transaction, finding, color)
		}
	}

	fmt.Printf("\n%d operation(s) probed, %d request(s) sent, %d finding(s)",
		len(transactions), requests, findings)
	if skipped > 0 {
		fmt.Printf(", %d transaction(s) skipped for having no request schema", skipped)
	}
	fmt.Println()

	if findings > 0 {
		return errFailed
	}
	return nil
}

func printFinding(transaction compile.Transaction, finding fuzz.Finding, color bool) {
	const (
		red   = "\033[31m"
		dim   = "\033[2m"
		reset = "\033[0m"
	)
	paint := func(code, text string) string {
		if !color {
			return text
		}
		return code + text + reset
	}

	fmt.Printf("\n%s %s\n", paint(red, "finding:"), transaction.Name)
	fmt.Printf("  %s\n", finding.Message)
	fmt.Printf("  %s %s %s\n", paint(dim, "request:"),
		transaction.Request.Method, transaction.Request.URI)
	fmt.Printf("  %s %s\n", paint(dim, "body:   "), finding.Body)
	fmt.Printf("  %s %s\n", paint(dim, "status: "), finding.Status)
}

// partitionBySchema separates the operations generation can work on from those
// it cannot, returning how many were left out.
func partitionBySchema(transactions []compile.Transaction) ([]compile.Transaction, int) {
	var probeable []compile.Transaction
	skipped := 0
	for _, transaction := range transactions {
		if strings.TrimSpace(transaction.Request.Schema) == "" {
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
