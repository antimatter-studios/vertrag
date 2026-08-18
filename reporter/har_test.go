package reporter

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// TestAHARCarriesEnoughToReplayTheRequest pins the whole point of the format.
// Each of these is a value a reader needs to resend the request or to see what
// came back, and a recording missing any one of them sends them back to rerunning
// the suite — which is what they could not do in the first place.
func TestAHARCarriesEnoughToReplayTheRequest(t *testing.T) {
	archive := harOf(t, []runner.Result{oneExchange()})

	if archive.Log.Version != "1.2" {
		t.Errorf("log.version = %q, want the version consumers check", archive.Log.Version)
	}
	if archive.Log.Creator.Name != "vertrag" {
		t.Errorf("creator.name = %q", archive.Log.Creator.Name)
	}
	if len(archive.Log.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(archive.Log.Entries))
	}
	entry := archive.Log.Entries[0]

	if entry.Request.Method != "GET" {
		t.Errorf("request.method = %q", entry.Request.Method)
	}
	if entry.Request.URL != "http://localhost:4000/things?limit=2" {
		t.Errorf("request.url = %q", entry.Request.URL)
	}
	if entry.Request.PostData == nil {
		t.Fatal("a request with a body should record postData")
	}
	if entry.Request.PostData.Text != `{"sent":true}` {
		t.Errorf("postData.text = %q", entry.Request.PostData.Text)
	}
	if entry.Request.PostData.MimeType != "application/json" {
		t.Errorf("postData.mimeType = %q, want the Content-Type that was sent",
			entry.Request.PostData.MimeType)
	}
	if entry.Response.Status != 200 {
		t.Errorf("response.status = %d, want a number rather than a string", entry.Response.Status)
	}
	if entry.Response.StatusText != "OK" {
		t.Errorf("response.statusText = %q", entry.Response.StatusText)
	}
	if entry.Response.Content.Text != `{"things":[]}` {
		t.Errorf("content.text = %q", entry.Response.Content.Text)
	}
	if entry.Response.Content.MimeType != "application/json" {
		t.Errorf("content.mimeType = %q", entry.Response.Content.MimeType)
	}
	if entry.Response.Content.Size != len(`{"things":[]}`) {
		t.Errorf("content.size = %d, want %d", entry.Response.Content.Size, len(`{"things":[]}`))
	}
	if got := headerValue(entry.Request.Headers, "Content-Type"); got != "application/json" {
		t.Errorf("a harmless request header was lost: %v", entry.Request.Headers)
	}
	if got := headerValue(entry.Response.Headers, "Content-Type"); got != "application/json" {
		t.Errorf("a harmless response header was lost: %v", entry.Response.Headers)
	}
	if got := headerValue(entry.Request.QueryString, "limit"); got != "2" {
		t.Errorf("queryString = %v, want the parameter that was sent", entry.Request.QueryString)
	}
	if entry.Comment == "" || !strings.Contains(entry.Comment, "GET /things > 200") {
		t.Errorf("entry.comment = %q, want the transaction it belongs to", entry.Comment)
	}
}

// TestAHARRedactsEveryCredentialHeader asserts on the parsed archive rather than
// on the text, so it is checking the values a consumer will read and not merely
// that a string is missing somewhere in the file. Both directions matter: the
// request carries the credential the run was given, and the response carries the
// one the server issued.
func TestAHARRedactsEveryCredentialHeader(t *testing.T) {
	SetSanitize(true)
	t.Cleanup(func() { SetSanitize(true) })

	archive := harOf(t, []runner.Result{oneExchange()})
	entry := archive.Log.Entries[0]

	for _, name := range []string{"Authorization", "X-Api-Key"} {
		got := headerValue(entry.Request.Headers, name)
		if got == "" {
			t.Errorf("%s vanished from the recording rather than being redacted", name)
		}
		if got != Redacted {
			t.Errorf("request header %s = %q, want %q", name, got, Redacted)
		}
	}
	if got := headerValue(entry.Response.Headers, "Set-Cookie"); got != Redacted {
		t.Errorf("response header Set-Cookie = %q, want %q", got, Redacted)
	}

	// The values themselves must be nowhere in the file, not merely absent from
	// the header arrays — a query string or a comment would carry them just as
	// far, and a cassette is read by grep as often as by a tool.
	var out bytes.Buffer
	HAR{Out: &out, Now: fixedClock()}.Report([]runner.Result{oneExchange()})
	for _, secret := range []string{"sk-live-supersecret", "ak_9999", "session=abcdef"} {
		if strings.Contains(out.String(), secret) {
			t.Errorf("the archive leaked %q:\n%s", secret, out.String())
		}
	}
}

