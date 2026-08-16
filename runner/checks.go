package runner

import (
	"strconv"
	"strings"

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
}

// run performs the enabled checks and returns what they found.
func (c Checks) run(expected, actual validate.Message) []string {
	var findings []string

	if c.ServerError {
		if finding, found := checkServerError(actual); found {
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
	return findings
}

// checkServerError reports a 5xx.
//
// Dredd reports this only as "expected 200, got 500", which reads like any
// other mismatch. It is not: a 5xx means the server broke rather than
// disagreed, and it is worth naming as its own kind of failure — it is the
// single most common thing an API test finds.
func checkServerError(actual validate.Message) (string, bool) {
	status, err := strconv.Atoi(strings.TrimSpace(actual.StatusCode))
	if err != nil || status < 500 || status > 599 {
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
