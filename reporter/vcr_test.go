package reporter

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// TestAVCRCassetteCarriesEnoughToReplayTheExchange pins the shape a replay
// library reads. Each value here is one it looks for by name, and a cassette
// missing any of them plays back as a request that matches nothing.
func TestAVCRCassetteCarriesEnoughToReplayTheExchange(t *testing.T) {
	cassette := cassetteOf(t, []runner.Result{oneExchange()})

	if len(cassette.HTTPInteractions) != 1 {
		t.Fatalf("http_interactions = %d, want 1", len(cassette.HTTPInteractions))
	}
	interaction := cassette.HTTPInteractions[0]

	if interaction.Request.URI != "http://localhost:4000/things?limit=2" {
		t.Errorf("request.uri = %q", interaction.Request.URI)
	}
	if interaction.Request.Body.String != `{"sent":true}` {
		t.Errorf("request.body.string = %q", interaction.Request.Body.String)
	}
	if interaction.Request.Body.Encoding != "UTF-8" {
		t.Errorf("request.body.encoding = %q", interaction.Request.Body.Encoding)
	}
	if got := interaction.Request.Headers["Content-Type"]; len(got) != 1 || got[0] != "application/json" {
		t.Errorf("request.headers[Content-Type] = %v, want a one-element list", got)
	}
	if interaction.Response.Status.Code != 200 {
		t.Errorf("response.status.code = %d, want a number", interaction.Response.Status.Code)
	}
	if interaction.Response.Status.Message != "OK" {
		t.Errorf("response.status.message = %q", interaction.Response.Status.Message)
	}
	if interaction.Response.Body.String != `{"things":[]}` {
		t.Errorf("response.body.string = %q", interaction.Response.Body.String)
	}
	if got := interaction.Response.Headers["Content-Type"]; len(got) != 1 || got[0] != "application/json" {
		t.Errorf("response.headers[Content-Type] = %v", got)
	}
	if interaction.RecordedAt != "Tue, 18 Aug 2026 09:30:15 GMT" {
		t.Errorf("recorded_at = %q, want an HTTP-date", interaction.RecordedAt)
	}
	if interaction.Name != "GET /things > 200" {
		t.Errorf("name = %q, want the transaction the interaction belongs to", interaction.Name)
	}
	if interaction.Status != "pass" {
		t.Errorf("status = %q", interaction.Status)
	}
	if cassette.RecordedWith != "vertrag" {
		t.Errorf("recorded_with = %q, want no trailing version for a run that has none",
			cassette.RecordedWith)
	}
	if cassette.RecordedFrom != "" {
		t.Errorf("recorded_from = %q, want nothing for a run that knows nothing about itself",
			cassette.RecordedFrom)
	}
}

// TestAVCRCassetteSpellsTheMethodAsTheWireDoes pins uppercase. The reference
// implementation writes `get` because Ruby holds the method as a symbol, and a
// library matching this cassette against a live request compares against the
// uppercase token HTTP defines — a lowercased method matches nothing.
func TestAVCRCassetteSpellsTheMethodAsTheWireDoes(t *testing.T) {
	cassette := cassetteOf(t, []runner.Result{oneExchange()})
	if got := cassette.HTTPInteractions[0].Request.Method; got != "GET" {
		t.Errorf("request.method = %q, want GET", got)
	}
}

