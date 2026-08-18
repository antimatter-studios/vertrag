package reporter

import (
	"sort"
	"strings"
	"sync"
)

// Bodies are not redacted by guesswork, and this is the exception that is not
// guesswork.
//
// sanitize.go states the rule and the reason for it: there is no way to know
// which field of a body is a secret, and redacting on a guess would hide the
// very payload a failure needs to show. That rule stands. A reporter looking at
// `{"token":"abc"}` cannot know whether `token` is a credential or the name of
// a row being created.
//
// But vertrag does not have to inspect a body to know one thing about it: the
// credential values it is itself holding. The password written in
// `auth.login.body`, the static `auth.header` value, the OAuth2 client secret,
// and the token or cookie the login exchange returned are all known exactly, by
// value, before any report is written. Replacing those exact strings is not a
// guess about what a field means — it is recognising a value we put there.
//
// What forced it: recordings. A HAR file or a VCR cassette is committed to a
// repository and shared far more readily than a terminal log, and `--reporter
// vcr` writes the login exchange like any other — so the posted password and
// the returned session token travelled into a file people were being encouraged
// to commit. Header redaction did not help, because in that one exchange the
// credential is in the body on the way out and in the body on the way back.
//
// The narrowness is the point. Nothing is matched by field name, by pattern, or
// by looking like a token. A value is redacted if and only if vertrag was
// given it or received it as a credential.
var secrets = struct {
	sync.RWMutex
	values map[string]bool
}{values: map[string]bool{}}

// shortestSecret is the length below which a credential is not redacted.
//
// Redaction is a substring replacement, so a two-character secret would rewrite
// every unrelated body that happens to contain those two characters, and the
// report would be worse than useless. A credential that short is not protecting
// anything anyway — this is the one case where refusing to act is safer than
// acting, and it is why the run says what it redacted rather than leaving the
// reader to infer it.
const shortestSecret = 6

// RegisterSecret records a credential value so it is redacted wherever it
// appears in a reported body.
//
// Returns whether it was taken, so a caller can say when a credential was too
// short to protect rather than leaving the reader to assume it was.
func RegisterSecret(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < shortestSecret {
		return false
	}
	secrets.Lock()
	defer secrets.Unlock()
	secrets.values[value] = true
	return true
}

// RegisterCredential records a credential and the secret inside it.
//
// What a login exchange returns is rarely the bare secret: it is the value
// vertrag will SEND, which is `Bearer eyJhbGci…` or `session=eyJhbGci…`. The
// body it came from carries the token alone. Registering only the assembled
// form therefore protects the header — which was already redacted by name —
// and leaves the login response body, which is the one place the token appears
// in the clear. An end-to-end test caught exactly that: the posted password was
// redacted out of a cassette and the returned token was not.
//
// The two shapes stripped here are the two vertrag itself assembles, so this is
// still recognising a value we constructed rather than guessing at one: an
// authentication scheme and its parameter, and a cookie's name=value.
func RegisterCredential(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	RegisterSecret(value)

	// Each leading word is stripped in turn, registering what remains.
	//
	// One strip is not enough, and finding that out cost the end-to-end test a
	// red run: the login template is `Authorization: Bearer {…}`, so what comes
	// back is `Authorization: Bearer eyJhbGci…` — three parts, of which only the
	// last is the secret, and stripping once leaves `Bearer eyJhbGci…`, which
	// matches nothing in the response body the token came from.
	//
	// Every prefix here is a header name or an authentication scheme, both of
	// which are public by construction, and the length floor stops a stray
	// short suffix being registered.
	for rest := value; ; {
		_, after, found := strings.Cut(rest, " ")
		if !found || after == "" {
			break
		}
		RegisterSecret(after)
		rest = after
	}
	// `name=value`, possibly several. The names are not secret; the values are.
	for _, pair := range strings.Split(value, ";") {
		if _, cookie, found := strings.Cut(strings.TrimSpace(pair), "="); found {
			RegisterSecret(cookie)
		}
	}
}

// RegisterSecretsIn records every string value in a login body, at any depth.
//
// The whole body is taken rather than the field somebody named `password`,
// because which field carries the credential is exactly the judgement this file
// exists to avoid making. A login body is short, entirely made of values the
// caller wrote into their configuration, and none of it is interesting in a
// report — so treating all of it as secret costs nothing and guesses nothing.
func RegisterSecretsIn(body map[string]any) {
	for _, value := range body {
		switch typed := value.(type) {
		case string:
			RegisterSecret(typed)
		case map[string]any:
			RegisterSecretsIn(typed)
		case []any:
			for _, item := range typed {
				if nested, ok := item.(map[string]any); ok {
					RegisterSecretsIn(nested)
				} else if text, ok := item.(string); ok {
					RegisterSecret(text)
				}
			}
		}
	}
}

// RedactSecrets replaces every registered credential value in a body.
//
// Longest first, so a token that contains a shorter registered value — a
// password reused as the start of an API key, say — does not leave the tail of
// the longer one exposed by a shorter match landing first.
func RedactSecrets(body string) string {
	if body == "" || !sanitizing.Load() {
		return body
	}

	secrets.RLock()
	values := make([]string, 0, len(secrets.values))
	for value := range secrets.values {
		values = append(values, value)
	}
	secrets.RUnlock()
	if len(values) == 0 {
		return body
	}

	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		body = strings.ReplaceAll(body, value, Redacted)
	}
	return body
}

// ForgetSecrets clears the registry. For tests: the registry is process-wide,
// like the sanitising flag it honours, so one test's credential would otherwise
// be redacted out of another's fixture.
func ForgetSecrets() {
	secrets.Lock()
	defer secrets.Unlock()
	secrets.values = map[string]bool{}
}
