// Command vertrag contract-tests an HTTP API against its description document.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/antimatter-studios/vertrag/internal/apidesc"
	"github.com/antimatter-studios/vertrag/internal/compile"
	"github.com/antimatter-studios/vertrag/internal/refract"
)

// Stamped at build time by the release pipeline; see .goreleaser.yml.
var (
	version   = "dev"
	buildDate = "unknown"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "vertrag:", err)
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
	default:
		return fmt.Errorf("unknown command %q; run `vertrag help`", args[0])
	}
}

func usage() {
	fmt.Printf(`vertrag %s (built %s)

Contract-test an HTTP API against its description document.

Usage:
  vertrag compile [flags] <file>   Compile a description into HTTP transactions
  vertrag version                  Print the version

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
