package reporter

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// summary is the count every format ends with.
//
// It is shared so the four numbers cannot disagree between two reports of the
// same run — a pipeline that writes a terminal log and an HTML file publishes
// both, and a reader who spots them contradicting each other stops believing
// either.
type summary struct {
	Total    int
	Passing  int
	Failing  int
	Errors   int
	Skipped  int
	Duration time.Duration
}

func tally(results []runner.Result) summary {
	counted := summary{Total: len(results)}
	for _, result := range results {
		counted.Duration += result.Duration
		switch result.Status {
		case runner.StatusPass:
			counted.Passing++
		case runner.StatusFail:
			counted.Failing++
		case runner.StatusError:
			counted.Errors++
		case runner.StatusSkip:
			counted.Skipped++
		}
	}
	return counted
}

// Passed reports whether the run may claim the API conforms. An error counts
// against it: the API was never successfully asked.
//
// It is exported because the HTML template calls it.
func (s summary) Passed() bool { return s.Failing == 0 && s.Errors == 0 }

// Line is the sentence a reader looks at first, so it is worded the same
// everywhere.
func (s summary) Line() string {
	return fmt.Sprintf("%d passing, %d failing, %d errors, %d skipped (%d total, %s)",
		s.Passing, s.Failing, s.Errors, s.Skipped, s.Total, s.Duration.Round(time.Millisecond))
}

// document is a run reduced to what a report says about it.
//
// The Markdown and HTML reporters are one report in two syntaxes, so they build
// this first and render it second. Deriving each format straight from the
// results would leave the two free to drift, and two documents describing the
// same run differently is worse than either of them being wrong.
type document struct {
	Title   string
	Summary summary
	Cases   []documentCase
}

// documentCase is one transaction as a report shows it.
//
// Every field is already the text that will appear. Deciding what a duration or
// a payload looks like once, here, is what keeps the two renderers from having
// opinions of their own about content.
type documentCase struct {
	Name     string
	Method   string
	URI      string
	Status   string
	Duration string
	// Errors explains a failure or an error. Beyond is kept apart because a
	// project arriving from Dredd meets those findings for the first time, and
	// an unlabelled new failure reads as a regression rather than as a contract
	// violation that was going unnoticed.
	Errors []string
	Beyond []string
	// Request and Response are the exchange, preformatted. They are empty when
	// the report is not showing this transaction in detail.
	Request  string
	Response string
}

// Detailed reports whether this case has anything to show beyond its row in the
// summary table. The HTML template calls it to decide whether to write a
// section at all.
func (c documentCase) Detailed() bool {
	return len(c.Errors) > 0 || len(c.Beyond) > 0 || c.Request != "" || c.Response != ""
}

// newDocument reduces a run to what a report will say about it.
//
// details asks for the exchange of passing transactions too, matching the CLI
// reporter's flag. A failure always carries its exchange whatever that flag
// says: a report of a failure that does not record what was sent cannot be
// acted on without running the suite again, which is the one thing a written
// report exists to avoid.
func newDocument(title string, results []runner.Result, details bool) document {
	doc := document{Title: title, Summary: tally(results)}

	for _, result := range results {
		failed := result.Status == runner.StatusFail || result.Status == runner.StatusError

		entry := documentCase{
			Name:     result.Name,
			Method:   result.Request.Method,
			URI:      result.Request.URI,
			Status:   string(result.Status),
			Duration: result.Duration.Round(time.Millisecond).String(),
			Errors:   result.Errors,
			Beyond:   result.Beyond,
		}
		if failed || details {
			entry.Request = requestText(result.Request)
			entry.Response = responseText(result.Actual)
		}

		doc.Cases = append(doc.Cases, entry)
	}
	return doc
}

// requestText renders what was sent as the HTTP exchange it was, which is what
// lets a reader repeat it by hand against the server.
func requestText(request runner.Request) string {
	if request.Method == "" && request.URI == "" {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", request.Method, request.URI)
	writeHeaders(&b, request.Headers)
	if request.Body != "" {
		fmt.Fprintf(&b, "\n%s\n", truncate(request.Body))
	}
	return strings.TrimRight(b.String(), "\n")
}

// responseText renders what came back, or nothing at all when the request never
// reached the server — an empty block would read as an empty response, which is
// a different and much more alarming thing.
func responseText(message validate.Message) string {
	if message.StatusCode == "" {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", message.StatusCode)
	writeHeaders(&b, message.Headers)
	if message.Body != "" {
		fmt.Fprintf(&b, "\n%s\n", truncate(message.Body))
	}
	return strings.TrimRight(b.String(), "\n")
}

// writeHeaders emits headers in name order, so two reports of the same failure
// can be diffed without Go's map iteration inventing changes.
func writeHeaders(b *strings.Builder, headers map[string]string) {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(b, "%s: %s\n", name, headers[name])
	}
}
