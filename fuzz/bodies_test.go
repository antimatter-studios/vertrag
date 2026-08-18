package fuzz

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/generate"
)

var formSchema = generate.Schema{
	"type": "object",
	"properties": map[string]any{
		"name": map[string]any{"type": "string"},
		"age":  map[string]any{"type": "integer", "minimum": 0},
		"vip":  map[string]any{"type": "boolean"},
	},
}

// TestFormBodyRendersAndReadsBack: the wire text is form-encoded, and reading
// it back through the schema recovers the value the server will judge — an
// integer as an integer, not the digits as text — so the same validator that
// judges the response can say whether the request was the one drawn.
func TestFormBodyRendersAndReadsBack(t *testing.T) {
	form := formBody(formSchema)
	rendered, ok := form.render(map[string]any{"name": "ann", "age": 7, "vip": true})
	if !ok {
		t.Fatal("an object should render as a form")
	}
	text := rendered.(string)
	for _, want := range []string{"age=7", "name=ann", "vip=true"} {
		if !strings.Contains(text, want) {
			t.Errorf("form body lacks %q: %s", want, text)
		}
	}
	readings := form.interpret(rendered)
	if len(readings) != 1 || !strings.Contains(readings[0], `"age":7`) || !strings.Contains(readings[0], `"vip":true`) {
		t.Errorf("interpret = %v, want the typed object back", readings)
	}
}

// TestMultipartBodyRendersAndReadsBack does the same for multipart, with the
// fixed boundary that makes a finding replay byte for byte.
func TestMultipartBodyRendersAndReadsBack(t *testing.T) {
	form := multipartBody(formSchema)
	rendered, ok := form.render(map[string]any{"name": "ann", "age": 7})
	if !ok {
		t.Fatal("an object should render as multipart")
	}
	text := rendered.(string)
	if !strings.Contains(text, "--"+multipartBoundary+"\r\n") || !strings.Contains(text, `name="age"`) {
		t.Errorf("not a multipart body:\n%s", text)
	}
	if !strings.HasSuffix(text, "--"+multipartBoundary+"--\r\n") {
		t.Errorf("multipart body is not closed:\n%s", text)
	}
	readings := form.interpret(rendered)
	if len(readings) != 1 || !strings.Contains(readings[0], `"age":7`) {
		t.Errorf("interpret = %v, want the typed object back", readings)
	}
}

// TestNonObjectsHaveNoFormShape: a generator that drew a string to violate an
// object schema has drawn something these media types cannot carry; the case
// is abandoned rather than sent as nonsense and blamed on the server.
func TestNonObjectsHaveNoFormShape(t *testing.T) {
	for _, form := range []wire{formBody(formSchema), multipartBody(formSchema)} {
		if _, ok := form.render("vertrag-not-an-object"); ok {
			t.Error("a scalar rendered as a form body")
		}
		if _, ok := form.render(map[string]any{"nested": map[string]any{"a": 1}}); ok {
			t.Error("a nested object has no agreed form-encoded spelling and should not render")
		}
	}
}

// TestBodyFormByMediaType pins the dispatch, including the media types
// generation refuses rather than guesses at.
func TestBodyFormByMediaType(t *testing.T) {
	for media, want := range map[string]bool{
		"":                                  true,
		"application/json":                  true,
		"application/vnd.api+json":          true,
		"application/x-www-form-urlencoded": true,
		"multipart/form-data":               true,
		"text/plain":                        false,
		"application/xml":                   false,
		"application/octet-stream":          false,
	} {
		if _, ok := BodyForm(media, formSchema); ok != want {
			t.Errorf("BodyForm(%q) ok = %v, want %v", media, ok, want)
		}
	}
}
