package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/validate"
)

// untimed is the elapsed time handed to a check that is not about time. Every
// test below it leaves the response-time bound at zero, so the value cannot
// affect the outcome — naming it says so, where a bare 0 in the argument list
// invites the reader to look for the significance it does not have.
const untimed = time.Duration(0)

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
	if findings := checks.run(expected, wrongStatus, untimed); len(findings) != 0 {
		t.Errorf("a mismatched status should suppress the comparison, got %v", findings)
	}

	// With the status matching, the comparison means something again.
	rightStatus := wrongStatus
	rightStatus.StatusCode = "200"
	findings := checks.run(expected, rightStatus, untimed)
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
		untimed,
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
		untimed,
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

	if findings := (Checks{}).run(headerSchemaExpectation(), violating, untimed); len(findings) != 0 {
		t.Errorf("the check must be off unless asked for, got %v", findings)
	}

	findings := Checks{HeaderSchema: true}.run(headerSchemaExpectation(), violating, untimed)
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

	findings := Checks{HeaderSchema: true}.run(headerSchemaExpectation(), wrongStatus, untimed)
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

// TestADocumentedServerErrorIsNotAFinding pins that a 5xx the description
// promises is conformance rather than a fault.
//
// The check fired on any 5xx whatever was expected, so an API publishing an
// error contract — a documented 500 for an internal failure — was reported for
// obeying it. That is the check reporting the description for existing.
func TestADocumentedServerErrorIsNotAFinding(t *testing.T) {
	documented := Checks{ServerError: true}.run(
		validate.Message{StatusCode: "500"},
		validate.Message{StatusCode: "500"},
		untimed,
	)
	if len(documented) != 0 {
		t.Errorf("a documented 500 should not be a finding, got %v", documented)
	}

	// An undocumented one still is: there the server failed where something
	// else was promised, which is the whole point of the check.
	unexpected := Checks{ServerError: true}.run(
		validate.Message{StatusCode: "200"},
		validate.Message{StatusCode: "500"},
		untimed,
	)
	if len(unexpected) != 1 {
		t.Errorf("an undocumented 500 should be reported, got %v", unexpected)
	}

	// And a DIFFERENT 5xx from the one promised is still a finding: a
	// description documenting a 503 says nothing about a 500.
	other := Checks{ServerError: true}.run(
		validate.Message{StatusCode: "503"},
		validate.Message{StatusCode: "500"},
		untimed,
	)
	if len(other) != 1 {
		t.Errorf("a 500 where a 503 was promised should be reported, got %v", other)
	}
}

// TestABodilessResponseIsNotCheckedForABody pins that the protocol wins over
// the description.
//
// A HEAD response never carries a body — that is the method's definition — and
// a description giving one a schema is describing what a GET to the same
// resource returns and what headers HEAD will send. Checking a body against it
// reported "the response declares JSON but the body does not parse" for every
// HEAD endpoint in existence, blaming a server for obeying the protocol. RFC
// 9110 says the same of 204 and 304.
func TestABodilessResponseIsNotCheckedForABody(t *testing.T) {
	for _, test := range []struct {
		method string
		status string
		want   bool
	}{
		{"HEAD", "200", true},
		{"head", "200", true},
		{"GET", "204", true},
		{"GET", "304", true},
		{"GET", "200", false},
		{"POST", "201", false},
		// A 205 does forbid content, but only in the sense that the server
		// should send none; it is not in RFC 9110's list of responses that
		// cannot have one, so it is left alone rather than guessed at.
		{"GET", "205", false},
	} {
		if got := bodiless(test.method, test.status); got != test.want {
			t.Errorf("bodiless(%q, %q) = %v, want %v", test.method, test.status, got, test.want)
		}
	}
}

