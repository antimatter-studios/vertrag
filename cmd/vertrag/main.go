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
	case "doctor":
		return runDoctor(args[1:])
	case "fuzz", "coverage":
		// Both were commands of their own until they became phases of a run.
		//
		// Two entry points to one job is not a convenience, it is a place for
		// them to drift, and they did: `vertrag fuzz` silently ignored
		// `fuzz.seed`, `fuzz.cases` and `fuzz.whole-request` from the config
		// file while `run --phases fuzz` honoured them — so a pinned seed
		// worked through one door and not the other, and the run that ignored
		// it still printed a seed and still looked reproducible. `server:`
		// went the same way, starting for `run` and not for these.
		//
		// Named rather than dropped into "unknown command", because the old
		// spelling is in people's shell history and their CI files, and the
		// replacement is one they would otherwise have to go and look up.
		return fmt.Errorf(
			"`vertrag %s` is now a phase of a run rather than a command of its own: "+
				"use `vertrag run --phases %s` (or `phases: [examples, %s]` in vertrag.yml). "+
				"Every flag it took, `run` takes",
			args[0], args[0], args[0])
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
  vertrag doctor [flags]           Check the configuration against the description
                                   and send nothing: which settings reach
                                   something, and which quietly reach nothing
  vertrag compile [flags] <file>   Show the transactions a description yields
  vertrag version                  Print the version

Phases, selected with --phases or the "phases:" key. Examples always runs; the
rest are opt-in because they generate values, and generation sends whatever a
schema permits — including, on an API that can act, the value that makes a
request real. See fuzz.pin in vertrag.yml.

  examples   The requests the description documents (the default, always run)
  coverage   Every boundary each schema implies — the maximum, one past it,
             the required property missing — deterministic, the same each run
  fuzz       Values drawn at random from the schema; --seed replays a run
  stateful   Whole lifecycles, ordered by the links the description declares

  vertrag run --phases coverage,fuzz openapi.yml http://localhost:4000

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
