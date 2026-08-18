package fuzz

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
)

// recorder is a stand-in server for one parameter. It keeps what it was sent, so
// a test can assert that a value never left the process — which for a parameter
// matters as much as what the server said about one.
type recorder struct {
	sent  []string
	reply func(value string) string
}

func (r *recorder) sender() Sender {
	return func(ctx context.Context, value any) (validate.Message, error) {
		// A parameter's value is `any` so a list can reach the URI template
		// intact; a test asserting on what was sent wants the text either way.
		rendered := text(value)
		r.sent = append(r.sent, rendered)
		status := "200"
		if r.reply != nil {
			status = r.reply(rendered)
		}
		return validate.Message{StatusCode: status}, nil
	}
}

func accepting() *recorder { return &recorder{} }

func probeParameter(t *testing.T, in, name string, schema generate.Schema, mode generate.Mode, server *recorder) (Finding, bool) {
	t.Helper()
	return ProbeParameter(context.Background(), Subject{In: in, Name: name},
		schema, mode, server.sender(), Options{Cases: 100})
}

// TestAValueOnlyInvalidBeforeSerialisationIsNeverSentAsAParameter is the
// soundness guard this file exists for.
//
// A parameter is a string on the wire whatever its schema says. Generation
// violates `{type: string}` the only way it can, with a value of another type —
// but 12345 reaches the server as "12345", which is a perfectly good string, and
// a server accepting it is right to. Judged on the drawn value the run would
// report a validation bypass against every correct server in existence.
//
// The body probe on the same schema is the control: there the value and the
// bytes carrying it are the same thing, so 12345 really is invalid and really is
// sent.
func TestAValueOnlyInvalidBeforeSerialisationIsNeverSentAsAParameter(t *testing.T) {
	schema := generate.Schema{"type": "string"}

	server := accepting()
	finding, found := probeParameter(t, InQuery, "q", schema, generate.Invalid, server)

	if len(server.sent) != 0 {
		t.Errorf("%d value(s) sent that a correct server would accept, e.g. %q",
			len(server.sent), server.sent[0])
	}
	if !found || !finding.Unprobeable {
		t.Fatalf("finding = %+v, want the honest report that nothing could be probed", finding)
	}
	if !strings.Contains(finding.Message, "not probed") {
		t.Errorf("message = %q, want it to say the parameter was not probed", finding.Message)
	}

	body := accepting()
	if _, found := Probe(context.Background(), schema, generate.Invalid, body.sender(), Options{Cases: 50}); !found {
		t.Error("the same schema IS violable in a body, where the value is the bytes")
	}
	if len(body.sent) == 0 {
		t.Error("no body was sent, so the control tested nothing")
	}
}

// TestATypedParameterIsJudgedAsTheServerWillReadIt is the other half of the same
// problem, and the half that decides whether parameters can be probed at all.
//
// `{type: integer, minimum: 18}` describes the number the server parses out of
// the text, not the text. A guard comparing the text against the schema directly
// would find every drawn value invalid — "42" is a string — and abandon every
// case, leaving the parameter silently untested.
func TestATypedParameterIsJudgedAsTheServerWillReadIt(t *testing.T) {
	schema := generate.Schema{"type": "integer", "minimum": float64(18)}

	// A server doing exactly what the description says: parse, then check.
	careful := &recorder{reply: func(value string) string {
		age, err := strconv.Atoi(value)
		if err != nil || age < 18 {
			return "400"
		}
		return "200"
	}}

	for _, mode := range []generate.Mode{generate.Valid, generate.Invalid} {
		careful.sent = nil
		finding, found := probeParameter(t, InPath, "age", schema, mode, careful)

		if len(careful.sent) == 0 {
			t.Errorf("mode %v: nothing was sent, so the parameter was not tested", mode)
		}
		if found {
			t.Errorf("mode %v: unexpected finding %q for value %q",
				mode, finding.Message, finding.Value)
		}
	}
}

