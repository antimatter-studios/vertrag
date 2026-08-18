package reporter

import (
	"sort"
	"strings"

	"github.com/antimatter-studios/vertrag/runner"
)

// redactedHeaders are the headers whose values a report must not repeat.
//
// A curl line is written to be pasted — into a terminal, a ticket, a chat
// message — and the paste travels further than the terminal it came from.
// The names cover what vertrag itself sends credentials in (auth's
// Authorization and Cookie headers) plus the common API-key spellings.
var redactedHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
	"x-api-key":           true,
}

// Curl renders the request as a command that repeats it.
//
// The URL is the absolute one the request was sent to; a line built from the
// relative URI would need the reader to remember which endpoint the run was
// aimed at, which is the kind of context a reproduction line exists to carry.
// Headers are emitted in name order so the same request always renders the
// same line. Credential values are replaced with <redacted>: the reader who
// owns the credential can restore it, and nobody else should receive it.
func Curl(request runner.Request) string {
	if request.URL == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString("curl -X " + request.Method + " " + shellQuote(request.URL))

	names := make([]string, 0, len(request.Headers))
	for name := range request.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := request.Headers[name]
		if redactedHeaders[strings.ToLower(name)] {
			value = "<redacted>"
		}
		b.WriteString(" -H " + shellQuote(name+": "+value))
	}

	if request.Body != "" {
		b.WriteString(" --data " + shellQuote(request.Body))
	}
	return b.String()
}

// shellQuote wraps a value so a POSIX shell reads it verbatim. A single-quoted
// string has no escapes at all, so the one character needing care is the
// single quote itself, spelled by closing the string, escaping the quote, and
// reopening: 'it'\''s'.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
