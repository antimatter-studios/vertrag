// Command vertrag contract-tests an HTTP API against its description document.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/refract"
)

// Stamped at build time by the release pipeline; see .goreleaser.yml.
var (
	version   = "dev"
	buildDate = "unknown"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		// A run whose tests failed has already reported them in full; printing
		// "vertrag: some transactions failed" underneath would add nothing.
		// The exit status is what a CI pipeline reads.
		switch err {
		case errFailed:
			os.Exit(1)
		case errFindings:
			// Documented transactions passed; the probing phases found
			// something. Distinct from 1 so a pipeline can gate on the
			// contract and merely report the findings.
			os.Exit(2)
		default:
			fmt.Fprintln(os.Stderr, "vertrag:", err)
			os.Exit(1)
		}
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}

	switch args[0] {
	case "--version", "-v", "version":
		fmt.Println(version)
		return nil
	case "--help", "-h", "help":
		usage()
		return nil
	case "compile":
		return runCompile(args[1:])
	case "run":
		return runRun(args[1:])
	case "fuzz":
		return runFuzz(args[1:])
	case "coverage":
		return runCoverage(args[1:])
	default:
		return fmt.Errorf("unknown command %q; run `vertrag help`", args[0])
	}
}

func usage() {
	fmt.Printf(`vertrag %s (built %s)

Contract-test an HTTP API against its description document: OpenAPI 3,
OpenAPI 2, or a GraphQL schema.

Usage:
  vertrag run [flags] [description] [endpoint]
                                   Test a running API against its description
  vertrag fuzz [flags] [description] [endpoint]
                                   Probe operations with bodies drawn from
                                   their schema, rather than the one example
  vertrag coverage [flags] [description] [endpoint]
                                   Send every boundary each schema implies —
                                   the maximum, one past it, the required
                                   property missing — the same ones every run
  vertrag compile [flags] <file>   Show the transactions a description yields
  vertrag version                  Print the version

Run reads ./vertrag.yml when it is present, so a configured project needs no
arguments. A project arriving from Dredd renames its dredd.yml: every key it
holds keeps working.

`, version, buildDate)
}

// runCompile emits the transactions a description document yields.
//
// It also accepts pre-parsed API Elements, via --elements. That mode exists for
// the oracle: it lets the compiler be compared against Dredd's for input
// formats whose parser is not written yet, since Dredd ships API Elements for
// all of them.
func runCompile(args []string) error {
	fs := flag.NewFlagSet("compile", flag.ContinueOnError)
	elements := fs.Bool("elements", false, "treat the input as pre-parsed API Elements")
	mediaType := fs.String("media-type", "", "media type of the input, when it is API Elements")
	filename := fs.String("filename", "", "name recorded as the transactions' origin")
	var graphql graphqlFlags
	addGraphQLFlags(fs, &graphql)
	// Parsed the same way `run`, `fuzz` and `coverage` parse: flags may appear
	// before, after or between the positional arguments. Go's flag package
	// stops at the first non-flag, so a plain fs.Parse turned
	// `compile api.yml --graphql-mutations` into two positional arguments and
	// refused it with "compile takes exactly one file" — a message about the
	// wrong thing entirely, for a command line that reads perfectly.
	//
	// It went unnoticed while compile's flags were all ones nobody writes last.
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("compile takes exactly one file")
	}

	path := positional[0]
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	origin := *filename
	if origin == "" {
		origin = filepath.Base(path)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	// Transaction names are built by joining parts with " > ", so the default
	// HTML escaping would render every one of them as > — unreadable, and
	// not what the reference emits.
	encoder.SetEscapeHTML(false)

	if *elements {
		root, err := refract.Load(data)
		if err != nil {
			return fmt.Errorf("reading API Elements: %w", err)
		}
		return encoder.Encode(compile.Compile(*mediaType, root, origin))
	}

	settings := config.Config{}
	graphql.apply(&settings.GraphQL)
	result, withheld, err := transactionsFor(data, origin, settings)
	if err != nil {
		return err
	}
	// To stderr, so that stdout still carries nothing but the JSON a pipeline
	// reads. It is said here as well as on a run because this command is what
	// people read to find out what a description yields, and an answer that
	// quietly stopped at the query root would be the wrong one.
	for _, note := range withheld {
		fmt.Fprintf(os.Stderr, "vertrag: %s\n", note)
	}
	return encoder.Encode(result)
}
