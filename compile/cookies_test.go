package compile

import (
	"strings"
	"testing"
)

func cookieHeader(request Request) string {
	for _, header := range request.Headers {
		if strings.EqualFold(header.Name, "Cookie") {
			return header.Value
		}
	}
	return ""
}

func cookieRequest() Request {
	return Request{
		Method:  "GET",
		URI:     "/session",
		Headers: []Header{{Name: "Cookie", Value: "sessionId=abc123; locale=en-GB"}},
		Parameters: []Parameter{
			{In: InCookie, Name: "sessionId", Value: "abc123", HasValue: true},
			{In: InCookie, Name: "locale", Value: "en-GB", HasValue: true},
		},
	}
}

// TestSettingACookieParameterKeepsTheOtherCookies is the cookie form of the
// composition problem SetParameter already had to solve for the query string:
// two sequential sets must both survive, or a probe meaning to vary two
// parameters varies one and reports about the wrong thing.
func TestSettingACookieParameterKeepsTheOtherCookies(t *testing.T) {
	request := cookieRequest()

	first, err := request.SetParameter(request.Parameters[0], "generated")
	if err != nil {
		t.Fatalf("SetParameter(sessionId): %v", err)
	}
	second, err := first.SetParameter(first.Parameters[1], "fr-FR")
	if err != nil {
		t.Fatalf("SetParameter(locale): %v", err)
	}

	if want := "sessionId=generated; locale=fr-FR"; cookieHeader(second) != want {
		t.Errorf("Cookie = %q, want %q", cookieHeader(second), want)
	}
}

// TestSettingACookieParameterLeavesTheCompiledRequestAlone pins that a Request
// is copied rather than written through, the way setHeader already is: the
// compiled transaction is what every later request is built from.
func TestSettingACookieParameterLeavesTheCompiledRequestAlone(t *testing.T) {
	request := cookieRequest()

	if _, err := request.SetParameter(request.Parameters[0], "generated"); err != nil {
		t.Fatalf("SetParameter: %v", err)
	}
	if want := "sessionId=abc123; locale=en-GB"; cookieHeader(request) != want {
		t.Errorf("the compiled request changed: Cookie = %q, want %q", cookieHeader(request), want)
	}
}

// TestSettingACookieParameterTheRequestDidNotCarryAddsIt covers an optional
// cookie the description gave no example for: it is absent from the compiled
// request entirely, and a probe asking for it must still be able to send one.
func TestSettingACookieParameterTheRequestDidNotCarryAddsIt(t *testing.T) {
	request := Request{Method: "GET", URI: "/session"}

	sent, err := request.SetParameter(Parameter{In: InCookie, Name: "tenant"}, "acme")
	if err != nil {
		t.Fatalf("SetParameter: %v", err)
	}
	if want := "tenant=acme"; cookieHeader(sent) != want {
		t.Errorf("Cookie = %q, want %q", cookieHeader(sent), want)
	}
}
