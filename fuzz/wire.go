package fuzz

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
)

// wire is how a drawn value reaches the server, and what it looks like when it
// arrives.
//
// The two halves are separate because for a parameter they are not inverses.
// Rendering an integer as a parameter loses that it was an integer; the server
// gets a string and has to decide what it means. Judging the case needs the
// second half, not the first: the question is never "what did generation draw"
// but "what will the server have in its hands, and does the schema allow it".
type wire struct {
	// render turns a drawn value into what will actually be sent, and reports
	// false for a value that has no such form and must not be sent.
	render func(value any) (string, bool)

	// interpret returns every value a correct server could decode the rendered
	// form back into, each as a JSON document ready to validate. More than one
	// means the description left the reading ambiguous; none means the form
	// cannot be interpreted at all.
	interpret func(rendered string) []string
}

// bodyForm sends a body as JSON, which the server reads back as JSON. There is
// only ever one reading, and it is the value that was drawn.
func bodyForm() wire {
	return wire{
		render: func(value any) (string, bool) {
			encoded, err := json.Marshal(value)
			if err != nil {
				// A value that will not serialise is a generator problem, not a
				// server one, and blaming the server for it would be wrong.
				return "", false
			}
			return string(encoded), true
		},
		interpret: func(rendered string) []string { return []string{rendered} },
	}
}

// parameterForm sends a parameter as the single string a path segment, a query
// value or a header field can hold.
func parameterForm(subject Subject, schema generate.Schema) wire {
	return wire{
		render: func(value any) (string, bool) {
			rendered, ok := scalarText(value)
			if !ok {
				// An array or an object has no one form here: which of comma,
				// space, pipe or a repeated key separates its members is
				// decided by a serialisation style the compiled request no
				// longer records. Guessing would send something the description
				// never described, and then judge the server on it.
				return "", false
			}
			return rendered, sendable(subject.In, rendered)
		},
		interpret: func(rendered string) []string {
			return interpretations(schema, rendered)
		},
	}
}

// scalarText renders a drawn value as the text a parameter carries.
//
// The renderings match the URI template expander's, because that is what will
// put a path or query value into the request — a number that appeared as "42"
// there and "42.000000" here would be judged as something other than what was
// sent.
func scalarText(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case int:
		return strconv.Itoa(v), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case float64:
		if math.IsInf(v, 0) || math.IsNaN(v) {
			return "", false
		}
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case json.Number:
		return v.String(), true
	default:
		return "", false
	}
}

// sendable rejects values that would test something other than the parameter.
//
// Each rule is here because without it a correct server looks broken:
//
//   - An empty value is indistinguishable from an absent parameter to most
//     servers, whether it is an empty path segment, a bare `?q=` or a header
//     with no field value. The reply would be about the parameter being
//     missing, which is a different question.
//   - A path value carrying a slash, a question mark or a hash changes which
//     route the request reaches even after encoding, because routers and
//     proxies do not agree on when %2F is a separator. The server would be
//     answering about another endpoint.
//   - `.` and `..` as a whole path segment are resolved away before anything
//     sees them, which is either nothing or a traversal attempt. Traversal is
//     worth testing and is not this: a server is SUPPOSED to reject it, so the
//     expectations are the opposite way round, and folding it in here would
//     report a correct rejection as a disagreement.
//   - A header value with control characters cannot be sent at all, and one
//     padded with spaces arrives trimmed, so the server would be judged on a
//     value it never received.
func sendable(in, rendered string) bool {
	if rendered == "" {
		return false
	}
	if strings.ContainsFunc(rendered, func(r rune) bool { return unicode.IsControl(r) }) {
		return false
	}

	switch in {
	case InPath:
		if rendered == "." || rendered == ".." {
			return false
		}
		return !strings.ContainsAny(rendered, "/?#")
	case InHeader:
		return rendered == strings.TrimSpace(rendered)
	default:
		return true
	}
}

