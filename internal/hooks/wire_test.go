package hooks

import (
	"encoding/json"
	"testing"

	"github.com/antimatter-studios/vertrag/internal/compile"
	"github.com/antimatter-studios/vertrag/internal/runner"
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
	_, err := Start(t.Context(), Options{Language: "python"})
	if err == nil {
		t.Fatal("an unsupported hooks language should be reported")
	}
}
