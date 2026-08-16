package corpus_test

import (
	"bytes"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/corpus"
	"github.com/antimatter-studios/vertrag/runner"
)

// TestCompilingIsDeterministic pins a property everything downstream assumes.
//
// The same description must compile to the same transactions every time, in the
// same order, with the same bodies. Two runs that differ cannot be diffed, a
// failure cannot be reproduced from a report, and a CI job comparing against a
// previous run reports noise as change.
//
// Go's map iteration is randomised deliberately, and a parser is made almost
// entirely of maps — schema properties, headers, parameters, examples. Anything
// that walks one without sorting produces a different answer on a different
// afternoon, and nothing about a single run reveals it.
func TestCompilingIsDeterministic(t *testing.T) {
	for _, name := range corpus.Names() {
		t.Run(name, func(t *testing.T) {
			source, err := corpus.Load(name)
			if err != nil {
				t.Fatalf("loading: %v", err)
			}

			first := compileOnce(t, source, name)
			// Enough repetitions that a randomised map order would almost
			// certainly differ at least once. Cheap: no I/O, no server.
			for i := 0; i < 30; i++ {
				if again := compileOnce(t, source, name); again != first {
					t.Fatalf("compiling twice gave different results on attempt %d:\n%s\n---\n%s",
						i+1, first, again)
				}
			}
		})
	}
}

func compileOnce(t *testing.T, source []byte, name string) string {
	t.Helper()

	parsed, err := apidesc.Parse(source, name)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	result := compile.Compile(parsed.MediaType, parsed.Elements, name)

	var b bytes.Buffer
	for _, transaction := range result.Transactions {
		b.WriteString(transaction.Name)
		b.WriteString(" | ")
		b.WriteString(transaction.Request.Method)
		b.WriteString(" ")
		b.WriteString(transaction.Request.URI)
		b.WriteString(" | ")
		b.WriteString(transaction.Request.Body)
		b.WriteString(" | ")
		b.WriteString(transaction.Response.Status)
		b.WriteString(" | ")
		b.WriteString(transaction.Response.Body)
		b.WriteString(" | ")
		b.WriteString(transaction.Response.Schema)
		for _, header := range transaction.Request.Headers {
			b.WriteString(" | ")
			b.WriteString(header.Name)
			b.WriteString(":")
			b.WriteString(header.Value)
		}
		b.WriteString("\n")
	}
	for _, annotation := range result.Annotations {
		b.WriteString(annotation.Type)
		b.WriteString(": ")
		b.WriteString(annotation.Message)
		b.WriteString("\n")
	}
	return b.String()
}

// TestReportingIsDeterministic is the same property at the other end.
//
// A report that reorders between runs cannot be diffed either, and the
// reporters render from maps too — headers most of all, which arrive from
// net/http as one.
func TestReportingIsDeterministic(t *testing.T) {
	for _, name := range corpus.Names() {
		results := run(t, name, runner.Checks{ServerError: true, ContentType: true},
			corpus.FaultBodyViolatesSchema)
		if len(failures(results)) == 0 {
			continue
		}

		for format := range reporters(&bytes.Buffer{}) {
			t.Run(name+"/"+format, func(t *testing.T) {
				var first bytes.Buffer
				reporters(&first)[format].Report(results)

				for i := 0; i < 20; i++ {
					var again bytes.Buffer
					reporters(&again)[format].Report(results)
					if again.String() != first.String() {
						t.Fatalf("reporting twice gave different output on attempt %d", i+1)
					}
				}
			})
		}
	}
}