// intendedValidity says whether every reading of a sent value satisfies the
// schema, and whether the readings agreed at all.
//
// Agreement is what makes a parameter finding trustworthy. A path parameter
// declared `{type: string, minimum: 100}` and sent as "42" is invalid read as a
// number and valid read as a string, and the description does not say which the
// server should do — so whichever way it answers, it cannot be shown to be
// wrong, and the case is dropped rather than reported. Only a value that is
// unambiguously permitted, or unambiguously forbidden, can convict a server.
func intendedValidity(rawSchema json.RawMessage, readings []string) (valid, decided bool) {
	if len(readings) == 0 {
		return false, false
	}
	for i, reading := range readings {
		result := validate.AgainstSchema(rawSchema, reading)
		if i == 0 {
			valid = result.Valid
			continue
		}
		if result.Valid != valid {
			return false, false
		}
	}
	return valid, true
}

// interpretations returns the values a correct server could read out of a
// parameter's text, as JSON documents.
//
// The declared type is what decides it, because that is what the description
// promised: a server implementing `{type: integer}` parses "42" into the number
// 42, and one that treated it as the string "42" and rejected it would be
// wrong. Text the declared type cannot parse is left as text, which is exactly
// the case worth testing — "abc" for an integer parameter is the value that
// should be refused and often is not.
//
// An untyped schema has promised nothing, so both readings stand and the caller
// only proceeds when they agree.
func interpretations(schema generate.Schema, rendered string) []string {
	types := declaredTypes(schema)

	var readings []any
	if len(types) == 0 {
		readings = append(readings, rendered)
		if parsed, ok := jsonScalar(rendered); ok {
			readings = append(readings, parsed)
		}
	}
	for _, name := range types {
		reading, ok := coerce(name, rendered)
		if !ok {
			// A type whose wire form depends on a serialisation style the
			// compiled request does not record. Nothing can be concluded.
			return nil
		}
		readings = append(readings, reading)
	}

	var out []string
	for _, reading := range readings {
		encoded, err := json.Marshal(reading)
		if err != nil {
			continue
		}
		if !contains(out, string(encoded)) {
			out = append(out, string(encoded))
		}
	}
	return out
}

// coerce reads a parameter's text as one declared type would.
func coerce(declared, rendered string) (any, bool) {
	switch declared {
	case "string":
		return rendered, true

	case "integer", "number":
		number, err := strconv.ParseFloat(rendered, 64)
		if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			// Not a number the server can parse, so what it has is the text —
			// and the text is what its schema will reject.
			return rendered, true
		}
		return number, true

	case "boolean":
		switch rendered {
		case "true":
			return true, true
		case "false":
			return false, true
		}
		return rendered, true

	case "null":
		if rendered == "null" {
			return nil, true
		}
		return rendered, true

	default:
		// "array" and "object": see parameterForm's render.
		return nil, false
	}
}

// jsonScalar reads text as a JSON number, boolean or null, for the untyped case.
//
// Strings, arrays and objects are deliberately not read: the text is already
// being considered as a string, and a parameter that looks like a JSON array is
// a coincidence rather than a reading a server would make.
func jsonScalar(rendered string) (any, bool) {
	var parsed any
	if err := json.Unmarshal([]byte(rendered), &parsed); err != nil {
		return nil, false
	}
	switch parsed.(type) {
	case float64, bool, nil:
		return parsed, true
	default:
		return nil, false
	}
}

// declaredTypes lists the types a schema allows, which JSON Schema permits to
// be one name or several.
func declaredTypes(schema generate.Schema) []string {
	switch declared := schema["type"].(type) {
	case string:
		return []string{declared}
	case []any:
		var names []string
		for _, item := range declared {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
		return names
	default:
		return nil
	}
}

// Probeable reports whether a parameter schema describes values that can be
// carried as a single string.
//
// It is the check a caller makes before probing at all, so that an array or
// object parameter is passed over knowingly rather than drawn from twenty times
// and abandoned every time.
func Probeable(schema generate.Schema) bool {
	for _, declared := range declaredTypes(schema) {
		if _, ok := coerce(declared, "x"); !ok {
			return false
		}
	}
	return true
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
