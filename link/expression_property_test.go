package link

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// TestAPointerFindsWhatItPointsAt checks the evaluator against a document it
// was given rather than against a table.
//
// A pointer is built alongside the document that satisfies it, so whatever
// shape is drawn — nested objects, arrays, keys containing the very characters
// RFC 6901 escapes — the value at the end is known independently of the code
// under test.
//
// The failure this guards against is quiet. An expression that resolves to the
// wrong value does not error: it fills a link's parameter with something
// plausible, the request goes somewhere unintended, and the failure that
// follows points at the wrong operation entirely.
func TestAPointerFindsWhatItPointsAt(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		document, pointer, want := drawDocumentAndPointer(rt, 0)

		encoded, err := json.Marshal(document)
		if err != nil {
			rt.Fatalf("document does not encode: %v", err)
		}

		got, ok := Evaluate("$response.body#"+pointer, Exchange{ResponseBody: string(encoded)})
		if !ok {
			rt.Fatalf("pointer %q did not resolve against %s", pointer, encoded)
		}

		// Compared as JSON, because a number arrives as a float64 whatever it
		// was written as and comparing the Go values would be comparing
		// encodings rather than content.
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			rt.Fatalf("pointer %q gave %s, want %s (document %s)",
				pointer, gotJSON, wantJSON, encoded)
		}
	})
}

// drawDocumentAndPointer builds a document, a pointer into it, and the value
// that pointer names.
func drawDocumentAndPointer(t *rapid.T, depth int) (document any, pointer string, target any) {
	if depth >= 3 || rapid.IntRange(0, 3).Draw(t, "leaf") == 0 {
		leaf := drawLeaf(t)
		return leaf, "", leaf
	}

	if rapid.Bool().Draw(t, "is-array") {
		length := rapid.IntRange(1, 3).Draw(t, "length")
		at := rapid.IntRange(0, length-1).Draw(t, "index")

		items := make([]any, 0, length)
		var chosen, nested any
		var rest string
		for i := 0; i < length; i++ {
			if i == at {
				nested, rest, chosen = drawDocumentAndPointer(t, depth+1)
				items = append(items, nested)
				continue
			}
			items = append(items, drawLeaf(t))
		}
		return items, "/" + strconv.Itoa(at) + rest, chosen
	}

	// Keys deliberately include the two characters RFC 6901 escapes, because
	// undoing the escapes in the wrong order turns "~01" into "/" where the
	// document means a literal "~1".
	key := rapid.SampledFrom([]string{"a", "b", "a/b", "c~d", "~0", "~1", "x.y", "with space"}).
		Draw(t, "key")

	nested, rest, chosen := drawDocumentAndPointer(t, depth+1)
	object := map[string]any{key: nested, "other": drawLeaf(t)}

	return object, "/" + escapeToken(key) + rest, chosen
}

func drawLeaf(t *rapid.T) any {
	switch rapid.IntRange(0, 4).Draw(t, "leaf-kind") {
	case 0:
		return rapid.SampledFrom([]string{"", "x", "hello", "café"}).Draw(t, "string")
	case 1:
		return float64(rapid.IntRange(-1000, 1000).Draw(t, "number"))
	case 2:
		return rapid.Bool().Draw(t, "bool")
	case 3:
		return nil
	default:
		return map[string]any{}
	}
}

// escapeToken writes a key the way RFC 6901 requires, tilde first.
func escapeToken(key string) string {
	key = strings.ReplaceAll(key, "~", "~0")
	return strings.ReplaceAll(key, "/", "~1")
}

// TestEvaluateNeverPanics covers expressions nobody meant to write, which a
// description contains as readily as the ones somebody did.
//
// An evaluator that panics takes the run with it, and the expression came from
// a document vertrag was asked to read — so the crash lands on the person least
// able to do anything about it.
func TestEvaluateNeverPanics(t *testing.T) {
	exchange := Exchange{
		ResponseBody:   `{"a":{"b":[1,2,{"c":"d"}]}}`,
		RequestHeader:  map[string]string{"X-A": "1"},
		RequestQuery:   map[string]string{"q": "2"},
		RequestPath:    map[string]string{"p": "3"},
		ResponseHeader: map[string]string{"Location": "/x"},
	}

	for _, expression := range []string{
		"", "$", "$response", "$response.", "$response.body#", "$response.body##",
		"$response.body#/", "$response.body#//", "$response.body#/a/b/2/c/d/e",
		"$response.body#/~", "$response.body#/~2", "$response.body#/-1",
		"$response.body#/999999999999999999999",
		"$request.header.", "$request.query.", "$request.path.",
		"{$}", "{$response.body#/a}", "{", "}", "{$", "$}",
		"{$response.body#/a}{$response.body#/a}",
		strings.Repeat("{$response.body#/a}", 50),
	} {
		// The assertion is reaching the next iteration.
		Evaluate(expression, exchange)
	}
}
