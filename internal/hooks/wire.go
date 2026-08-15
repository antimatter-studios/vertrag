package hooks

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/antimatter-studios/vertrag/internal/runner"
	"github.com/antimatter-studios/vertrag/internal/validate"
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
		FullPath: t.Request.URI,
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
	t.Request.Body = w.Request.Body

	t.Expected = validate.Message{
		StatusCode: w.Expected.StatusCode,
		Headers:    copyHeaders(w.Expected.Headers),
		Body:       w.Expected.Body,
		BodySchema: w.Expected.BodySchema,
	}

	if w.FullPath != "" && w.FullPath != t.Request.URI {
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
