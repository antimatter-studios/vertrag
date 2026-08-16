package link

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Exchange is one completed request and response, which is what a runtime
// expression reads from.
//
// It is deliberately not the runner's own result type: an expression needs the
// request as it was actually sent and the response as it actually came back,
// and nothing else. Keeping the dependency this narrow is what lets the
// evaluator be tested without a server, a description or a compiled
// transaction.
type Exchange struct {
	URL    string
	Method string

	StatusCode string

	RequestPath   map[string]string
	RequestQuery  map[string]string
	RequestHeader map[string]string
	RequestBody   string

	ResponseHeader map[string]string
	ResponseBody   string
}

// Evaluate resolves a runtime expression against a completed exchange.
//
// The grammar is the whole of OpenAPI's runtime expression language, which is
// seven forms with no arithmetic, no conditionals, no function calls and no
// concatenation. That is the single most useful fact about it: the language is
// finished, byte-identical across 3.0.3, 3.0.4, 3.1.0 and 3.1.1, and cannot
// grow underneath this code.
//
// A value that cannot be resolved returns false rather than an empty string,
// because a link that cannot be followed must leave its target alone rather
// than send it a blank where an identifier was meant to go.
func Evaluate(expression string, exchange Exchange) (any, bool) {
	// A value may embed expressions in braces rather than being one — the
	// specification's own example writes `id: "$response.body#/id"` but also
	// permits `Authorization: "Bearer {$response.body#/token}"`.
	if strings.Contains(expression, "{$") {
		return expandEmbedded(expression, exchange)
	}
	return resolve(expression, exchange)
}

func resolve(expression string, exchange Exchange) (any, bool) {
	switch {
	case expression == "$url":
		return exchange.URL, true
	case expression == "$method":
		return exchange.Method, true
	case expression == "$statusCode":
		return exchange.StatusCode, true

	case strings.HasPrefix(expression, "$request."):
		return source(expression[len("$request."):],
			exchange.RequestPath, exchange.RequestQuery, exchange.RequestHeader, exchange.RequestBody)

	case strings.HasPrefix(expression, "$response."):
		// A response has no path or query of its own; the specification's
		// grammar admits them and there is nothing for them to read.
		return source(expression[len("$response."):],
			nil, nil, exchange.ResponseHeader, exchange.ResponseBody)

	default:
		// Not an expression at all. A Link Object's parameter values may be
		// literals, and a literal is worth passing through unchanged.
		return expression, true
	}
}

// source reads one of the four places a value can come from.
func source(rest string, path, query, header map[string]string, body string) (any, bool) {
	switch {
	case strings.HasPrefix(rest, "header."):
		// `token` is case-insensitive by the specification's own wording, and
		// HTTP agrees. The name is lowered on both sides of the lookup.
		//
		// Split on the FIRST dot after "header", because tchar includes '.',
		// '$' and '#' — a header may legitimately be called `X-A.B`.
		name := strings.ToLower(rest[len("header."):])
		for key, value := range header {
			if strings.ToLower(key) == name {
				return value, true
			}
		}
		return nil, false

	case strings.HasPrefix(rest, "query."):
		// `name` IS case-sensitive, unlike a header token, so this is an exact
		// lookup and deliberately not the loop above.
		value, found := query[rest[len("query."):]]
		return value, found

	case strings.HasPrefix(rest, "path."):
		value, found := path[rest[len("path."):]]
		return value, found

	case rest == "body":
		return decodeBody(body)

	case strings.HasPrefix(rest, "body#"):
		document, ok := decodeBody(body)
		if !ok {
			return nil, false
		}
		// An empty pointer is RFC 6901's way of naming the whole document, so
		// `$response.body#` and `$response.body` mean the same thing.
		return pointer(document, rest[len("body#"):])

	default:
		return nil, false
	}
}

func decodeBody(body string) (any, bool) {
	if strings.TrimSpace(body) == "" {
		return nil, false
	}
	var decoded any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		// A body that will not parse has nothing to point into. This is not an
		// error to report — the response may legitimately not be JSON — it is
		// simply a link that cannot be followed.
		return nil, false
	}
	return decoded, true
}

// pointer walks an RFC 6901 JSON Pointer.
func pointer(document any, path string) (any, bool) {
	if path == "" {
		return document, true
	}
	if !strings.HasPrefix(path, "/") {
		// Every non-empty pointer begins with '/'. Anything else is malformed.
		return nil, false
	}

	current := document
	for _, token := range strings.Split(path[1:], "/") {
		// The escapes have to be undone in this order: undoing ~1 first would
		// turn "~01" into "~1" and then into "/", where the document means a
		// literal "~1".
		token = strings.ReplaceAll(token, "~1", "/")
		token = strings.ReplaceAll(token, "~0", "~")

		switch typed := current.(type) {
		case map[string]any:
			value, found := typed[token]
			if !found {
				return nil, false
			}
			current = value

		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]

		default:
			// A scalar with pointer left to walk: the document is not the
			// shape the expression assumed.
			return nil, false
		}
	}
	return current, true
}

// expandEmbedded substitutes `{$...}` expressions inside a larger string.
//
// The result is always a string, because the surrounding text makes it one —
// `Bearer {$response.body#/token}` is a header value, not a token.
func expandEmbedded(template string, exchange Exchange) (any, bool) {
	var out strings.Builder
	rest := template

	for {
		open := strings.Index(rest, "{$")
		if open < 0 {
			out.WriteString(rest)
			return out.String(), true
		}
		close := strings.Index(rest[open:], "}")
		if close < 0 {
			// An unterminated expression is a malformed value, and guessing
			// where it was meant to end would send something arbitrary.
			return nil, false
		}
		close += open

		out.WriteString(rest[:open])

		value, ok := resolve(rest[open+1:close], exchange)
		if !ok {
			return nil, false
		}
		out.WriteString(stringify(value))

		rest = rest[close+1:]
	}
}

// stringify renders a resolved value for embedding in text.
//
// A number arrives from JSON as a float64, and an identifier of 42 has to reach
// a URL as "42" rather than "42.000000" or "4.2e+01".
func stringify(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	case nil:
		return ""
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}