// TestAHARRecordsNoCookiesForARedactedCookieHeader pins the near-bug that made
// the cookies arrays empty by decision rather than by omission. The format has a
// parsed cookies array beside the raw headers, so filling it from a Cookie or
// Set-Cookie header would put the credential back into the file through a second
// door — and the reader, seeing <redacted> in the headers, would never look.
func TestAHARRecordsNoCookiesForARedactedCookieHeader(t *testing.T) {
	SetSanitize(true)
	t.Cleanup(func() { SetSanitize(true) })

	result := oneExchange()
	result.Request.Headers["Cookie"] = "jwt=alsosecret"
	archive := harOf(t, []runner.Result{result})
	entry := archive.Log.Entries[0]

	if len(entry.Request.Cookies) != 0 {
		t.Errorf("request.cookies = %v, want none", entry.Request.Cookies)
	}
	if len(entry.Response.Cookies) != 0 {
		t.Errorf("response.cookies = %v, want none", entry.Response.Cookies)
	}

	// Present and empty rather than absent: the field is required, and a
	// consumer reading null where an array is promised fails at import.
	var out bytes.Buffer
	HAR{Out: &out, Now: fixedClock()}.Report([]runner.Result{result})
	if strings.Contains(out.String(), "null") {
		t.Errorf("a required array was written as null:\n%s", out.String())
	}
	if strings.Contains(out.String(), "alsosecret") {
		t.Errorf("the archive leaked a cookie:\n%s", out.String())
	}
}

// TestAHARRecordsARequestThatGotNoAnswer pins that the entry survives. A request
// that provoked a connection reset is the one a reader most wants to resend, and
// dropping it would leave the recording claiming the run never made it — while
// inventing a 200 for it would be worse still.
func TestAHARRecordsARequestThatGotNoAnswer(t *testing.T) {
	archive := harOf(t, []runner.Result{{
		Name: "DELETE /things/1 > 204", Status: runner.StatusError,
		Request: runner.Request{Method: "DELETE", URL: "http://localhost:4000/things/1"},
		Errors:  []string{"dial tcp 127.0.0.1:4000: connection refused"},
		Started: recordedAt,
	}})

	if len(archive.Log.Entries) != 1 {
		t.Fatalf("entries = %d, want the request that was attempted", len(archive.Log.Entries))
	}
	entry := archive.Log.Entries[0]
	if entry.Response.Status != 0 {
		t.Errorf("response.status = %d, want 0 for a request that got no answer", entry.Response.Status)
	}
	if !strings.Contains(entry.Response.Error, "connection refused") {
		t.Errorf("_error = %q, want what happened instead of a response", entry.Response.Error)
	}
	if entry.Response.BodySize != -1 {
		t.Errorf("bodySize = %d, want -1 for a body that never arrived", entry.Response.BodySize)
	}
}

