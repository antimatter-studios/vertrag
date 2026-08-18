package reporter

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/antimatter-studios/vertrag/runner"
)

// A cassette is the run's HTTP traffic written out as a file — HAR for a
// browser's network panel and the many tools that import one, VCR for the
// replay libraries. It answers the question no verdict-shaped report can. The
// CLI, Markdown, HTML and JUnit reporters all say whether the API agreed with
// its description; a cassette says what actually went over the wire, which is
// what somebody reproducing a failure on another machine needs. Until this
// existed the only way to get it was to rerun the suite with a proxy in front
// of it, and a failure that only happens in CI cannot be rerun that way.
//
// Both formats are derived from the same exchanges, for the reason Markdown
// and HTML share a document: two files describing one run differently is worse
// than either of them being wrong, and a reader who catches them disagreeing
// stops trusting both.
//
// Every header value goes through Redact, which is the whole reason this file
// exists rather than each format walking the header maps itself. A cassette is
// committed to a repository and attached to tickets far more readily than a
// terminal log is, so it is the report where a leaked bearer token travels
// furthest — and it is written to be replayed, meaning somebody will later
// point it at a machine and wonder why the credential in it still works.
// Bodies are left alone, the same as in every other reporter: there is no way
// to know which field of a payload is a secret, and guessing would hide the
// payload a failure exists to show. So a cassette of a login exchange still
// holds the password that was posted, which is worth knowing before committing
// one.

// nameValue is a header or a query parameter. Both formats want an ordered
// list rather than a map, and having one means two cassettes of the same run
// are byte-identical — a run diffed against yesterday's shows what changed
// instead of what Go's map iteration felt like today.
type nameValue struct {
	Name  string
	Value string
}

// exchange is one request and the answer it got, reduced to what a cassette
// records.
//
// Every value here is already final: redacted, ordered, parsed. Neither format
// gets an opinion of its own about content, so a change to the redaction list
// cannot reach the HAR file and miss the cassette beside it.
type exchange struct {
	Name   string
	Status runner.Status

	Method         string
	URL            string
	Query          []nameValue
	RequestHeaders []nameValue
	RequestBody    string

	Started  time.Time
	Duration time.Duration

	// Answered says the server replied. A request that got no reply is still
	// recorded — it is frequently the one somebody most wants to repeat — but
	// there is nothing to play back, so each format says so in its own way
	// rather than inventing a response that never arrived.
	Answered        bool
	StatusCode      int
	StatusText      string
	ResponseHeaders []nameValue
	ResponseBody    string
	// Unanswered is what happened instead of a reply.
	Unanswered string
}

// exchanges reduces a run to the traffic a cassette records.
//
// A skipped transaction never reached the network, so there is nothing to
// record: a hook took it out, or a sequenced step's dependency failed and
// sending it would have meant asking for an identifier nothing created. The
// omission is not a silence — every other reporter still counts the skip — but
// it does mean a cassette holds fewer entries than the JUnit file beside it has
// test cases, which is the correct number for a recording of what was sent.
//
// now stamps an exchange that has no time of its own. `vertrag run` records
// when each request went out; the probing commands assemble their results by
// hand and do not, and both cassette formats require a timestamp per entry.
func exchanges(results []runner.Result, now time.Time) []exchange {
	recorded := make([]exchange, 0, len(results))

	for _, result := range results {
		if result.Status == runner.StatusSkip || result.Request.URL == "" {
			continue
		}

		entry := exchange{
			Name:           result.Name,
			Status:         result.Status,
			Method:         result.Request.Method,
			URL:            result.Request.URL,
			Query:          queryOf(result.Request.URL),
			RequestHeaders: recordedHeaders(result.Request.Headers),
			RequestBody:    RedactSecrets(result.Request.Body),
			Started:        result.Started,
			Duration:       result.Duration,
		}
		if entry.Started.IsZero() {
			entry.Started = now
		}

		// A status code that is absent or unparseable means no response was
		// recorded against this transaction. The probing commands are the usual
		// reason: they keep the request that provoked a finding and report the
		// finding in words, without storing the response it came from.
		if code, err := strconv.Atoi(strings.TrimSpace(result.Actual.StatusCode)); err == nil && code > 0 {
			entry.Answered = true
			entry.StatusCode = code
			entry.StatusText = http.StatusText(code)
			entry.ResponseHeaders = recordedHeaders(result.Actual.Headers)
			entry.ResponseBody = RedactSecrets(result.Actual.Body)
		} else {
			entry.Unanswered = strings.Join(result.Errors, "; ")
			if entry.Unanswered == "" {
				entry.Unanswered = "no response was recorded"
			}
		}

		recorded = append(recorded, entry)
	}

	return recorded
}

