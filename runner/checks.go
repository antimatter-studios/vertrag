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
// server can answer with a content type the document never mentions, or fail
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
}

// run performs the enabled checks and returns what they found.
func (c Checks) run(expected, actual validate.Message) []string {
	var findings []string

	if c.ServerError {
		if finding, found := checkServerError(actual); found {
			findings = append(findings, finding)
		}
	}
	if c.ContentType {
		if finding, found := checkContentType(expected, actual); found {
			findings = append(findings, finding)
		}
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
// Dredd checks that the expected headers are *present* and never compares their
// values, so a document promising application/json and a server answering
// text/html passes. Parameters are ignored: a charset the document did not
// mention is not a contract violation.
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