// TestAHARBase64sABodyThatIsNotText pins the substitution encoding/json would
// otherwise make silently. It replaces every byte it cannot represent with
// U+FFFD, so a recorded binary download or a fuzzed payload would come back as a
// run of replacement characters — unreplayable, with nothing in the file
// admitting it happened.
func TestAHARBase64sABodyThatIsNotText(t *testing.T) {
	binary := "\x00\x01\xff\xfe\x80payload"

	archive := harOf(t, []runner.Result{{
		Name: "GET /download > 200", Status: runner.StatusPass,
		Request: runner.Request{
			Method: "POST", URL: "http://localhost:4000/upload", Body: binary,
		},
		Actual:  validate.Message{StatusCode: "200", Body: binary},
		Started: recordedAt,
	}})
	entry := archive.Log.Entries[0]

	if entry.Response.Content.Encoding != "base64" {
		t.Fatalf("content.encoding = %q, want base64", entry.Response.Content.Encoding)
	}
	decoded, err := base64.StdEncoding.DecodeString(entry.Response.Content.Text)
	if err != nil {
		t.Fatalf("content.text is not base64: %v", err)
	}
	if string(decoded) != binary {
		t.Errorf("the response body did not survive: %q", decoded)
	}

	if entry.Request.PostData.Encoding != "base64" {
		t.Fatalf("postData._encoding = %q, want base64", entry.Request.PostData.Encoding)
	}
	decoded, err = base64.StdEncoding.DecodeString(entry.Request.PostData.Text)
	if err != nil {
		t.Fatalf("postData.text is not base64: %v", err)
	}
	if string(decoded) != binary {
		t.Errorf("the request body did not survive: %q", decoded)
	}
}

// TestAHARLeavesMarkupInABodyReadable pins that a recorded HTML or XML payload
// is not a wall of \u003c escapes. Both spellings are legal JSON and no consumer
// minds either, but half the value of a recording is that a person can read the
// payload in it.
func TestAHARLeavesMarkupInABodyReadable(t *testing.T) {
	var out bytes.Buffer
	HAR{Out: &out, Now: fixedClock()}.Report([]runner.Result{{
		Name: "GET /page > 200", Status: runner.StatusPass,
		Request: runner.Request{Method: "GET", URL: "http://localhost:4000/page"},
		Actual:  validate.Message{StatusCode: "200", Body: "<html><body>hello & goodbye</body></html>"},
		Started: recordedAt,
	}})

	if strings.Contains(out.String(), `\u003c`) {
		t.Errorf("markup was escaped rather than written as itself:\n%s", out.String())
	}

	// Readable is not an excuse for invalid: it still has to parse.
	var parsed harFile
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("the archive does not parse: %v\n%s", err, out.String())
	}
	if parsed.Log.Entries[0].Response.Content.Text != "<html><body>hello & goodbye</body></html>" {
		t.Errorf("the body did not round-trip: %q", parsed.Log.Entries[0].Response.Content.Text)
	}
}

// TestAHARStatesWhatItDidNotMeasure pins the -1s. The runner times a request as
// a whole and does not instrument the connection, so writing a zero for the send
// phase would claim a measurement nobody made — and the format defines the total
// as the sum of the phases that are not -1, which a fabricated zero would break.
func TestAHARStatesWhatItDidNotMeasure(t *testing.T) {
	entry := harOf(t, []runner.Result{oneExchange()}).Log.Entries[0]

	if entry.Timings.Send != -1 || entry.Timings.Receive != -1 {
		t.Errorf("timings = %+v, want -1 for the phases that were not measured", entry.Timings)
	}
	if entry.Timings.Wait != entry.Time {
		t.Errorf("time %v should be the sum of the measured phases, got wait %v",
			entry.Time, entry.Timings.Wait)
	}
	if entry.Time != 12.34 {
		t.Errorf("time = %v, want the duration in milliseconds", entry.Time)
	}
	if entry.Request.HeadersSize != -1 || entry.Response.HeadersSize != -1 {
		t.Error("headersSize should be -1: the raw header block was never held as bytes")
	}
}

