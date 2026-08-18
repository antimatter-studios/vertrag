package reporter

import (
	"strings"
	"sync/atomic"
)

// A report is written to be kept and shared — archived by CI, pasted into a
// ticket, attached to a chat message — and travels much further than the
// terminal that produced it. Every credential a run sends would otherwise
// travel with it. So the values of credential-bearing headers are replaced
// with <redacted> in every reporter, the same way the curl line already did:
// the reader who owns the credential can restore it, and nobody else should
// receive it. What was sent is not lost — the header NAME still appears, and
// --no-sanitize is there for the person debugging their own credential.
//
// The names are the ones vertrag itself sends credentials in (auth's
// Authorization and Cookie) plus the common API-key spellings; a project with
// its own header adds it with --sanitize-header. Bodies are not touched:
// there is no way to know which field of a body is a secret, and redacting on
// a guess would hide the very payload a failure needs to show.

// redactedHeaders is the default list. Names are compared case-insensitively.
var redactedHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
	"x-api-key":           true,
	"x-auth-token":        true,
	"api-key":             true,
}

// Redacted is the text a redacted value becomes.
const Redacted = "<redacted>"

// sanitizing is on unless a run turned it off. It is process-wide because a
// run has one policy and every reporter it builds shares it; a per-reporter
// flag would let a junit file redact while the terminal leaked, which is not
// a configuration anyone means.
var sanitizing atomic.Bool

func init() { sanitizing.Store(true) }

// SetSanitize turns credential redaction on or off for every reporter.
func SetSanitize(on bool) { sanitizing.Store(on) }

// AddRedactedHeader adds a header name to the redaction list for this run.
func AddRedactedHeader(name string) {
	redactedHeaders[strings.ToLower(strings.TrimSpace(name))] = true
}

// Redact returns the value a report should show for a header. A credential
// becomes <redacted>; anything else is returned as it was.
func Redact(name, value string) string {
	if sanitizing.Load() && redactedHeaders[strings.ToLower(name)] {
		return Redacted
	}
	return value
}
