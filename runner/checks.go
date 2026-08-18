package runner

import (
	"strconv"
	"strings"
	"time"

	"github.com/antimatter-studios/vertrag/validate"
)

// Checks that Dredd does not make.
//
// Dredd compares the status code, the presence of the expected headers, and the
// body against its schema. That leaves gaps a contract test ought to close: a
// server can answer with a content type the document never mentions, return a
// header whose value contradicts the schema the description gave it, or fail
// outright with a 5xx nobody documented, and Dredd reports only that the status
// was not the expected one.
//
// These findings are kept apart from the ordinary ones so a report can say
// which failures Dredd would not have raised. A project upgrading from Dredd
// will see new failures; they are contract violations that were going
// unnoticed, and saying so plainly is the difference between a useful finding
// and an apparent regression.

// Checks selects which of the additional checks run.
type Checks struct {
	ServerError bool
	ContentType bool

	// HeaderSchema validates response header values against the schemas the
	// description gave them. It is off unless a run asks for it, unlike its
	// neighbours: those two report things nobody disputes are wrong, while this
	// one has to decide what a header's text means before it can judge it, and
	// a description whose header schemas were never enforced is quite likely to
	// contain one nobody ever checked. Turning that into red on the first run
	// after adopting vertrag would be a poor trade.
	HeaderSchema bool

	// IgnoredAuth re-sends each authenticated request WITHOUT the credential
	// and requires the server to refuse it.
	//
	// An endpoint that answers the same whether or not a credential is
	// present is not authenticated, however carefully the description says it
	// is — and nothing in an ordinary run can tell, because every request
	// carries the credential and every response is therefore correct. It is
	// off by default because it doubles the requests a run makes, and because
	// a suite whose auth is genuinely absent would light up entirely rather
	// than usefully.
	IgnoredAuth bool

	// MaxResponseTime is how long a response may take before it is reported.
	// Zero, the default, means nothing is timed.
	//
	// It is the only check here that judges something the description does not
	// say. OpenAPI has no way to write "this endpoint answers within 750ms", so
	// the bound can only come from the run — and any number vertrag picked for
	// you would be wrong for every project, which is why there is no default
	// bound rather than a generous one.
	//
	// What is measured is Result.ResponseTime — the exchange alone — and not
	// Result.Duration, which is the whole transaction. The two differ by
	// everything the run does around the request: the pause `transport.delay`
	// takes to spare a throttled server, the backoff before a retried network
	// failure, the hooks. Judging the bound on the whole of it meant a suite
	// with `delay: 500ms` and `max-response-time: 750ms` reported the server
	// as slow when the server had answered at once, and the only slow thing in
	// the run was the courtesy it had been configured to extend.
	MaxResponseTime time.Duration
}

// run performs the enabled checks and returns what they found. responseTime is
// how long the server took, which is not in the message pair and has to be
// passed alongside it.
func (c Checks) run(expected, actual validate.Message, responseTime time.Duration) []string {
	var findings []string

	if c.ServerError {
		if finding, found := checkServerError(expected, actual); found {
			findings = append(findings, finding)
		}
	}
	// The content type is only worth comparing when the status matched. A
	// response with a different status is a DIFFERENT documented response — a
	// 404 error body is JSON whatever the 200 promised — so comparing it
	// against this expectation reports a disagreement that does not exist.
	//
	// Found the hard way: a card-reader endpoint answering 404 "no card has
	// been read" was reported as a handler returning JSON where its
	// description promised a binary download. The 200 path was never reached.
	if c.ContentType && statusMatches(expected, actual) {
		if finding, found := checkContentType(expected, actual); found {
			findings = append(findings, finding)
		}
	}
	// Gated on the status for the same reason as the content type: the schemas
	// belong to the response the expectation names, and a 404's headers are not
	// evidence about what the documented 200 promised.
	if c.HeaderSchema && statusMatches(expected, actual) {
		findings = append(findings,
			validate.AgainstHeaderSchemas(expected.HeaderSchemas, actual.Headers)...)
	}
	// Not gated on the status, unlike the two above it. A response that took
	// four seconds took four seconds whichever of the documented responses it
	// turned out to be, and an error path is where a timeout most often hides
	// — the retry loop nobody bounded, the lookup that only misses.
	if finding, found := checkResponseTime(c.MaxResponseTime, responseTime); found {
		findings = append(findings, finding)
	}
	return findings
}