// TestTheResponseTimeBoundIsOffUntilItIsGiven pins that an unconfigured run
// never reports on time at all.
//
// Every other check here judges the response against the description, so it can
// be on by default and still only report what the document already promised. A
// response time has no such reference: OpenAPI cannot state one, so any bound a
// default applied would be a number vertrag made up on the operator's behalf —
// and a suite that goes red over an invented threshold teaches people to
// distrust the tool rather than read the finding.
func TestTheResponseTimeBoundIsOffUntilItIsGiven(t *testing.T) {
	slow := validate.Message{StatusCode: "200"}

	if findings := (Checks{}).run(slow, slow, time.Hour); len(findings) != 0 {
		t.Errorf("an hour must be unremarkable with no bound set, got %v", findings)
	}

	findings := Checks{MaxResponseTime: 750 * time.Millisecond}.run(slow, slow, time.Hour)
	if len(findings) != 1 {
		t.Errorf("once a bound is set the hour should be reported, got %v", findings)
	}
}

// TestTheResponseTimeFindingNamesTheBoundAndWhatItTook covers the arithmetic and
// the wording together, because both are how a reader decides whether the
// finding is about their server or about their configuration.
func TestTheResponseTimeFindingNamesTheBoundAndWhatItTook(t *testing.T) {
	for _, test := range []struct {
		name    string
		bound   time.Duration
		elapsed time.Duration
		want    string
	}{
		{
			name:    "over the bound",
			bound:   750 * time.Millisecond,
			elapsed: 900 * time.Millisecond,
			want:    "the response took 900ms, longer than the 750ms this run allows",
		},
		{
			// A bound is a maximum, so meeting it exactly is meeting it. The
			// alternative reading turns `750ms` into "strictly under 750ms",
			// which is not what anybody writing an SLA means by it.
			name:    "exactly on the bound",
			bound:   750 * time.Millisecond,
			elapsed: 750 * time.Millisecond,
			want:    "",
		},
		{
			// The microseconds differ on every run and no server can be tuned
			// by them, so they are rounded away rather than printed as
			// precision the finding does not have.
			name:    "microseconds are not reported",
			bound:   750 * time.Millisecond,
			elapsed: 751348219 * time.Nanosecond,
			want:    "the response took 751ms, longer than the 750ms this run allows",
		},
		{
			// Under a bound finer than a millisecond that rounding would print
			// "took 0s", which reads as the checker having fired on nothing.
			name:    "a bound finer than the rounding",
			bound:   500 * time.Microsecond,
			elapsed: 900 * time.Microsecond,
			want:    "the response took 900µs, longer than the 500µs this run allows",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			finding, found := checkResponseTime(test.bound, test.elapsed)
			if found != (test.want != "") {
				t.Fatalf("found = %v, want %v (finding %q)", found, test.want != "", finding)
			}
			if finding != test.want {
				t.Errorf("finding = %q, want %q", finding, test.want)
			}
		})
	}
}