// TestAVCRCassetteRedactsEveryCredentialHeader asserts on the parsed cassette,
// so it checks the values a replay library will read rather than merely that a
// string is missing somewhere. Both directions matter: the request carries the
// credential the run was given, the response the one the server issued.
func TestAVCRCassetteRedactsEveryCredentialHeader(t *testing.T) {
	SetSanitize(true)
	t.Cleanup(func() { SetSanitize(true) })

	cassette := cassetteOf(t, []runner.Result{oneExchange()})
	interaction := cassette.HTTPInteractions[0]

	for _, name := range []string{"Authorization", "X-Api-Key"} {
		got := interaction.Request.Headers[name]
		if len(got) == 0 {
			t.Errorf("%s vanished from the cassette rather than being redacted", name)
			continue
		}
		if got[0] != Redacted {
			t.Errorf("request.headers[%s] = %q, want %q", name, got[0], Redacted)
		}
	}
	if got := interaction.Response.Headers["Set-Cookie"]; len(got) != 1 || got[0] != Redacted {
		t.Errorf("response.headers[Set-Cookie] = %v, want %q", got, Redacted)
	}

	var out bytes.Buffer
	VCR{Out: &out, Now: fixedClock()}.Report([]runner.Result{oneExchange()})
	for _, secret := range []string{"sk-live-supersecret", "ak_9999", "session=abcdef"} {
		if strings.Contains(out.String(), secret) {
			t.Errorf("the cassette leaked %q:\n%s", secret, out.String())
		}
	}
}

// TestAVCRCassetteKeepsABinaryBodyIntact pins the one place a cassette beats a
// HAR file. yaml.v3 writes a string it cannot represent as a !!binary scalar,
// which round-trips the bytes exactly, so a recorded download survives here even
// though JSON forces the HAR file to base64 it. Pinned because it is a property
// of yaml.v3 rather than of any code here, and a hand-rolled writer put in its
// place would lose it silently.
func TestAVCRCassetteKeepsABinaryBodyIntact(t *testing.T) {
	binary := "\x00\x01\xff\xfe\x80payload"

	cassette := cassetteOf(t, []runner.Result{{
		Name: "GET /download > 200", Status: runner.StatusPass,
		Request: runner.Request{
			Method: "POST", URL: "http://localhost:4000/upload", Body: binary,
		},
		Actual:  validate.Message{StatusCode: "200", Body: binary},
		Started: recordedAt,
	}})
	interaction := cassette.HTTPInteractions[0]

	if interaction.Response.Body.String != binary {
		t.Errorf("the response body did not survive: %q", interaction.Response.Body.String)
	}
	if interaction.Request.Body.String != binary {
		t.Errorf("the request body did not survive: %q", interaction.Request.Body.String)
	}
	// Calling it UTF-8 would be contradicted by the !!binary tag on the line
	// below it. ASCII-8BIT is the format's word for bytes with no encoding.
	if interaction.Response.Body.Encoding != "ASCII-8BIT" {
		t.Errorf("response.body.encoding = %q, want ASCII-8BIT", interaction.Response.Body.Encoding)
	}
	if interaction.Request.Body.Encoding != "ASCII-8BIT" {
		t.Errorf("request.body.encoding = %q, want ASCII-8BIT", interaction.Request.Body.Encoding)
	}
}

// TestAVCRCassetteSaysWhenNoAnswerCameBack pins code 0 with the reason in the
// message. It is the only slot the format keeps for this, and a replay library
// handed the interaction answers 0 — the truth about what came back, rather than
// a fabricated success a suite would go on to trust.
func TestAVCRCassetteSaysWhenNoAnswerCameBack(t *testing.T) {
	cassette := cassetteOf(t, []runner.Result{{
		Name: "DELETE /things/1 > 204", Status: runner.StatusError,
		Request: runner.Request{Method: "DELETE", URL: "http://localhost:4000/things/1"},
		Errors:  []string{"dial tcp 127.0.0.1:4000: connection refused"},
		Started: recordedAt,
	}})

	if len(cassette.HTTPInteractions) != 1 {
		t.Fatalf("interactions = %d, want the request that was attempted", len(cassette.HTTPInteractions))
	}
	status := cassette.HTTPInteractions[0].Response.Status
	if status.Code != 0 {
		t.Errorf("response.status.code = %d, want 0", status.Code)
	}
	if !strings.Contains(status.Message, "connection refused") {
		t.Errorf("response.status.message = %q, want what happened instead", status.Message)
	}
	if cassette.HTTPInteractions[0].Status != "error" {
		t.Errorf("status = %q, want error", cassette.HTTPInteractions[0].Status)
	}
}