// TestAmbiguouslyReadableValuesAreNotSent pins the rule for a schema that never
// said how to read its values.
//
// `{enum: [1, 2, 3]}` declares no type, so a server may reasonably read the text
// "1" as the number 1, which the enum permits, or as the string "1", which it
// does not. Neither reading can be shown wrong, so a value drawn as valid
// convicts nobody and is dropped. A value in neither reading — a word — is
// unambiguously forbidden and is sent.
func TestAmbiguouslyReadableValuesAreNotSent(t *testing.T) {
	schema := generate.Schema{"enum": []any{float64(1), float64(2), float64(3)}}

	permitted := accepting()
	finding, found := probeParameter(t, InQuery, "page", schema, generate.Valid, permitted)
	if len(permitted.sent) != 0 {
		t.Errorf("sent %q, which the description leaves ambiguous", permitted.sent[0])
	}
	if !found || !finding.Unprobeable {
		t.Errorf("finding = %+v, want the report that nothing could be probed", finding)
	}

	forbidden := accepting()
	if _, found := probeParameter(t, InQuery, "page", schema, generate.Invalid, forbidden); !found {
		t.Error("a server accepting a value no reading of the enum permits should be found")
	}
	if len(forbidden.sent) == 0 {
		t.Error("nothing was sent, so the unambiguous half was not tested either")
	}
}

// TestValuesThatWouldTestSomethingElseAreNotSent pins the guards on what can go
// into each position.
//
// Each of these arrives as something other than what was judged, so the server's
// answer would be about a different question: a slash in a path segment reaches
// another route, an empty value is indistinguishable from an absent parameter,
// and a padded header value is trimmed before the handler sees it. The same
// value in a query string is fine, which is what the second column checks — the
// rule is about the position, not about the value being unpleasant.
func TestValuesThatWouldTestSomethingElseAreNotSent(t *testing.T) {
	for _, test := range []struct {
		name    string
		in      string
		value   string
		sending bool
	}{
		{"a slash in a path segment", InPath, "a/b", false},
		{"a slash in a query value", InQuery, "a/b", true},
		{"a bare dot as a whole path segment", InPath, ".", false},
		{"a padded header value", InHeader, "  acme  ", false},
		{"a padded query value", InQuery, "  acme  ", true},
		{"an empty header value", InHeader, "", false},
		{"an empty query value", InQuery, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			// An enum of one leaves generation no choice, so the case is
			// entirely about whether that value is allowed onto the wire.
			schema := generate.Schema{"type": "string", "enum": []any{test.value}}

			server := accepting()
			probeParameter(t, test.in, "p", schema, generate.Valid, server)

			if sent := len(server.sent) > 0; sent != test.sending {
				t.Errorf("sent = %v, want %v (values sent: %q)", sent, test.sending, server.sent)
			}
		})
	}
}

// TestAParameterBypassIsFoundAndSaysWhichParameter is the finding parameter
// fuzzing exists for, and the reason its report has to name a location: a
// project with a dozen parameters gets a message it can act on rather than one
// it has to reproduce first.
func TestAParameterBypassIsFoundAndSaysWhichParameter(t *testing.T) {
	schema := generate.Schema{"type": "integer", "minimum": float64(1), "maximum": float64(100)}

	finding, found := probeParameter(t, InQuery, "limit", schema, generate.Invalid, accepting())
	if !found {
		t.Fatal("a server ignoring a documented range should be found")
	}
	if finding.Subject != (Subject{In: InQuery, Name: "limit"}) {
		t.Errorf("subject = %+v, want the query parameter", finding.Subject)
	}
	if !strings.Contains(finding.Message, `query parameter "limit"`) {
		t.Errorf("message = %q, want it to name the parameter and where it lives", finding.Message)
	}
	if !strings.Contains(finding.Message, "not enforced") {
		t.Errorf("message = %q, want the validation-bypass wording", finding.Message)
	}
}

// TestAServerErrorFromAParameterIsFound pins the classic: a path parameter typed
// as a number, handed a word, that the handler never checked before using.
func TestAServerErrorFromAParameterIsFound(t *testing.T) {
	schema := generate.Schema{"type": "integer"}

	crashes := &recorder{reply: func(value string) string {
		if _, err := strconv.Atoi(value); err != nil {
			return "500"
		}
		return "200"
	}}

	finding, found := probeParameter(t, InPath, "id", schema, generate.Invalid, crashes)
	if !found {
		t.Fatal("a handler that fails on a value its schema forbids should be found")
	}
	if !strings.Contains(finding.Message, "failed rather than rejected") {
		t.Errorf("message = %q, want the crash wording", finding.Message)
	}
}

