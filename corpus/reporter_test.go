package corpus_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

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
		"har":      &reporter.HAR{Out: out},
		"vcr":      &reporter.VCR{Out: out},
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

// TestASkipSaysWhyInEveryReporter pins that a transaction which did not run
// explains itself.
//
// A skip is the one outcome a reader cannot interpret without a reason: a
// transaction excluded on purpose and a step abandoned because what it depended
// on failed look identical, and only one of them is a problem. JUnit wrote
// "skipped by a hook" for both, which is worse than saying nothing — it sends
// the reader to look at hooks that had nothing to do with it.
func TestASkipSaysWhyInEveryReporter(t *testing.T) {
	results := []runner.Result{{
		Name:    "a > skipped step",
		Status:  runner.StatusSkip,
		Request: runner.Request{Method: "GET", URI: "/a"},
		Errors:  []string{"the step it follows did not pass"},
	}}

	for format, report := range reporters(&bytes.Buffer{}) {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			reporters(&out)[format].Report(results)
			_ = report

			rendered := out.String()
			if strings.Contains(rendered, "skipped by a hook") {
				t.Error("claims a hook skipped it, which nothing here did")
			}
			// The dot reporter prints one character per transaction and a
			// summary; there is nowhere in that format for a reason to go. The
			// two recording formats have nowhere either, and for a stronger
			// reason: a skipped transaction was never sent, so a recording of
			// the run's traffic holds no entry for it to explain.
			switch format {
			case "dot", "har", "vcr":
				return
			}
			if !strings.Contains(rendered, "did not pass") {
				t.Errorf("does not say why the step was skipped:\n%s", rendered)
			}
		})
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

// TestACassetteIsAlwaysParseableAcrossTheCorpus pins the two formats a machine
// reads back rather than a person, for the reason the JUnit check above exists:
// a recording that will not parse is worse than none, because the failure it was
// meant to make reproducible is now sealed inside a file nothing will open.
//
// The corpus is what makes this worth running broadly. Its descriptions carry
// bodies with astral-plane unicode, NUL bytes, angle brackets and keys colliding
// with JSON Pointer syntax — the content that breaks a serialiser — and each is
// run against a faulty server, because the failure path is where bodies and
// headers are actually written out.
func TestACassetteIsAlwaysParseableAcrossTheCorpus(t *testing.T) {
	for _, name := range corpus.Names() {
		for _, fault := range corpus.Faults() {
			results := run(t, name, runner.Checks{ServerError: true, ContentType: true}, fault)
			if len(failures(results)) == 0 {
				continue
			}

			t.Run(name+"/"+string(fault), func(t *testing.T) {
				var har bytes.Buffer
				(&reporter.HAR{Out: &har}).Report(results)
				var archive struct {
					Log struct {
						Entries []struct {
							Request struct {
								Method string `json:"method"`
								URL    string `json:"url"`
							} `json:"request"`
						} `json:"entries"`
					} `json:"log"`
				}
				if err := json.Unmarshal(har.Bytes(), &archive); err != nil {
					t.Fatalf("the archive does not parse as JSON: %v\n%s", err, har.String())
				}

				var cassette bytes.Buffer
				(&reporter.VCR{Out: &cassette}).Report(results)
				var recording struct {
					Interactions []struct {
						Request struct {
							Method string `yaml:"method"`
							URI    string `yaml:"uri"`
						} `yaml:"request"`
					} `yaml:"http_interactions"`
				}
				if err := yaml.Unmarshal(cassette.Bytes(), &recording); err != nil {
					t.Fatalf("the cassette does not parse as YAML: %v\n%s", err, cassette.String())
				}

				// Both files record the same traffic, so they hold the same
				// requests. Two recordings of one run that disagree about what
				// was sent leave the reader unable to trust either.
				if len(archive.Log.Entries) != len(recording.Interactions) {
					t.Fatalf("the archive holds %d entries and the cassette %d",
						len(archive.Log.Entries), len(recording.Interactions))
				}
				for i := range archive.Log.Entries {
					if archive.Log.Entries[i].Request.URL != recording.Interactions[i].Request.URI {
						t.Errorf("entry %d: the archive recorded %q and the cassette %q", i,
							archive.Log.Entries[i].Request.URL, recording.Interactions[i].Request.URI)
					}
				}
			})
		}
	}
}
