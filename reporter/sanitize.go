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
	if !sanitizing.Load() {
		return value
	}
	// Cookie before the list, because it is the one header whose value is a
	// list of independent values rather than a single one. See redactCookies.
	if strings.EqualFold(name, "cookie") {
		return redactCookies(value)
	}
	if redactedHeaders[strings.ToLower(name)] {
		return Redacted
	}
	return value
}

// redactCookies replaces every cookie's value and keeps every cookie's name.
//
// A Cookie header used to be blanked whole, which was correct while the only
// thing vertrag ever put there was the credential. Now a description can
// declare cookie PARAMETERS, so the header carries a mixture: a session the
// run obtained, and inputs the operation documents. Blanking the line hides
// both, and hiding the parameters costs the reader the thing a report exists
// to give them — a failing request they can reproduce. `Cookie: <redacted>`
// does not even say how many cookies were sent, let alone which.
//
// So the names stay and the values go, which is the rule the rest of this file
// already follows for headers: "the header NAME still appears, and
// --no-sanitize is there for the person debugging their own credential".
//
// Every value goes, including the ones the description wrote down, because
// vertrag cannot tell which pair is the credential. The credential's name
// comes from the server's Set-Cookie at login and a documented parameter may
// be called `session` just as easily; redacting only the pair whose name
// matches auth's would leak a live session the moment a project named things
// differently from the guess. A cassette is committed to a repository, so that
// mistake would be permanent — which is the same reasoning that makes HAR's
// parsed `cookies` array deliberately empty.
//
// Set-Cookie is deliberately NOT treated this way: its value is one cookie
// followed by attributes (`Path`, `Expires`, `HttpOnly`), not a list of
// independent pairs, and nobody reproducing a request needs to read it back.
func redactCookies(value string) string {
	var pairs []string
	for _, pair := range strings.Split(value, ";") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		name, _, separated := strings.Cut(pair, "=")
		name = strings.TrimSpace(name)
		if !separated || name == "" {
			// Not a name/value pair at all, so there is no name to keep and
			// the whole of it goes rather than being guessed at.
			pairs = append(pairs, Redacted)
			continue
		}
		pairs = append(pairs, name+"="+Redacted)
	}
	if len(pairs) == 0 {
		return Redacted
	}
	return strings.Join(pairs, "; ")
}