// TestAWellFormedIdentifierNamingNothingIsNotAFinding pins the one exemption in
// judging.
//
// A description says which path parameter values are well formed, not which
// resources exist. Generating a valid id and getting a 404 is the server saying
// "no such thing", which is correct — reporting it would produce a finding
// against every server whose database does not happen to hold the number that
// was drawn, which is nearly all of them.
//
// The exemption is for the path alone: the endpoint a query parameter belongs to
// is still there, so a 404 for a value the description permits is a real
// disagreement. That is the control here.
func TestAWellFormedIdentifierNamingNothingIsNotAFinding(t *testing.T) {
	schema := generate.Schema{"type": "integer", "minimum": float64(1)}
	missing := func() *recorder { return &recorder{reply: func(string) string { return "404" }} }

	if finding, found := probeParameter(t, InPath, "id", schema, generate.Valid, missing()); found {
		t.Errorf("a 404 for a well-formed id is not a contract violation, got %q", finding.Message)
	}

	if _, found := probeParameter(t, InQuery, "id", schema, generate.Valid, missing()); !found {
		t.Error("a 404 for a query parameter the description permits should still be reported")
	}
}

// TestProbeableRejectsSchemasWithNoSingleWireForm pins what is passed over
// knowingly.
//
// An array parameter's text depends on a serialisation style — comma, space,
// pipe, or a repeated key — that the compiled request no longer records.
// Guessing one would send something the description never described and then
// judge the server on it.
func TestProbeableRejectsSchemasWithNoSingleWireForm(t *testing.T) {
	for _, test := range []struct {
		schema generate.Schema
		style  string
		want   bool
	}{
		{generate.Schema{"type": "string"}, "", true},
		{generate.Schema{"type": "integer"}, "", true},
		{generate.Schema{"type": "boolean"}, "", true},
		{generate.Schema{"type": []any{"string", "null"}}, "", true},
		{generate.Schema{"enum": []any{"a", "b"}}, "", true},
		// An array is probeable under every style: the compiled request lays
		// it out by the description's own rule — form exploded or not,
		// spaceDelimited, pipeDelimited — so the server is judged on the
		// shape it documented, never on a guess.
		{generate.Schema{"type": "array", "items": map[string]any{"type": "string"}}, "", true},
		{generate.Schema{"type": "array", "items": map[string]any{"type": "string"}}, "spaceDelimited", true},
		{generate.Schema{"type": "array", "items": map[string]any{"type": "string"}}, "pipeDelimited", true},
		// An object has a wire form only under deepObject (`x[a]=1&x[b]=2`).
		// Under any other style there is no agreed layout, so it is left out
		// rather than sent as something the description never described.
		{generate.Schema{"type": "object"}, "", false},
		{generate.Schema{"type": "object"}, "spaceDelimited", false},
		{generate.Schema{"type": "object"}, "deepObject", true},
	} {
		if got := Probeable(test.schema, test.style); got != test.want {
			t.Errorf("Probeable(%v, %q) = %v, want %v", test.schema, test.style, got, test.want)
		}
	}
}

// TestATransportFailureNamesTheValueThatCausedIt pins that a dropped connection
// is reported usefully.
//
// A handler that panics answers nothing at all: the connection closes and there
// is no status code to judge. That is a crash, and the value that provoked it is
// the whole content of the report — an empty finding would say a request failed
// and leave the reader to find out which.
func TestATransportFailureNamesTheValueThatCausedIt(t *testing.T) {
	refusing := func(ctx context.Context, value any) (validate.Message, error) {
		return validate.Message{}, context.DeadlineExceeded
	}

	finding, found := ProbeParameter(context.Background(), Subject{In: InPath, Name: "id"},
		generate.Schema{"type": "integer"}, generate.Valid, refusing, Options{Cases: 5})
	if !found {
		t.Fatal("a transport failure should be reported rather than passing silently")
	}
	if finding.Value == "" || finding.Message == "" {
		t.Errorf("finding = %+v, want the value and the reason", finding)
	}
	if finding.Subject.Name != "id" {
		t.Errorf("subject = %+v, want the parameter that was being varied", finding.Subject)
	}
}
