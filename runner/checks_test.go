package runner

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/validate"
)

// TestContentTypeIsNotComparedAcrossAStatusMismatch pins a fix for a false
// finding vertrag reported against a real server.
//
// A card-reader endpoint documented as returning a binary download answered 404
// with a JSON error body, because nothing had been read. vertrag reported it as
// a handler contradicting its description. It was not: the 200 path was never
// reached, and a 404's content type says nothing about what the 200 promised.
func TestContentTypeIsNotComparedAcrossAStatusMismatch(t *testing.T) {
	checks := Checks{ContentType: true}

	expected := validate.Message{
		StatusCode: "200",
		Headers:    map[string]string{"Content-Type": "application/octet-stream"},
	}
	wrongStatus := validate.Message{
		StatusCode: "404",
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       `{"error":"No card has been read"}`,
	}
	if findings := checks.run(expected, wrongStatus); len(findings) != 0 {
		t.Errorf("a mismatched status should suppress the comparison, got %v", findings)
	}

	// With the status matching, the comparison means something again.
	rightStatus := wrongStatus
	rightStatus.StatusCode = "200"
	findings := checks.run(expected, rightStatus)
	if len(findings) != 1 || !strings.Contains(findings[0], "promises application/octet-stream") {
		t.Errorf("a genuine mismatch should still be reported, got %v", findings)
	}
}

// TestCharsetIsNotAViolation records a deliberate difference from Gavel, which
// fails `application/json; charset=utf-8` against an expectation of
// `application/json`. A charset the description did not mention does not break
// any client.
func TestCharsetIsNotAViolation(t *testing.T) {
	findings := Checks{ContentType: true}.run(
		validate.Message{StatusCode: "200", Headers: map[string]string{"Content-Type": "application/json"}},
		validate.Message{StatusCode: "200", Headers: map[string]string{"content-type": "application/json; charset=utf-8"}},
	)
	if len(findings) != 0 {
		t.Errorf("a charset should not be a violation, got %v", findings)
	}
}

func TestServerErrorIsReportedRegardlessOfStatusExpectation(t *testing.T) {
	// Unlike the content type, a 5xx is worth saying whatever was expected: the
	// server failed rather than disagreed.
	findings := Checks{ServerError: true}.run(
		validate.Message{StatusCode: "200"},
		validate.Message{StatusCode: "500"},
	)
	if len(findings) != 1 || !strings.Contains(findings[0], "failed rather than disagreed") {
		t.Errorf("a 5xx should be reported, got %v", findings)
	}
}
