package corpus_test

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/antimatter-studios/vertrag/corpus"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
)

// reporters are every format vertrag can write, built over one buffer.
func reporters(out *bytes.Buffer) map[string]interface {
	Report([]runner.Result) bool
} {
	return map[string]interface {
		Report([]runner.Result) bool
	}{
		"cli":      &reporter.CLI{Out: out},
		"dot":      &reporter.Dot{Out: out},
		"markdown": &reporter.Markdown{Out: out},
		"html":     &reporter.HTML{Out: out},
		"junit":    &reporter.JUnit{Out: out},
	}
}

// TestEveryReporterSurvivesEveryDescription is a breadth check rather than a
// deep one, and it exists because the reporters are the only part of vertrag a
// user always sees.
//
// The corpus carries content chosen to be awkward — bodies with unicode outside
// the basic plane, keys colliding with JSON Pointer syntax, values needing URL
// escaping, numbers at the edge of what a float64 holds — and a reporter that
// panics or emits something unreadable on any of it fails a run for reasons
// having nothing to do with the API under test.
//
// Every description is run against a faulty server, because a passing run
// exercises almost none of a reporter: the failure path is where bodies,
// headers and diagnostics are actually written out.
func TestEveryReporterSurvivesEveryDescription(t *testing.T) {
	for _, name := range corpus.Names() {
		for _, fault := range corpus.Faults() {
			results := run(t, name, runner.Checks{ServerError: true, ContentType: true}, fault)
			if len(failures(results)) == 0 {
				continue
			}

			for format, report := range reporters(&bytes.Buffer{}) {
				t.Run(name+"/"+string(fault)+"/"+format, func(t *testing.T) {
					var out bytes.Buffer
					report = reporters(&out)[format]

					// A panic here is the failure being guarded against, and
					// the test framework reports it with the case that caused
					// it, which is what makes this worth running broadly.
					report.Report(results)

					if out.Len() == 0 {
						t.Error("reported nothing at all for a failing run")
					}
					if !utf8.Valid(out.Bytes()) {
						t.Error("output is not valid UTF-8, so a terminal or a browser will mangle it")
					}
					if strings.ContainsRune(out.String(), 0) {
						t.Error("output contains a NUL byte, which no reader handles")
					}
				})
			}
		}
	}
}

// TestJUnitIsAlwaysWellFormedXML pins the one reporter whose output is parsed
// by another program rather than read by a person.
//
// A CI server given malformed XML reports the whole run as an infrastructure
// error, which is worse than reporting the failures: the failures disappear.
// Bodies containing control characters, invalid UTF-8 or angle brackets are
// exactly what provokes it.
func TestJUnitIsAlwaysWellFormedXML(t *testing.T) {
	for _, name := range corpus.Names() {
		for _, fault := range []corpus.Fault{
			corpus.FaultBodyViolatesSchema,
			corpus.FaultWrongStatus,
			corpus.FaultWrongContentType,
		} {
			results := run(t, name, runner.Checks{ServerError: true, ContentType: true}, fault)
			if len(failures(results)) == 0 {
				continue
			}

			t.Run(name+"/"+string(fault), func(t *testing.T) {
				var out bytes.Buffer
				(&reporter.JUnit{Out: &out}).Report(results)

				decoder := xml.NewDecoder(bytes.NewReader(out.Bytes()))
				for {
					_, err := decoder.Token()
					if err != nil {
						if err.Error() == "EOF" {
							return
						}
						t.Fatalf("not well-formed XML: %v\n%s", err, out.String())
					}
				}
			})
		}
	}
}
