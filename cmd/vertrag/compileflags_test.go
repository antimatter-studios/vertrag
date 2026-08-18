package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCompileTakesItsFlagsInAnyPosition pins a bug found by driving the built
// binary rather than by reading the code.
//
// `run`, `fuzz` and `coverage` all parse through parseInterspersed, because Go's
// flag package stops at the first argument that is not a flag. `compile` did
// not, so `vertrag compile api.yml --graphql-mutations` — a command line that
// reads perfectly — became two positional arguments and was refused with
// "compile takes exactly one file", a message about the wrong thing entirely.
//
// It went unnoticed while compile's flags were all ones nobody writes last.
func TestCompileTakesItsFlagsInAnyPosition(t *testing.T) {
	binary := build(t)

	dir := t.TempDir()
	schema := filepath.Join(dir, "schema.graphql")
	if err := os.WriteFile(schema, []byte(
		"type Query { viewer: User }\ntype Mutation { wipe: Boolean }\ntype User { id: ID! }\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"compile", schema, "--graphql-mutations"},
		{"compile", "--graphql-mutations", schema},
	} {
		output, code := runCommand(t, binary, args...)
		if code != 0 {
			t.Errorf("%v exited %d: %s", args[1:], code, output)
			continue
		}
		// The flag was honoured whichever side of the filename it sat.
		if !strings.Contains(output, "Mutation > wipe") {
			t.Errorf("%v did not honour --graphql-mutations:\n%s", args[1:], output)
		}
	}
}
