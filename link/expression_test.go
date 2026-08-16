package link

import "testing"

// exchange is the specimen every case reads from, built once so a test says
// only what it is testing.
var exchange = Exchange{
	URL:           "https://api.example.com/users?limit=10",
	Method:        "POST",
	StatusCode:    "201",
	RequestPath:   map[string]string{"tenant": "acme"},
	RequestQuery:  map[string]string{"limit": "10", "Limit": "99"},
	RequestHeader: map[string]string{"X-Request-Id": "abc", "X-A.B": "dotted"},
	RequestBody:   `{"name":"widget"}`,
	ResponseHeader: map[string]string{
		"Location": "/users/42",
		"X-Total":  "7",
	},
	ResponseBody: `{"id":42,"nested":{"deep":["a","b"]},"od~d":1,"sl/ash":2,"ratio":1.5,"ok":true}`,
}

func TestEvaluate(t *testing.T) {
	for _, test := range []struct {
		expression string
		want       any
	}{
		{"$url", "https://api.example.com/users?limit=10"},
		{"$method", "POST"},
		{"$statusCode", "201"},

		{"$request.path.tenant", "acme"},
		{"$request.query.limit", "10"},
		{"$request.header.x-request-id", "abc"},
		{"$response.header.location", "/users/42"},

		{"$response.body#/id", float64(42)},
		{"$response.body#/nested/deep/1", "b"},
		{"$response.body#/ratio", 1.5},
		{"$response.body#/ok", true},

		// An empty pointer names the whole document, as does no pointer at all.
		{"$response.body#", map[string]any{}},
		{"$response.body", map[string]any{}},

		// A value that is not an expression is a literal and passes through.
		{"literal-value", "literal-value"},
	} {
		t.Run(test.expression, func(t *testing.T) {
			got, ok := Evaluate(test.expression, exchange)
			if !ok {
				t.Fatalf("Evaluate(%q) failed", test.expression)
			}
			if _, whole := test.want.(map[string]any); whole {
				// The two whole-document forms only need to have resolved to an
				// object; comparing every field would restate the fixture.
				if _, isObject := got.(map[string]any); !isObject {
					t.Errorf("got %T, want the whole body as an object", got)
				}
				return
			}
			if got != test.want {
				t.Errorf("Evaluate(%q) = %#v, want %#v", test.expression, got, test.want)
			}
		})
	}
}

// TestHeaderLookupIsCaseInsensitiveAndQueryIsNot pins a distinction the
// specification states explicitly and which is easy to lose by lowering
// everything.
//
// "The `name` identifier is case-sensitive, whereas `token` is not" — so a
// header is matched however it is cased, and a query parameter named `Limit` is
// a different parameter from `limit`.
func TestHeaderLookupIsCaseInsensitiveAndQueryIsNot(t *testing.T) {
	for _, spelling := range []string{
		"$request.header.x-request-id",
		"$request.header.X-Request-Id",
		"$request.header.X-REQUEST-ID",
	} {
		if got, ok := Evaluate(spelling, exchange); !ok || got != "abc" {
			t.Errorf("Evaluate(%q) = %v (ok=%v), want abc", spelling, got, ok)
		}
	}

	if got, _ := Evaluate("$request.query.limit", exchange); got != "10" {
		t.Errorf("query limit = %v, want 10", got)
	}
	if got, _ := Evaluate("$request.query.Limit", exchange); got != "99" {
		t.Errorf("query Limit = %v, want 99 — query names are case-sensitive", got)
	}
}

// TestHeaderNameMayContainDots pins that the split is on the first dot after
// `header`, not the last and not every one. tchar includes '.', '$' and '#'.
func TestHeaderNameMayContainDots(t *testing.T) {
	if got, ok := Evaluate("$request.header.x-a.b", exchange); !ok || got != "dotted" {
		t.Errorf("got %v (ok=%v), want dotted", got, ok)
	}
}

// TestPointerEscapes pins RFC 6901's two escapes and the order they are undone
// in. Undoing ~1 first would turn "~01" into "~1" and then into "/", where the
// document means a literal "~1".
func TestPointerEscapes(t *testing.T) {
	if got, ok := Evaluate("$response.body#/od~0d", exchange); !ok || got != float64(1) {
		t.Errorf("~0 should decode to a tilde: got %v (ok=%v)", got, ok)
	}
	if got, ok := Evaluate("$response.body#/sl~1ash", exchange); !ok || got != float64(2) {
		t.Errorf("~1 should decode to a slash: got %v (ok=%v)", got, ok)
	}
}

// TestEmbeddedExpressions pins the braced form, where the expression is part of
// a larger string and the result is therefore text.
func TestEmbeddedExpressions(t *testing.T) {
	for _, test := range []struct{ template, want string }{
		{"Bearer {$response.body#/id}", "Bearer 42"},
		{"/users/{$response.body#/id}/edit", "/users/42/edit"},
		{"{$method} {$statusCode}", "POST 201"},
		// A number reaches text as an identifier would be written, not as
		// 42.000000 and not in exponent form.
		{"{$response.body#/ratio}", "1.5"},
	} {
		got, ok := Evaluate(test.template, exchange)
		if !ok {
			t.Fatalf("Evaluate(%q) failed", test.template)
		}
		if got != test.want {
			t.Errorf("Evaluate(%q) = %v, want %q", test.template, got, test.want)
		}
	}
}

// TestUnresolvableExpressionsReportFailure is the property the plan depends on.
//
// A link that cannot be followed has to leave its target alone. Returning an
// empty string instead would send a blank where an identifier was meant to go,
// and the request would go somewhere the description never described — the same
// silent-wrong-destination class as a URI template that accepts a typo.
func TestUnresolvableExpressionsReportFailure(t *testing.T) {
	for _, expression := range []string{
		"$response.body#/missing",
		"$response.body#/nested/deep/99",
		"$response.body#/id/deeper",
		"$request.header.x-absent",
		"$request.query.absent",
		"$request.path.absent",
		"$response.body#missing-leading-slash",
		"Bearer {$response.body#/missing}",
		"unterminated {$response.body#/id",
	} {
		if value, ok := Evaluate(expression, exchange); ok {
			t.Errorf("Evaluate(%q) resolved to %#v, want failure", expression, value)
		}
	}
}

// TestUnparseableBodyIsNotAnError pins that a response which is not JSON simply
// yields no value. It may legitimately be a CSV or an image, and reporting that
// as a fault would blame the server for the description's optimism.
func TestUnparseableBodyIsNotAnError(t *testing.T) {
	notJSON := Exchange{ResponseBody: "id,name\n1,widget\n"}
	if value, ok := Evaluate("$response.body#/id", notJSON); ok {
		t.Errorf("resolved to %#v against a CSV body, want failure", value)
	}
}
