package openapi3

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
)

// cookieHeaderOf returns the Cookie header a compiled request carries, and
// whether it carries one at all — the two are different answers and a test
// asserting on "" alone could not tell them apart.
func cookieHeaderOf(request compile.Request) (string, bool) {
	for _, header := range request.Headers {
		if strings.EqualFold(header.Name, "Cookie") {
			return header.Value, true
		}
	}
	return "", false
}

const cookieDocument = `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /session:
    get:
      summary: S
      parameters:
        - {name: sessionId, in: cookie, example: abc123, schema: {type: string}}
        - {name: locale, in: cookie, example: en-GB, schema: {type: string}}
      responses:
        "200": {description: OK}
`

func TestACookieParameterIsSentInTheCookieHeader(t *testing.T) {
	result := compileSource(t, cookieDocument)

	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1", len(result.Transactions))
	}
	got, carried := cookieHeaderOf(result.Transactions[0].Request)
	if !carried {
		t.Fatalf("the request carries no Cookie header at all, so the parameter was dropped")
	}
	if want := "sessionId=abc123; locale=en-GB"; got != want {
		t.Errorf("Cookie = %q, want %q", got, want)
	}
}

// TestACookieParameterReachesTheRequestAsAParameter pins the carriage rather
// than the header: generation varies a parameter by name and location, so a
// cookie that arrived only as header text could never be probed.
func TestACookieParameterReachesTheRequestAsAParameter(t *testing.T) {
	result := compileSource(t, cookieDocument)

	var names []string
	for _, parameter := range result.Transactions[0].Request.Parameters {
		if parameter.In == compile.InCookie {
			names = append(names, parameter.Name)
			if parameter.Schema == "" {
				t.Errorf("cookie parameter %q carries no schema, so nothing could generate from it", parameter.Name)
			}
		}
	}
	if len(names) != 2 || names[0] != "sessionId" || names[1] != "locale" {
		t.Errorf("cookie parameters = %v, want [sessionId locale] in declaration order", names)
	}
}

func TestACookieParameterIsNoLongerReportedAsUnsupported(t *testing.T) {
	result := compileSource(t, cookieDocument)

	for _, annotation := range result.Annotations {
		if strings.Contains(annotation.Message, "'in' 'cookie'") {
			t.Errorf("cookie parameters are sent now, so warning about them is false: %q", annotation.Message)
		}
	}
}

// TestACookieParameterWithNoValueSendsNoCookie pins the distinction a query
// parameter already gets: a parameter the document demonstrated no value for
// is absent from the request rather than sent blank, because `x=` says the
// cookie was sent and is empty — a different request from the one described.
// It is still listed as a parameter, so a probe can supply one.
func TestACookieParameterWithNoValueSendsNoCookie(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /session:
    get:
      summary: S
      parameters:
        - {name: tenant, in: cookie, schema: {type: string, minLength: 2}}
      responses:
        "200": {description: OK}
`)

	request := result.Transactions[0].Request
	if got, carried := cookieHeaderOf(request); carried {
		t.Errorf("Cookie = %q, want no Cookie header at all", got)
	}
	found := false
	for _, parameter := range request.Parameters {
		if parameter.In == compile.InCookie && parameter.Name == "tenant" {
			found = true
			if parameter.HasValue {
				t.Errorf("tenant reports a value the document never gave it")
			}
		}
	}
	if !found {
		t.Errorf("the cookie parameter was dropped, so nothing could ever supply one")
	}
}

// TestADeclaredCookieHeaderBeatsTheAssembledOne covers a document that spells
// the whole header out as a header parameter. It has said what it wants sent,
// separators included, and assembling one over the top of it would send bytes
// nobody wrote.
func TestADeclaredCookieHeaderBeatsTheAssembledOne(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /session:
    get:
      summary: S
      parameters:
        - {name: Cookie, in: header, example: "a=1; b=2", schema: {type: string}}
        - {name: sessionId, in: cookie, example: abc123, schema: {type: string}}
      responses:
        "200": {description: OK}
`)

	got, _ := cookieHeaderOf(result.Transactions[0].Request)
	if want := "a=1; b=2"; got != want {
		t.Errorf("Cookie = %q, want %q", got, want)
	}
}

// TestACookieParameterStyleOtherThanFormIsReported keeps the standing rule:
// `form` is the only style defined for a cookie, and anything else is a
// serialisation vertrag does not produce.
func TestACookieParameterStyleOtherThanFormIsReported(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /session:
    get:
      summary: S
      parameters:
        - {name: sessionId, in: cookie, style: spaceDelimited, example: abc123}
      responses:
        "200": {description: OK}
`)

	for _, annotation := range result.Annotations {
		if strings.Contains(annotation.Message, "spaceDelimited") {
			return
		}
	}
	t.Errorf("no annotation named the unsupported cookie style; annotations = %v", result.Annotations)
}