// checkServerError reports a 5xx.
//
// Dredd reports this only as "expected 200, got 500", which reads like any
// other mismatch. It is not: a 5xx means the server broke rather than
// disagreed, and it is worth naming as its own kind of failure — it is the
// single most common thing an API test finds.
func checkServerError(expected, actual validate.Message) (string, bool) {
	status, err := strconv.Atoi(strings.TrimSpace(actual.StatusCode))
	if err != nil || status < 500 || status > 599 {
		return "", false
	}

	// A 5xx the description documents is not a failure, it is the documented
	// outcome. Some APIs publish an error contract and a test that reports
	// conformance to it as a fault is reporting the description for existing.
	if statusMatches(expected, actual) {
		return "", false
	}
	return "the server returned " + actual.StatusCode +
		", which means it failed rather than disagreed", true
}

// checkContentType reports a response carrying a media type the description did
// not promise.
//
// Dredd compares the content type too — it is the one header whose value Gavel
// compares, the rest being presence-only — so this is not a check Dredd lacks.
// Where the two differ is parameters: Gavel fails `application/json;
// charset=utf-8` against an expectation of `application/json`, and this does
// not. A charset the document did not mention is not a contract violation, and
// failing on one sends people to edit their descriptions rather than fix
// anything. vertrag is the more lenient of the two here, not the stricter.
func checkContentType(expected, actual validate.Message) (string, bool) {
	want := baseMediaType(headerValue(expected.Headers, "content-type"))
	if want == "" {
		return "", false
	}

	got := baseMediaType(headerValue(actual.Headers, "content-type"))
	if got == "" {
		return "the response carries no Content-Type, but the description promises " + want, true
	}
	if got != want {
		return "the response is " + got + ", but the description promises " + want, true
	}
	return "", false
}

// checkResponseTime reports a response slower than the bound the run set.
//
// A response rather than a transaction, and the distinction is the whole point
// of the separate measurement: what is judged is the time the server spent, not
// the time the run spent, so a paced or retried run is not reported for waits
// it chose to take. See Result.ResponseTime.
//
// A bound of zero is not "answer instantly", it is "do not time this at all" —
// there is no bound an unconfigured run could apply that would not be an
// opinion vertrag invented, and a check that fires on a number nobody chose is
// a check people switch off rather than read.
//
// The finding is deliberately not a contract error. Nothing in the description
// was contradicted: the status, the headers and the body are all exactly what
// was promised, and the only thing wrong is a bound that lives in the run's
// configuration. Reporting it as though the document had been violated would
// send the reader to edit a document that says nothing about time.
func checkResponseTime(bound, responseTime time.Duration) (string, bool) {
	if bound <= 0 || responseTime <= bound {
		return "", false
	}

	// Rounded, because the microseconds vary run to run and nobody can act on
	// them — except under a bound finer than a millisecond, where rounding
	// would report "took 0s, longer than the 500µs bound" and read as a fault
	// in the checker rather than a slow response.
	took := responseTime.Round(time.Millisecond)
	if bound < time.Millisecond {
		took = responseTime
	}
	return "the response took " + took.String() +
		", longer than the " + bound.String() + " this run allows", true
}

func headerValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

// baseMediaType strips parameters, so `application/json; charset=utf-8` and
// `application/json` compare equal.
func baseMediaType(value string) string {
	if i := strings.IndexByte(value, ';'); i >= 0 {
		value = value[:i]
	}
	return strings.ToLower(strings.TrimSpace(value))
}

// statusMatches reports whether the response is the one the expectation
// describes.
func statusMatches(expected, actual validate.Message) bool {
	return strings.TrimSpace(expected.StatusCode) == strings.TrimSpace(actual.StatusCode)
}
