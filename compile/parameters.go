package compile

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/antimatter-studios/vertrag/uritemplate"
)

// SetParameter returns a copy of the request carrying a different value for one
// of its parameters.
//
// The value is the one the server will see once it has decoded the request, not
// the text that appears in it: a path parameter is percent-encoded for the
// segment it sits in, and by whichever rules the template's operator selects, so
// the two are not the same string. Doing the substitution by editing the URI
// text would have to reproduce that encoding — and would still not work for a
// query parameter the description gave no example for, which is absent from the
// compiled URI altogether and has to be added in the right place.
//
// So the URI is expanded again from the template it came from, through the same
// expander the compiler used, with one value replaced. What that guarantees is
// the property the whole exercise depends on: the generated request differs from
// the compiled one in exactly one parameter, so whatever the server does
// differently is about that parameter.
// The value is `any` rather than a string because a parameter may be a list.
// A list has no single text form — whether it becomes `tags=a&tags=b` or
// `tags=a,b` depends on the explode modifier, which the URI template already
// carries — so rendering it here would mean reimplementing the expander's own
// rules beside it and getting one of the two wrong.
func (r Request) SetParameter(parameter Parameter, value any) (Request, error) {
	switch parameter.In {
	case InHeader:
		r.Headers = setHeader(r.Headers, parameter.Name, headerText(value))
		return r, nil
	case InPath, InQuery:
	default:
		return r, fmt.Errorf("parameter %q travels in %q, which is nowhere this can put it",
			parameter.Name, parameter.In)
	}

	if r.Template == "" {
		return r, fmt.Errorf("the request carries no URI template to substitute %q into", parameter.Name)
	}
	parsed, err := uritemplate.Parse(r.Template)
	if err != nil {
		return r, fmt.Errorf("re-reading the URI template %q: %w", r.Template, err)
	}

	values := map[string]any{}
	for _, existing := range r.Parameters {
		if existing.In == InHeader || !existing.HasValue {
			continue
		}
		values[existing.Name] = existing.Value
	}
	values[parameter.Name] = value

	r.URI = parsed.Expand(values)
	return r, nil
}

// setHeader replaces a header's value, adding it when the request does not
// already carry it.
//
// The list is copied rather than written through, because a Request is passed
// around by value and its headers are not: mutating them in place would change
// the compiled transaction every later request is built from.
func setHeader(headers []Header, name, value string) []Header {
	replaced := make([]Header, 0, len(headers)+1)
	found := false
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			header.Value = value
			found = true
		}
		replaced = append(replaced, header)
	}
	if !found {
		replaced = append(replaced, Header{Name: name, Value: value})
	}
	return replaced
}

// headerText renders a value for a header, which is text whatever it describes.
//
// A list becomes a comma-separated line, which is HTTP's own #rule and what the
// runner's joining of a repeated header produces.
func headerText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, headerText(item))
		}
		return strings.Join(parts, ",")
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}
