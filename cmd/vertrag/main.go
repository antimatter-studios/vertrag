// Command vertrag contract-tests an HTTP API against its description document.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
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
		if err != errFailed {
			fmt.Fprintln(os.Stderr, "vertrag:", err)
		}
		os.Exit(1)
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

Contract-test an HTTP API against its description document.

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

Run reads ./dredd.yml when it is present, so a project already configured for
Dredd needs no arguments.

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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("compile takes exactly one file")
	}

	path := fs.Arg(0)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	origin := *filename
	if origin == "" {
		origin = filepath.Base(path)
	}

	var root *refract.Element
	if *elements {
		if root, err = refract.Load(data); err != nil {
			return fmt.Errorf("reading API Elements: %w", err)
		}
	} else {
		result, err := apidesc.Parse(data, origin)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		root, *mediaType = result.Elements, result.MediaType
	}
	*filename = origin

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	// Transaction names are built by joining parts with " > ", so the default
	// HTML escaping would render every one of them as > — unreadable, and
	// not what the reference emits.
	encoder.SetEscapeHTML(false)
	return encoder.Encode(compile.Compile(*mediaType, root, *filename))
}