// TestASlowResponseIsReportedWithoutFailingTheContract runs the check where it
// actually has to work: over a live server, through the runner, with the
// duration the runner measured rather than one a test handed it.
//
// The duration reaching the check is the part a refactor breaks silently. It is
// not carried in the message pair the other checks read — it is measured by the
// runner and passed alongside — so nothing but a real transaction proves the
// two are still connected, and a bound that quietly stopped being applied looks
// exactly like a server that got faster.
//
// The other half is what the finding is filed as. Nothing the description
// promised was contradicted here: the status, the headers and the body are all
// what it said they would be, and Errors must stay empty to say so. A reader
// sent to the document by this finding would find nothing in it about time.
func TestASlowResponseIsReportedWithoutFailingTheContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Comfortably longer than the bound below, so the test is deciding
		// whether the check works rather than how loaded the machine is.
		time.Sleep(25 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	source := compile.Transaction{
		Name:    "slow",
		Request: compile.Request{Method: "GET", URI: "/slow"},
		Response: compile.Response{
			Status:  "200",
			Headers: []compile.Header{{Name: "Content-Type", Value: "application/json"}},
			Body:    `{"ok":true}`,
		},
	}

	engine := New(server.URL)
	engine.Checks = Checks{MaxResponseTime: time.Millisecond}

	results, err := engine.Run(context.Background(), []compile.Transaction{source})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results[0].Beyond) != 1 || !strings.Contains(results[0].Beyond[0], "longer than the 1ms") {
		t.Errorf("findings = %v, want the bound named", results[0].Beyond)
	}
	if len(results[0].Errors) != 0 {
		t.Errorf("errors = %v, want none: the description was not contradicted", results[0].Errors)
	}
	// It does still fail the transaction, as every finding in Beyond does. The
	// CLI reporter prints Beyond for a failure only, so a finding that left the
	// run green would be one nobody ever reads — and a bound is only ever set
	// by somebody who wanted to be told.
	if results[0].Status != StatusFail {
		t.Errorf("status = %s, want fail", results[0].Status)
	}

	// The same server under a bound it meets is green, which is what makes the
	// check safe to leave configured.
	engine.Checks = Checks{MaxResponseTime: time.Minute}
	results, err = engine.Run(context.Background(), []compile.Transaction{source})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if results[0].Status != StatusPass || len(results[0].Beyond) != 0 {
		t.Errorf("status = %s, findings = %v, want a clean pass", results[0].Status, results[0].Beyond)
	}
}

// TestAPacedRunIsNotJudgedSlowForItsOwnPause is the distinction the bound has
// to make, and the bug it was written for.
//
// `transport.delay` exists so a run can spare a server that throttles. The
// bound was measured over the whole transaction, so that pause was spent
// against it: a suite with `delay: 500ms` and `max-response-time: 750ms`
// reported the server as slow when the server had answered instantly, and the
// only thing slow about the run was the courtesy it had been configured to
// extend. The two settings could not be used together at all.
//
// The server here answers at once and the pause is four times the bound, so
// there is nothing for the check to find unless it is timing the run's own
// waiting.
func TestAPacedRunIsNotJudgedSlowForItsOwnPause(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sources := []compile.Transaction{
		{Name: "first", Request: compile.Request{Method: "GET", URI: "/one"},
			Response: compile.Response{Status: "200"}},
		{Name: "second", Request: compile.Request{Method: "GET", URI: "/two"},
			Response: compile.Response{Status: "200"}},
	}

	engine, err := NewWithTransport(server.URL, Transport{Delay: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewWithTransport: %v", err)
	}
	engine.Checks = Checks{MaxResponseTime: 50 * time.Millisecond}

	results, err := engine.Run(context.Background(), sources)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The second transaction is the one that waited, and it has to be over the
	// bound as a whole for this test to be testing anything: on a build where
	// pacing had quietly stopped working every transaction would be under the
	// bound and the run would pass for the wrong reason.
	if results[1].Duration <= 50*time.Millisecond {
		t.Fatalf("the second transaction took %s, which is inside the bound; the run never paced itself",
			results[1].Duration)
	}

	for i, result := range results {
		if result.Status != StatusPass || len(result.Beyond) != 0 {
			t.Errorf("transaction %d: status = %s, findings = %v; the server answered at once "+
				"and the pause is the run's own", i, result.Status, result.Beyond)
		}
		// Both measurements survive, and they say different things. Duration
		// still means what every reporter prints it as — how long this
		// transaction cost the run — and the bound is judged on the other one.
		if result.ResponseTime > 50*time.Millisecond {
			t.Errorf("transaction %d: the server was timed at %s against an instant answer",
				i, result.ResponseTime)
		}
		if result.ResponseTime <= 0 {
			t.Errorf("transaction %d: the exchange was not timed at all", i)
		}
		if result.ResponseTime > result.Duration {
			t.Errorf("transaction %d: the exchange (%s) cannot outlast the transaction (%s)",
				i, result.ResponseTime, result.Duration)
		}
	}
}
