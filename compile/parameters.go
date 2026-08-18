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
	case InCookie:
		return r.setCookie(parameter, value), nil
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

	// The new value is recorded on the request's own parameter list, not
	// only expanded into the URI, so a SECOND SetParameter — for another
	// parameter — rebuilds the URI from the updated list and keeps this one.
	// Without that, setting limit then offset silently reset limit to its
	// example: every parameter but the last reverted, and a probe that meant
	// to vary two at once varied one.
	updated := make([]Parameter, len(r.Parameters))
	copy(updated, r.Parameters)
	recorded := false
	for i := range updated {
		if updated[i].In == parameter.In && updated[i].Name == parameter.Name {
			updated[i].Value, updated[i].HasValue = value, true
			recorded = true
		}
	}
	if !recorded {
		set := parameter
		set.Value, set.HasValue = value, true
		updated = append(updated, set)
	}
	r.Parameters = updated

	values := map[string]any{}
	for _, existing := range r.Parameters {
		if existing.In == InHeader || !existing.HasValue {
			continue
		}
		values[existing.Name] = styled(existing, existing.Value)
	}
	// The template's variables are keyed by name alone, so a path `id` and a
	// query `id` share one slot. The parameter being set is written last and
	// wins the collision — the value asked for is the value that goes out,
	// which is what the caller (and any probe judging the reply) relies on.
	values[parameter.Name] = styled(parameter, value)

	r.URI = parsed.Expand(values)
	return r, nil
}

// setCookie returns a copy of the request carrying a different value for one
// cookie parameter.
//
// The Cookie header is rebuilt from the whole parameter list rather than
// edited in place, which is the same answer the URI gets a few lines above and
// for the same reason: several parameters share one piece of the request, so
// substituting text into it means reproducing the layout rules beside the code
// that owns them. Rebuilding is also what makes two sequential sets compose —
// the defect SetParameter already had to fix for query parameters, where
// setting `limit` and then `offset` silently reverted `limit` to its example
// and a probe meaning to vary two parameters varied one.
func (r Request) setCookie(parameter Parameter, value any) Request {
	updated := make([]Parameter, len(r.Parameters))
	copy(updated, r.Parameters)

	recorded := false
	for i := range updated {
		if updated[i].In == InCookie && updated[i].Name == parameter.Name {
			updated[i].Value, updated[i].HasValue = value, true
			recorded = true
		}
	}
	if !recorded {
		set := parameter
		set.In = InCookie
		set.Value, set.HasValue = value, true
		updated = append(updated, set)
	}
	r.Parameters = updated

	pairs := make([]string, 0, len(updated))
	for _, existing := range updated {
		if existing.In != InCookie || !existing.HasValue {
			continue
		}
		pairs = append(pairs, existing.Name+"="+headerText(existing.Value))
	}
	if len(pairs) == 0 {
		return r
	}
	r.Headers = setHeader(r.Headers, "Cookie", strings.Join(pairs, "; "))
	return r
}

// styled pre-renders a value whose serialisation style RFC 6570 cannot
// express, so the template expander sees a scalar and lays it out verbatim.
//
//	spaceDelimited  [1,2,3]        → "1 2 3"   (encoded to 1%202%203)
//	pipeDelimited   [1,2,3]        → "1|2|3"
//	deepObject      {a:1, b:2}     → the expander gets a[a]=1&x[b]=2 by way
//	                                  of a map keyed name[key], see below
//
// A value the style does not apply to — a scalar under spaceDelimited, a
// list under deepObject — is passed through untouched; the description asked
// for something the value cannot be, and expansion's own rules are as good a
// guess as any.
func styled(parameter Parameter, value any) any {
	switch parameter.Style {
	case "spaceDelimited", "pipeDelimited":
		list, ok := value.([]any)
		if !ok {
			return value
		}
		sep := " "
		if parameter.Style == "pipeDelimited" {
			sep = "|"
		}
		parts := make([]string, 0, len(list))
		for _, item := range list {
			parts = append(parts, scalarText(item))
		}
		return strings.Join(parts, sep)
	case "deepObject":
		object, ok := value.(map[string]any)
		if !ok {
			return value
		}
		// The template names the parameter once, exploded (`{?x*}`); an
		// exploded map expands to key=value pairs. Rewriting the keys to
		// name[key] here gives deepObject's `x[a]=1&x[b]=2` from the
		// expander's ordinary object rule, no new syntax needed.
		deep := make(map[string]any, len(object))
		for key, item := range object {
			deep[parameter.Name+"["+key+"]"] = scalarText(item)
		}
		return deep
	}
	return value
}

// scalarText is the wire text of a scalar, the way the expander itself
// stringifies one.
func scalarText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
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
