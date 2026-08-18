package hooks

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

func prepared(t *testing.T) *runner.Transaction {
	t.Helper()

	source := compile.Transaction{
		Name: "API > /things > List > 200 > application/json",
		Request: compile.Request{
			Method:  "GET",
			URI:     "/things",
			Headers: []compile.Header{{Name: "Accept", Value: "application/json"}},
		},
		Response: compile.Response{
			Status:  "200",
			Headers: []compile.Header{{Name: "Content-Type", Value: "application/json"}},
			Body:    `{"id":"x"}`,
			Schema:  `{"type":"object"}`,
		},
	}

	var transactions []*runner.Transaction
	engine := runner.New("http://localhost:4000")
	engine.Hooks = capture{&transactions}
	engine.Run(t.Context(), []compile.Transaction{source})

	if len(transactions) == 0 {
		t.Fatal("no transaction was prepared")
	}
	return transactions[0]
}

// capture grabs the prepared transactions without needing a server.
type capture struct{ into *[]*runner.Transaction }

func (c capture) BeforeAll(t []*runner.Transaction) error { *c.into = t; return nil }
func (c capture) AfterAll([]*runner.Transaction) error    { return nil }
func (c capture) BeforeEach(*runner.Transaction) error    { return nil }
func (c capture) BeforeEachValidation(*runner.Transaction) error {
	return nil
}
func (c capture) AfterEach(*runner.Transaction) error { return nil }

// TestWireShapeMatchesDredd pins the field names hook files are written
// against. A hook reaching for transaction.expected.statusCode must find it.
func TestWireShapeMatchesDredd(t *testing.T) {
	encoded, err := json.Marshal(toWire(prepared(t)))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, key := range []string{"name", "id", "host", "port", "protocol", "fullPath", "request", "expected", "real", "skip", "fail"} {
		if _, present := decoded[key]; !present {
			t.Errorf("wire shape is missing %q, which hook files read", key)
		}
	}

	request := decoded["request"].(map[string]any)
	for _, key := range []string{"method", "uri", "headers", "body"} {
		if _, present := request[key]; !present {
			t.Errorf("request is missing %q", key)
		}
	}

	expected := decoded["expected"].(map[string]any)
	if expected["statusCode"] != "200" {
		t.Errorf("expected.statusCode = %v, want 200", expected["statusCode"])
	}

	// `fail` is false rather than absent, because hook files test it directly.
	if decoded["fail"] != false {
		t.Errorf("fail = %v, want false", decoded["fail"])
	}
}

func TestEndpointIsSplitForHooks(t *testing.T) {
	for _, test := range []struct {
		endpoint string
		host     string
		port     string
		protocol string
	}{
		{"http://localhost:4000", "localhost", "4000", "http:"},
		{"https://api.example.com", "api.example.com", "443", "https:"},
		{"http://api.example.com", "api.example.com", "80", "http:"},
	} {
		host, port, protocol := splitEndpoint(test.endpoint)
		if host != test.host || port != test.port || protocol != test.protocol {
			t.Errorf("splitEndpoint(%q) = %q %q %q, want %q %q %q",
				test.endpoint, host, port, protocol, test.host, test.port, test.protocol)
		}
	}
}

// TestHookEditsAreAppliedBack pins that what a hook changes takes effect.
func TestHookEditsAreAppliedBack(t *testing.T) {
	transaction := prepared(t)

	edited := toWire(transaction)
	edited.Request.Headers["Cookie"] = "jwt_token=abc"
	edited.Request.Body = `{"sent":true}`
	edited.Expected.StatusCode = "404"
	edited.Skip = true

	applyWire(transaction, edited)

	if transaction.Request.Headers["Cookie"] != "jwt_token=abc" {
		t.Error("a header set by a hook should be applied")
	}
	if transaction.Request.Body != `{"sent":true}` {
		t.Error("a body set by a hook should be applied")
	}
	if transaction.Expected.StatusCode != "404" {
		t.Error("an expectation set by a hook should be applied")
	}
	if !transaction.Skip {
		t.Error("skip should be applied")
	}
}

// TestFullPathOverridesTheURL pins that rewriting the address works through
// fullPath — the field Dredd builds its URL from.
func TestFullPathOverridesTheURL(t *testing.T) {
	transaction := prepared(t)

	edited := toWire(transaction)
	edited.FullPath = "/things/rewritten"
	applyWire(transaction, edited)

	if got := transaction.FullURL(); got != "http://localhost:4000/things/rewritten" {
		t.Errorf("FullURL = %q, want the rewritten path", got)
	}
}

