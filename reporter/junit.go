package reporter

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/antimatter-studios/vertrag/runner"
)

// JUnit writes results as JUnit XML, which is what CI systems read.
//
// The schema is written out here rather than taken from a library. The Go
// packages in this space either convert `go test` output or parse existing
// reports; none writes a report from arbitrary results, so a dependency would
// buy a struct definition and cost a supply chain.
//
// JUnit XML has no formal specification — it is whatever Ant emitted and every
// consumer since has guessed at. The subset below is the part Jenkins, GitLab
// and GitHub Actions all agree on: testsuite with counts, testcase with a
// classname and a name, and a failure or skipped element inside it.
type JUnit struct {
	Out io.Writer
	// Name labels the suite in the CI interface.
	Name string
	// Run says what produced the report. It becomes the suite's <properties>.
	Run Provenance
}

// Provenance names the run a report describes: which binary, which
// description, which server, which config file.
//
// The same four values go to stderr as the signature line before a run
// starts, and they are there because each has cost this project an afternoon
// — "which config was read" and "which of the two hubs on this port
// answered" were both unanswerable from the report. stderr is what a
// terminal shows; a pipeline archives the report and discards the console,
// so by the time anybody is reading the XML the line is gone. Carrying the
// values in the report itself is what makes the archived artefact explain
// itself.
//
// A value that is not known is left empty and is then not written, so a run
// without a config file does not claim one.
type Provenance struct {
	Version  string
	Spec     string
	Endpoint string
	Config   string
}

// properties renders the provenance as JUnit properties, in a fixed order so
// two reports of the same run are byte-identical. Nil when nothing is known,
// so the document carries no empty <properties/> element.
func (p Provenance) properties() *junitProperties {
	var list []junitProperty
	add := func(name, value string) {
		if value != "" {
			list = append(list, junitProperty{Name: name, Value: value})
		}
	}
	add("vertrag.version", p.Version)
	add("vertrag.spec", p.Spec)
	add("vertrag.endpoint", p.Endpoint)
	add("vertrag.config", p.Config)

	if len(list) == 0 {
		return nil
	}
	return &junitProperties{Properties: list}
}

type junitSuite struct {
	XMLName  xml.Name `xml:"testsuite"`
	Name     string   `xml:"name,attr"`
	Tests    int      `xml:"tests,attr"`
	Failures int      `xml:"failures,attr"`
	Errors   int      `xml:"errors,attr"`
	Skipped  int      `xml:"skipped,attr"`
	Time     string   `xml:"time,attr"`
	// Properties precede the cases: that is the order the Ant schema fixed,
	// and a consumer validating against it rejects the other.
	Properties *junitProperties `xml:"properties,omitempty"`
	Cases      []junitCase      `xml:"testcase"`
}

// junitProperties is the <properties> element: key/value pairs about the
// suite as a whole, which every consumer that matters displays or ignores
// harmlessly. The values are attributes, and encoding/xml escapes them, which
// is what lets an endpoint carry `&` in a query string without producing a
// document nothing can parse.
type junitProperties struct {
	Properties []junitProperty `xml:"property"`
}

type junitProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	ClassName string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
	Error     *junitFailure `xml:"error,omitempty"`
	Skipped   *junitSkipped `xml:"skipped,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

type junitSkipped struct {
	Message string `xml:"message,attr"`
}

// Report writes the document and returns true when the run passed.
func (r JUnit) Report(results []runner.Result) bool {
	suite := junitSuite{
		Name:       r.suiteName(),
		Tests:      len(results),
		Properties: r.Run.properties(),
	}

	var total time.Duration
	passed := true

	for _, result := range results {
		total += result.Duration

		testCase := junitCase{
			// The transaction name carries the path and operation; the method
			// makes the row identifiable at a glance in a CI list.
			Name:      result.Name,
			ClassName: className(result),
			Time:      seconds(result.Duration),
		}

		switch result.Status {
		case runner.StatusFail:
			suite.Failures++
			passed = false
			testCase.Failure = &junitFailure{
				Message: firstLine(result),
				Type:    "contract",
				Body:    detail(result),
			}
		case runner.StatusError:
			suite.Errors++
			passed = false
			testCase.Error = &junitFailure{
				Message: firstLine(result),
				Type:    "unreachable",
				Body:    detail(result),
			}
		case runner.StatusSkip:
			suite.Skipped++
			// The recorded reason, when there is one. A hook is no longer the
			// only thing that skips a transaction — a step whose dependency
			// failed is skipped by sequencing, and saying "skipped by a hook"
			// there sends the reader to look at hooks that had nothing to do
			// with it.
			testCase.Skipped = &junitSkipped{Message: skipReason(result)}
		}

		suite.Cases = append(suite.Cases, testCase)
	}
	suite.Time = seconds(total)

	fmt.Fprint(r.Out, xml.Header)
	encoder := xml.NewEncoder(r.Out)
	encoder.Indent("", "  ")
	encoder.Encode(suite)
	fmt.Fprintln(r.Out)

	return passed
}

func (r JUnit) suiteName() string {
	if r.Name != "" {
		return r.Name
	}
	return "vertrag"
}

// className groups the cases. CI interfaces treat it as a package, so grouping
// by method puts every GET together, which is how a reader scans a long list.
func className(result runner.Result) string {
	if result.Request.Method == "" {
		return "vertrag"
	}
	return "vertrag." + result.Request.Method
}

// firstLine is the one-line summary a CI list shows without expanding.
func firstLine(result runner.Result) string {
	for _, message := range result.Errors {
		return strings.SplitN(message, "\n", 2)[0]
	}
	for _, message := range result.Beyond {
		return strings.SplitN(message, "\n", 2)[0]
	}
	return string(result.Status)
}

// detail is the body a reader expands to, holding everything known about the
// failure — including what was sent, which is what makes it reproducible.
func detail(result runner.Result) string {
	var b strings.Builder

	for _, message := range result.Errors {
		fmt.Fprintf(&b, "%s\n", message)
	}
	for _, message := range result.Beyond {
		fmt.Fprintf(&b, "[additional check] %s\n", message)
	}

	fmt.Fprintf(&b, "\nrequest: %s %s\n", result.Request.Method, result.Request.URI)
	writeIndentedHeaders(&b, result.Request.Headers)
	if result.Request.Body != "" {
		fmt.Fprintf(&b, "\n%s\n", truncate(result.Request.Body))
	}

	if result.Actual.StatusCode != "" {
		fmt.Fprintf(&b, "\nresponse: %s\n", result.Actual.StatusCode)
		writeIndentedHeaders(&b, result.Actual.Headers)
		if result.Actual.Body != "" {
			fmt.Fprintf(&b, "\n%s\n", truncate(result.Actual.Body))
		}
	}

	return b.String()
}

// truncate keeps a report readable when a payload is enormous.
// maxReportedBodyBytes is how much of a payload a report shows before saying
// how much it left out.
//
// Long enough for a body whose difference is somewhere in the middle, short
// enough that one megabyte of JSON does not bury every other finding in the
// run. It lives here rather than beside each use because two reports of the
// same failure truncating at different points cannot be compared, and a
// constant declared twice drifts the first time anyone tunes it.
const maxReportedBodyBytes = 2000

func truncate(body string) string {
	if len(body) <= maxReportedBodyBytes {
		return body
	}
	return body[:maxReportedBodyBytes] +
		fmt.Sprintf("… (%d bytes truncated)", len(body)-maxReportedBodyBytes)
}

func seconds(d time.Duration) string {
	return fmt.Sprintf("%.3f", d.Seconds())
}

// skipReason says why a transaction did not run.
//
// A hook may skip one without explaining itself, which is the only case left
// where nothing better can be said.
func skipReason(result runner.Result) string {
	if len(result.Errors) > 0 {
		return strings.Join(result.Errors, "; ")
	}
	return "skipped without a reason given"
}

// writeIndentedHeaders renders headers in a fixed order, indented for the body
// of a failure element.
//
// The document reporters already sorted theirs; this one walked the map
// directly, so the same results produced a different report every time. Nothing
// about a single run shows it, and the cost lands on the reader: a CI job
// diffing this report against the previous one sees change where nothing
// changed, and a failure cannot be confirmed identical to the one somebody else
// saw.
func writeIndentedHeaders(b *strings.Builder, headers map[string]string) {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		fmt.Fprintf(b, "  %s: %s\n", name, headers[name])
	}
}