// TestAHARStampsWhenEachRequestWentOut pins the timestamps a viewer draws its
// waterfall from. Every entry sharing one instant renders as a single stacked
// bar, which is a picture of nothing — the reason the runner records a start time
// per transaction at all.
func TestAHARStampsWhenEachRequestWentOut(t *testing.T) {
	first := oneExchange()
	second := oneExchange()
	second.Name = "GET /things > 200 again"
	second.Started = recordedAt.Add(750 * time.Millisecond)

	archive := harOf(t, []runner.Result{first, second})

	if got := archive.Log.Entries[0].StartedDateTime; got != "2026-08-18T09:30:15.500Z" {
		t.Errorf("entry 0 startedDateTime = %q", got)
	}
	if got := archive.Log.Entries[1].StartedDateTime; got != "2026-08-18T09:30:16.250Z" {
		t.Errorf("entry 1 startedDateTime = %q, want its own time rather than the first's", got)
	}
}

// TestAHAROfARunThatSentNothingIsStillAnArchive pins that entries is an empty
// array and not null. A run every transaction of which was skipped is a real run,
// and a consumer reading null where the format promises an array fails at import
// with nothing useful said about why.
func TestAHAROfARunThatSentNothingIsStillAnArchive(t *testing.T) {
	var out bytes.Buffer
	HAR{Out: &out, Now: fixedClock()}.Report([]runner.Result{{
		Name: "GET /skipped > 200", Status: runner.StatusSkip,
	}})

	var parsed harFile
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("the archive does not parse: %v\n%s", err, out.String())
	}
	if parsed.Log.Entries == nil {
		t.Errorf("entries came back null rather than empty:\n%s", out.String())
	}
	if !strings.Contains(out.String(), `"entries": []`) {
		t.Errorf("entries should be written as an empty array:\n%s", out.String())
	}
}

// TestAHARSaysWhichRunProducedIt pins the provenance, for the reason the JUnit
// properties carry it: the stderr signature line is gone by the time anybody
// opens an archived recording, and a set of answers with no named endpoint
// attached cannot be checked against anything.
func TestAHARSaysWhichRunProducedIt(t *testing.T) {
	var out bytes.Buffer
	HAR{Out: &out, Now: fixedClock(), Run: Provenance{
		Version:  "0.4.0",
		Spec:     "./openapi.json",
		Endpoint: "http://localhost:4000",
		Config:   "vertrag.yml",
	}}.Report([]runner.Result{oneExchange()})

	var parsed harFile
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("the archive does not parse: %v\n%s", err, out.String())
	}
	if parsed.Log.Creator.Version != "0.4.0" {
		t.Errorf("creator.version = %q", parsed.Log.Creator.Version)
	}
	for _, want := range []string{"./openapi.json", "http://localhost:4000", "vertrag.yml"} {
		if !strings.Contains(parsed.Log.Comment, want) {
			t.Errorf("log.comment = %q, want it to name %q", parsed.Log.Comment, want)
		}
	}
	// The version belongs to the creator block, and saying it twice in one file
	// is how two copies of a value start disagreeing.
	if strings.Contains(parsed.Log.Comment, "0.4.0") {
		t.Errorf("log.comment repeats the version the creator already states: %q", parsed.Log.Comment)
	}
}

// TestAHAROfARunThatKnowsNothingAboutItselfCarriesNoComment pins that an unknown
// provenance produces no comment rather than the word "vertrag" on its own, the
// way the JUnit report writes no <properties> element rather than an empty one.
func TestAHAROfARunThatKnowsNothingAboutItselfCarriesNoComment(t *testing.T) {
	var out bytes.Buffer
	HAR{Out: &out, Now: fixedClock()}.Report([]runner.Result{oneExchange()})

	if strings.Contains(out.String(), `"comment": "vertrag"`) {
		t.Errorf("a run with no provenance should carry no log comment:\n%s", out.String())
	}
}

// headerValue finds a header in a recorded array, case-insensitively, so the
// assertions read like the question they are asking.
func headerValue(headers []harHeader, name string) string {
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			return header.Value
		}
	}
	return ""
}