// TestFullPathSurvivesALaterHook pins the bug that made a request appear to go
// somewhere it never went: fullPath was being recomputed from Request.URI on
// every exchange, so a hook editing the URI after the request had been sent
// rewrote the record of where it went.
func TestFullPathSurvivesALaterHook(t *testing.T) {
	transaction := prepared(t)
	original := transaction.FullURL()

	// A before hook edits the URI, as inpace's hooks do.
	edited := toWire(transaction)
	edited.Request.URI = "/things/edited"
	applyWire(transaction, edited)

	if transaction.FullURL() != original {
		t.Fatalf("FullURL = %q after a URI edit, want %q", transaction.FullURL(), original)
	}

	// A later hook — beforeEachValidation — round-trips the transaction again.
	again := toWire(transaction)
	if again.FullPath != "/things" {
		t.Errorf("fullPath = %q on the second exchange, want the original path", again.FullPath)
	}
	applyWire(transaction, again)

	if transaction.FullURL() != original {
		t.Errorf("FullURL = %q after a second exchange, want %q", transaction.FullURL(), original)
	}
}

func TestFailMarker(t *testing.T) {
	if got := failValue(""); got != false {
		t.Errorf("an unfailed transaction should report fail=false, got %v", got)
	}
	if got := failValue("because"); got != "because" {
		t.Errorf("a failed transaction should carry its reason, got %v", got)
	}

	for _, test := range []struct {
		value any
		want  string
	}{
		{false, ""},
		{"a reason", "a reason"},
		{true, "marked as failed by a hook"},
		{nil, ""},
	} {
		if got := failString(test.value); got != test.want {
			t.Errorf("failString(%#v) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestUnsupportedHookLanguageIsRejected(t *testing.T) {
	// Python is supported now; something genuinely absent stands in.
	_, err := Start(t.Context(), Options{Language: "ruby"})
	if err == nil {
		t.Fatal("an unsupported hooks language should be reported")
	}
	// The message offers what there is, rather than only refusing what was
	// asked for: a reader who typed the wrong language wants the right one.
	for _, want := range []string{"ruby", "nodejs", "python"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestABinaryBodySurvivesAHookThatDoesNotTouchIt pins a corruption that reached
// validation.
//
// The hook protocol is newline-delimited JSON, and Go's encoder replaces every
// byte that is not valid UTF-8 with U+FFFD — a PNG header of eight bytes
// arrives at the worker as fourteen. Taking that value back replaced the
// recorded body with a corrupted one, so a binary response was validated,
// reported and diffed against something the server never sent.
func TestABinaryBodySurvivesAHookThatDoesNotTouchIt(t *testing.T) {
	binary := string([]byte{0x89, 'P', 'N', 'G', 0x00, 0xFF, 0xFE, 0x01})

	transaction := &runner.Transaction{
		Request:  runner.Request{Method: "GET", Headers: map[string]string{}, Body: binary},
		Expected: validate.Message{StatusCode: "200", Body: binary},
	}

	// What a worker that only read the transaction would send back.
	wire := toWire(transaction)
	applyWire(transaction, wire)

	if transaction.Request.Body != binary {
		t.Errorf("request body corrupted: %d bytes in, %d out",
			len(binary), len(transaction.Request.Body))
	}
	if transaction.Expected.Body != binary {
		t.Errorf("expected body corrupted: %d bytes in, %d out",
			len(binary), len(transaction.Expected.Body))
	}
}

// TestAHookThatRewritesABodyStillWins pins that the guard does not make bodies
// read-only. A hook exists to change things, and one that genuinely rewrote the
// body has its version taken.
func TestAHookThatRewritesABodyStillWins(t *testing.T) {
	transaction := &runner.Transaction{
		Request:  runner.Request{Method: "POST", Headers: map[string]string{}, Body: `{"a":1}`},
		Expected: validate.Message{StatusCode: "200"},
	}

	wire := toWire(transaction)
	wire.Request.Body = `{"a":2}`
	applyWire(transaction, wire)

	if transaction.Request.Body != `{"a":2}` {
		t.Errorf("body = %q, want the hook's version", transaction.Request.Body)
	}
}
