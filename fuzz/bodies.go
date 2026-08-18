package fuzz

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/antimatter-studios/vertrag/generate"
)

// The body content types generation can speak. JSON is the natural one — a
// value and its bytes are the same thing. Form-encoded and multipart bodies
// are objects whose members travel as TEXT, so they are judged the way a
// parameter is: by what the server will parse out of the text, not by the
// text itself.
const (
	ContentJSON      = "application/json"
	ContentForm      = "application/x-www-form-urlencoded"
	ContentMultipart = "multipart/form-data"
)

// multipartBoundary is fixed, so a generated multipart body is the same bytes
// for the same value and a finding replays exactly.
const multipartBoundary = "vertrag-boundary"

// BodyForm returns the wire form for a request body of the given media type,
// or false when generation has no way to lay a value out in it. The media
// type is the bare one — parameters like charset or boundary already removed.
func BodyForm(mediaType string, schema generate.Schema) (wire, bool) {
	switch {
	case mediaType == "" || mediaType == ContentJSON || strings.HasSuffix(mediaType, "+json"):
		return bodyForm(), true
	case mediaType == ContentForm:
		return formBody(schema), true
	case mediaType == ContentMultipart:
		return multipartBody(schema), true
	}
	return wire{}, false
}

// SpeaksBody reports whether generation can lay a body out in the media type.
func SpeaksBody(mediaType string, schema generate.Schema) bool {
	_, ok := BodyForm(mediaType, schema)
	return ok
}

// formBody sends an object as application/x-www-form-urlencoded: `a=1&b=x`,
// members in name order. Only an object has this form; a generator drawing
// anything else to violate an object schema has drawn something the media
// type cannot carry, and the case is abandoned rather than sent as nonsense.
func formBody(schema generate.Schema) wire {
	return wire{
		render: func(value any) (any, bool) {
			object, ok := value.(map[string]any)
			if !ok {
				return nil, false
			}
			values := url.Values{}
			for name, member := range object {
				text, ok := scalarText(member)
				if !ok {
					// A nested object or list has no single form-encoded
					// spelling; there is no agreed way to send it.
					return nil, false
				}
				values.Set(name, text)
			}
			return values.Encode(), true
		},
		interpret: func(rendered any) []string {
			text, ok := rendered.(string)
			if !ok {
				return nil
			}
			return interpretForm(schema, text)
		},
	}
}

// multipartBody sends an object as multipart/form-data, one part per member,
// with the fixed boundary. Same shape rule as formBody.
func multipartBody(schema generate.Schema) wire {
	return wire{
		render: func(value any) (any, bool) {
			object, ok := value.(map[string]any)
			if !ok {
				return nil, false
			}
			names := make([]string, 0, len(object))
			for name := range object {
				names = append(names, name)
			}
			sort.Strings(names)

			var body strings.Builder
			for _, name := range names {
				text, ok := scalarText(object[name])
				if !ok {
					return nil, false
				}
				fmt.Fprintf(&body, "--%s\r\nContent-Disposition: form-data; name=%q\r\n\r\n%s\r\n",
					multipartBoundary, name, text)
			}
			fmt.Fprintf(&body, "--%s--\r\n", multipartBoundary)
			return body.String(), true
		},
		interpret: func(rendered any) []string {
			text, ok := rendered.(string)
			if !ok {
				return nil
			}
			return interpretMultipart(schema, text)
		},
	}
}

// interpretForm reads a form-encoded body back into the JSON the server will
// see once it has parsed each field to the type its schema declares — the
// same coercion a parameter gets, member by member.
func interpretForm(schema generate.Schema, text string) []string {
	values, err := url.ParseQuery(text)
	if err != nil {
		return nil
	}
	fields := map[string]string{}
	for name := range values {
		fields[name] = values.Get(name)
	}
	return interpretFields(schema, fields)
}

// interpretMultipart does the same for a multipart body, reading each part's
// name and content back out of the fixed-boundary layout formBody wrote.
func interpretMultipart(schema generate.Schema, text string) []string {
	fields := map[string]string{}
	for _, part := range strings.Split(text, "--"+multipartBoundary) {
		part = strings.TrimPrefix(part, "\r\n")
		if part == "" || strings.HasPrefix(part, "--") {
			continue
		}
		header, content, found := strings.Cut(part, "\r\n\r\n")
		if !found {
			continue
		}
		_, after, found := strings.Cut(header, `name="`)
		if !found {
			continue
		}
		name, _, _ := strings.Cut(after, `"`)
		fields[name] = strings.TrimSuffix(content, "\r\n")
	}
	return interpretFields(schema, fields)
}

// interpretFields coerces each field's text to the type its property schema
// declares and returns the JSON object the server will judge, so the same
// validator that judges the response can say whether the request was the
// valid or invalid one it was drawn to be.
func interpretFields(schema generate.Schema, fields map[string]string) []string {
	properties, _ := schema["properties"].(map[string]any)
	object := make(map[string]any, len(fields))
	for name, text := range fields {
		property, _ := properties[name].(map[string]any)
		types := declaredTypes(generate.Schema(property))
		if len(types) == 0 {
			object[name] = text
			continue
		}
		// The first declared type is how the server will read it; a value
		// that does not parse as that type stays text, which is what the
		// schema will then reject — the same rule coerce applies.
		reading, ok := coerce(types[0], text)
		if !ok {
			return nil
		}
		object[name] = reading
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil
	}
	return []string{string(encoded)}
}