// recordedHeaders renders a header map as an ordered, redacted list.
//
// The slice is never nil, because both formats have a place where the absence
// of headers must still be written as an empty collection: a HAR consumer
// reading null where it expects an array is the sort of thing that fails at
// import time with nothing useful said about which entry did it.
func recordedHeaders(headers map[string]string) []nameValue {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	recorded := make([]nameValue, 0, len(names))
	for _, name := range names {
		recorded = append(recorded, nameValue{Name: name, Value: Redact(name, headers[name])})
	}
	return recorded
}

// queryOf splits the query string out of the URL, which is what a HAR viewer
// tabulates.
//
// It is derived rather than authoritative: the URL in the record keeps the
// parameters in the order they were sent, and this list is sorted, because
// parsing them into a map loses their order and inventing one that changes
// between runs would make two cassettes of the same run differ. A URL that
// will not parse yields nothing here and is still recorded whole, since a
// malformed address is exactly the sort of thing worth having a record of.
func queryOf(raw string) []nameValue {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil
	}

	values := parsed.Query()
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	query := make([]nameValue, 0, len(values))
	for _, name := range names {
		for _, value := range values[name] {
			query = append(query, nameValue{Name: name, Value: value})
		}
	}
	return query
}

// contentTypeOf finds the media type among already-recorded headers.
//
// Both formats have a field for it and both want the one that was really sent,
// header names being case-insensitive on the wire — a server answering with
// `content-type` should not produce a cassette whose media type is blank.
func contentTypeOf(headers []nameValue) string {
	for _, header := range headers {
		if strings.EqualFold(header.Name, "Content-Type") {
			return header.Value
		}
	}
	return ""
}

// clock is the time a report should stamp on an exchange that carries none.
//
// The reporters take the function rather than calling time.Now, so a test can
// assert on the exact bytes written instead of parsing them back and comparing
// against whatever moment the test ran at.
func clock(now func() time.Time) time.Time {
	if now == nil {
		return time.Now()
	}
	return now()
}

// Summary is the one-line description of a run: which vertrag, which
// description, which server, which config file.
//
// It is the same line `vertrag run` prints to stderr before it starts, and it
// is here so that the line and a recording's header cannot drift apart. The
// stderr line is gone by the time anybody opens an archived recording, which is
// the same reason the JUnit report carries these values as properties.
func (p Provenance) Summary() string {
	summary := "vertrag"
	if p.Version != "" {
		summary += " " + p.Version
	}
	if source := p.Source(); source != "" {
		summary += " · " + source
	}
	return summary
}

// Source is the provenance without the version: which description, which
// server, which config file.
//
// It exists for the formats that already have a field naming the tool that
// wrote them. Repeating the version in a comment beside a creator block that
// states it is how two copies of one value start disagreeing.
func (p Provenance) Source() string {
	var parts []string
	if p.Spec != "" {
		parts = append(parts, p.Spec+" → "+p.Endpoint)
	}
	if p.Config != "" {
		parts = append(parts, p.Config)
	}
	return strings.Join(parts, " · ")
}
