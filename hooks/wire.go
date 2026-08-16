package hooks

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// wireTransaction is the shape a hook file sees.
//
// The field names are Dredd's, because hook files are written against them: a
// hook reaching for `transaction.expected.statusCode` must find it there. Fail
// is `any` because Dredd lets a hook assign either a string or `false`, and
// both have to survive the round trip.
type wireTransaction struct {
	Name     string      `json:"name"`
	ID       string      `json:"id"`
	Host     string      `json:"host"`
	Port     string      `json:"port"`
	Protocol string      `json:"protocol"`
	FullPath string      `json:"fullPath"`
	Request  wireRequest `json:"request"`
	Expected wireMessage `json:"expected"`
	Real     wireMessage `json:"real"`
	Skip     bool        `json:"skip"`
	Fail     any         `json:"fail"`
}

type wireRequest struct {
	Method  string            `json:"method"`
	URI     string            `json:"uri"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type wireMessage struct {
	StatusCode string            `json:"statusCode,omitempty"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	BodySchema json.RawMessage   `json:"bodySchema,omitempty"`
}

func toWire(t *runner.Transaction) wireTransaction {
	host, port, protocol := splitEndpoint(t.Endpoint())

	return wireTransaction{
		Name:     t.Name,
		ID:       t.Request.Method + " (" + t.Expected.StatusCode + ") " + t.Request.URI,
		Host:     host,
		Port:     port,
		Protocol: protocol,
		FullPath: t.FullPath,
		Request: wireRequest{
			Method:  t.Request.Method,
			URI:     t.Request.URI,
			Headers: copyHeaders(t.Request.Headers),
			Body:    t.Request.Body,
		},
		Expected: wireMessage{
			StatusCode: t.Expected.StatusCode,
			Headers:    copyHeaders(t.Expected.Headers),
			Body:       t.Expected.Body,
			BodySchema: t.Expected.BodySchema,
		},
		Real: wireMessage{
			StatusCode: t.Real.StatusCode,
			Headers:    copyHeaders(t.Real.Headers),
			Body:       t.Real.Body,
		},
		Skip: t.Skip,
		Fail: failValue(t.Fail),
	}
}

// applyWire copies a hook's edits back onto the transaction.
//
// Only what a hook can meaningfully change is read back. The address the
// request goes to comes from fullPath, matching Dredd: a hook that rewrites
// `request.uri` alone changes nothing, because Dredd builds the URL from
// fullPath and vertrag does the same rather than being quietly more helpful.
func applyWire(t *runner.Transaction, w wireTransaction) {
	t.Request.Method = w.Request.Method
	t.Request.URI = w.Request.URI
	t.Request.Headers = copyHeaders(w.Request.Headers)
	t.Request.Body = keptBody(t.Request.Body, w.Request.Body)

	t.Expected = validate.Message{
		StatusCode: w.Expected.StatusCode,
		Headers:    copyHeaders(w.Expected.Headers),
		Body:       keptBody(t.Expected.Body, w.Expected.Body),
		BodySchema: w.Expected.BodySchema,
	}

	// Only fullPath redirects the request. A hook that edited request.uri has
	// changed what a later hook reads there, not where the request goes —
	// matching Dredd.
	if w.FullPath != "" {
		t.FullPath = w.FullPath
		t.SetFullURL(t.Endpoint() + w.FullPath)
	}

	t.Skip = w.Skip
	t.Fail = failString(w.Fail)
}

// failValue renders a fail marker the way a hook file expects to read it:
// `false` when the transaction is fine, a string when it is not.
func failValue(fail string) any {
	if fail == "" {
		return false
	}
	return fail
}

func failString(fail any) string {
	switch v := fail.(type) {
	case string:
		return v
	case bool:
		if v {
			// `fail = true` carries no reason, but it is still a failure and
			// must not be read as "fine".
			return "marked as failed by a hook"
		}
		return ""
	default:
		return ""
	}
}

func copyHeaders(headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		out[name] = value
	}
	return out
}

// splitEndpoint breaks an endpoint into the parts a hook file reads separately.
func splitEndpoint(endpoint string) (host, port, protocol string) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint, "", "http:"
	}

	protocol = parsed.Scheme + ":"
	host = parsed.Hostname()
	port = parsed.Port()
	if port == "" {
		// Dredd reports the port a hook would need to rebuild the URL, so the
		// scheme's default is filled in rather than left blank.
		if strings.EqualFold(parsed.Scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	return host, port, protocol
}

// keptBody decides whether a hook actually changed a body.
//
// The protocol is newline-delimited JSON, and Go's encoder replaces every byte
// that is not valid UTF-8 with U+FFFD. A PNG header of eight bytes arrives at
// the worker as fourteen, and taking that back would replace the recorded body
// with a corrupted one — so a binary response would be validated, reported and
// diffed against something the server never sent.
//
// A hook that did not touch the body sends back exactly what it was given, so
// comparing against the same round trip identifies that case exactly: if the
// returned value matches what the hook would have seen, the original bytes are
// kept. Only a hook that genuinely rewrote the body has its version taken, and
// there the corruption is moot — a hook cannot express arbitrary bytes in JSON
// anyway, so whatever it sent is what it meant.
//
// Dredd has the same defect and no equivalent guard. Reproducing it would mean
// corrupting a body to match, which is a poor reason to corrupt a body.
func keptBody(original, returned string) string {
	if original == returned {
		return returned
	}
	if returned == overTheWire(original) {
		// Unchanged by the hook; the difference is the wire's doing.
		return original
	}
	return returned
}

// overTheWire renders a string as the hook worker would have received it.
func overTheWire(body string) string {
	if utf8.ValidString(body) {
		return body
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return body
	}
	var decoded string
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return body
	}
	return decoded
}