// TestAVCRCassetteSurvivesAPayloadThatLooksLikeYAML pins that a body cannot
// restructure the document that carries it. A response of `key: value` written
// unquoted would be read back as a mapping, and a recording an API can rewrite by
// answering is not a recording.
func TestAVCRCassetteSurvivesAPayloadThatLooksLikeYAML(t *testing.T) {
	payload := "http_interactions:\n- request:\n    method: INJECTED\n"

	var out bytes.Buffer
	VCR{Out: &out, Now: fixedClock()}.Report([]runner.Result{{
		Name: "GET /yaml > 200", Status: runner.StatusPass,
		Request: runner.Request{Method: "GET", URL: "http://localhost:4000/yaml"},
		Actual:  validate.Message{StatusCode: "200", Body: payload},
		Started: recordedAt,
	}})

	var parsed vcrCassette
	if err := yaml.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("a payload broke the cassette: %v\n%s", err, out.String())
	}
	if len(parsed.HTTPInteractions) != 1 {
		t.Fatalf("interactions = %d: the document was restructured by its own content",
			len(parsed.HTTPInteractions))
	}
	if parsed.HTTPInteractions[0].Request.Method != "GET" {
		t.Errorf("request.method = %q: a body rewrote the request that carried it",
			parsed.HTTPInteractions[0].Request.Method)
	}
	if parsed.HTTPInteractions[0].Response.Body.String != payload {
		t.Errorf("the body did not round-trip: %q", parsed.HTTPInteractions[0].Response.Body.String)
	}
}

// TestAVCRCassetteSaysWhichRunProducedIt pins the header, for the reason the
// JUnit properties carry the same values: a cassette committed to a repository
// outlives every terminal that could have said which endpoint it was taken
// against, and answers with no question attached cannot be checked.
func TestAVCRCassetteSaysWhichRunProducedIt(t *testing.T) {
	var out bytes.Buffer
	VCR{Out: &out, Now: fixedClock(), Run: Provenance{
		Version:  "0.4.0",
		Spec:     "./openapi.json",
		Endpoint: "http://localhost:4000",
		Config:   "vertrag.yml",
	}}.Report([]runner.Result{oneExchange()})

	var parsed vcrCassette
	if err := yaml.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("the cassette does not parse: %v\n%s", err, out.String())
	}
	if parsed.RecordedWith != "vertrag 0.4.0" {
		t.Errorf("recorded_with = %q", parsed.RecordedWith)
	}
	for _, want := range []string{"./openapi.json", "http://localhost:4000", "vertrag.yml"} {
		if !strings.Contains(parsed.RecordedFrom, want) {
			t.Errorf("recorded_from = %q, want it to name %q", parsed.RecordedFrom, want)
		}
	}
}

// TestAVCRCassetteWritesHeaderNamesInAFixedOrder pins the determinism the
// formats depend on. The headers are a map, and yaml.v3 sorting its keys is what
// keeps two cassettes of the same run byte-identical — if that ever stops being
// true, this fails here rather than in a pipeline diffing yesterday's recording.
func TestAVCRCassetteWritesHeaderNamesInAFixedOrder(t *testing.T) {
	var out bytes.Buffer
	VCR{Out: &out, Now: fixedClock()}.Report([]runner.Result{oneExchange()})
	text := out.String()

	order := []string{"Authorization:", "Content-Type:", "X-Api-Key:"}
	at := -1
	for _, name := range order {
		found := strings.Index(text, name)
		if found < 0 {
			t.Fatalf("header %s is missing:\n%s", name, text)
		}
		if found < at {
			t.Errorf("header names are not in sorted order:\n%s", text)
		}
		at = found
	}
}
