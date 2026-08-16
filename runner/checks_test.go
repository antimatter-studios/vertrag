package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
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

// headerSchemaExpectation is a 200 promising a non-negative integer rate limit.
func headerSchemaExpectation() validate.Message {
	return validate.Message{
		StatusCode: "200",
		Headers:    map[string]string{"X-Rate-Limit": ""},
		HeaderSchemas: map[string]json.RawMessage{
			"X-Rate-Limit": json.RawMessage(`{"type":"integer","minimum":0}`),
		},
	}
}

// TestHeaderSchemasAreOnlyCheckedWhenAskedFor pins the opt-in.
//
// Dredd never looked at a header's value, so no description has ever had its
// header schemas enforced and a suite adopting vertrag would meet all of its own
// at once. Turning that into red unasked reads as vertrag being broken rather
// than as the description being wrong, which is how a useful check gets switched
// off for good.
func TestHeaderSchemasAreOnlyCheckedWhenAskedFor(t *testing.T) {
	violating := validate.Message{
		StatusCode: "200",
		Headers:    map[string]string{"x-rate-limit": "banana"},
	}

	if findings := (Checks{}).run(headerSchemaExpectation(), violating); len(findings) != 0 {
		t.Errorf("the check must be off unless asked for, got %v", findings)
	}

	findings := Checks{HeaderSchema: true}.run(headerSchemaExpectation(), violating)
	if len(findings) != 1 || !strings.Contains(findings[0], "X-Rate-Limit") {
		t.Errorf("once asked for it should report the violation, got %v", findings)
	}
}

// TestHeaderSchemasAreNotCheckedAcrossAStatusMismatch pins the same reasoning
// TestContentTypeIsNotComparedAcrossAStatusMismatch records: the schemas belong
// to the response the expectation names, and an error response's headers are no
// evidence about what the documented success promised.
func TestHeaderSchemasAreNotCheckedAcrossAStatusMismatch(t *testing.T) {
	wrongStatus := validate.Message{
		StatusCode: "503",
		Headers:    map[string]string{"x-rate-limit": "unavailable"},
	}

	findings := Checks{HeaderSchema: true}.run(headerSchemaExpectation(), wrongStatus)
	if len(findings) != 0 {
		t.Errorf("a mismatched status should suppress the check, got %v", findings)
	}
}

// TestAViolatingHeaderFailsAWholeRun joins the pieces the unit tests exercise
// separately: the schema a compiled transaction carries, a live server, and the
// verdict a reporter prints.
//
// It is the only test that proves the schema survives compile.Response and
// reaches the check, which is the part a refactor would silently break — the
// field is invisible in the compiled JSON by design, so nothing else notices if
// it stops being populated.
func TestAViolatingHeaderFailsAWholeRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Rate-Limit", "banana")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	source := compile.Transaction{
		Name:    "limits",
		Request: compile.Request{Method: "GET", URI: "/limits"},
		Response: compile.Response{
			Status:  "200",
			Headers: []compile.Header{{Name: "X-Rate-Limit"}},
			HeaderSchemas: map[string]json.RawMessage{
				"X-Rate-Limit": json.RawMessage(`{"type":"integer","minimum":0}`),
			},
		},
	}

	engine := New(server.URL)
	engine.Checks = Checks{HeaderSchema: true}

	results, err := engine.Run(context.Background(), []compile.Transaction{source})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if results[0].Status != StatusFail {
		t.Fatalf("status = %s, want fail", results[0].Status)
	}
	if len(results[0].Beyond) != 1 || !strings.Contains(results[0].Beyond[0], "X-Rate-Limit") {
		t.Errorf("findings = %v, want the header named", results[0].Beyond)
	}

	// The same run without the check is green, which is what makes adopting
	// vertrag safe for a suite whose descriptions have never been enforced.
	engine.Checks = Checks{}
	results, err = engine.Run(context.Background(), []compile.Transaction{source})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if results[0].Status != StatusPass {
		t.Errorf("status = %s, want pass: %v", results[0].Status, results[0].Errors)
	}
}
