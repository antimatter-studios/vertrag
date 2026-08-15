package validate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStatusCode(t *testing.T) {
	for _, test := range []struct {
		name     string
		expected string
		real     string
		valid    bool
	}{
		{"identical", "200", "200", true},
		{"different", "200", "404", false},
		{"surrounding whitespace is ignored", " 200 ", "200", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := Validate(Message{StatusCode: test.expected}, Message{StatusCode: test.real})
			if result.Valid != test.valid {
				t.Fatalf("Valid = %v, want %v", result.Valid, test.valid)
			}
			if !test.valid {
				want := "Expected status code '" + test.expected + "', but got '" + test.real + "'."
				if got := result.Fields["statusCode"].Errors[0]; got != want {
					t.Errorf("error = %q, want %q", got, want)
				}
			}
		})
	}
}

// TestHeadersCheckPresenceOnly pins the rule that a description says which
// headers exist, not what they contain.
func TestHeadersCheckPresenceOnly(t *testing.T) {
	expected := Message{StatusCode: "200", Headers: map[string]string{"X-A": "1"}}

	if result := Validate(expected, Message{StatusCode: "200", Headers: map[string]string{"X-A": "totally different"}}); !result.Valid {
		t.Error("a differing header value should not fail; only presence is checked")
	}

	if result := Validate(expected, Message{StatusCode: "200", Headers: map[string]string{"x-a": "1"}}); !result.Valid {
		t.Error("header names are case-insensitive in HTTP and must be compared that way")
	}

	result := Validate(expected, Message{StatusCode: "200", Headers: map[string]string{}})
	if result.Valid {
		t.Fatal("a missing header should fail")
	}
	if got, want := result.Fields["headers"].Errors[0], "At '/x-a' Missing required property: x-a"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

// TestBodyWithoutSchemaChecksKeysOnly pins the most surprising rule in the
// package: an expected body is an example, so only the presence of its keys is
// asserted — not values, not even types.
func TestBodyWithoutSchemaChecksKeysOnly(t *testing.T) {
	json := map[string]string{"content-type": "application/json"}
	expected := Message{StatusCode: "200", Headers: json, Body: `{"a":1,"b":{"c":2}}`}

	for _, test := range []struct {
		name  string
		body  string
		valid bool
	}{
		{"same shape, different values", `{"a":999,"b":{"c":0}}`, true},
		{"same shape, different types", `{"a":"text","b":{"c":true}}`, true},
		{"additional keys are allowed", `{"a":1,"b":{"c":2},"extra":3}`, true},
		{"a missing top-level key fails", `{"b":{"c":2}}`, false},
		{"a missing nested key fails", `{"a":1,"b":{}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := Validate(expected, Message{StatusCode: "200", Headers: json, Body: test.body})
			if result.Valid != test.valid {
				t.Errorf("Valid = %v, want %v (errors: %v)",
					result.Valid, test.valid, result.Fields["body"].Errors)
			}
		})
	}
}

// TestBodyWithSchemaIsCheckedProperly is the other half of the rule: where the
// description supplied a schema, values ARE checked.
func TestBodyWithSchemaIsCheckedProperly(t *testing.T) {
	headers := map[string]string{"content-type": "application/json"}
	expected := Message{
		StatusCode: "200",
		Headers:    headers,
		BodySchema: json.RawMessage(`{"type":"object","required":["a"],"properties":{"a":{"type":"number"}}}`),
	}

	if result := Validate(expected, Message{StatusCode: "200", Headers: headers, Body: `{"a":1}`}); !result.Valid {
		t.Errorf("a conforming body should pass: %v", result.Fields["body"].Errors)
	}

	result := Validate(expected, Message{StatusCode: "200", Headers: headers, Body: `{"a":"no"}`})
	if result.Valid {
		t.Fatal("a body contradicting the schema should fail")
	}
	if got, want := result.Fields["body"].Errors[0], "At '/a' Invalid type: string (expected number)"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

func TestTextBodyMustMatchExactly(t *testing.T) {
	headers := map[string]string{"content-type": "text/plain"}
	expected := Message{StatusCode: "200", Headers: headers, Body: "hello"}

	if result := Validate(expected, Message{StatusCode: "200", Headers: headers, Body: "hello"}); !result.Valid {
		t.Error("identical text should pass")
	}

	result := Validate(expected, Message{StatusCode: "200", Headers: headers, Body: "world"})
	if result.Valid {
		t.Fatal("differing text should fail")
	}
	if got, want := result.Fields["body"].Errors[0], "Actual and expected data do not match."; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

// TestBodyFieldAbsentWhenNothingExpected pins the difference between "compared
// and passed" and "never compared".
func TestBodyFieldAbsentWhenNothingExpected(t *testing.T) {
	result := Validate(Message{StatusCode: "200"}, Message{StatusCode: "200", Body: `{"a":1}`})
	if !result.Valid {
		t.Fatal("a response body nothing asked about should not fail")
	}
	if _, present := result.Fields["body"]; present {
		t.Error("the body field should be absent entirely, not reported as trivially valid")
	}
}

// TestUnparseableJSONBody pins the distinct failure for a body that claims to
// be JSON and is not, including the null kind.
func TestUnparseableJSONBody(t *testing.T) {
	headers := map[string]string{"content-type": "application/json"}
	result := Validate(
		Message{StatusCode: "200", Headers: headers, Body: `{"a":1}`},
		Message{StatusCode: "200", Headers: headers, Body: "not json"},
	)

	if result.Valid {
		t.Fatal("an unparseable body should fail")
	}
	field := result.Fields["body"]
	if field.Kind != nil {
		t.Errorf("kind = %q, want null: nothing was compared", *field.Kind)
	}
	if !strings.Contains(field.Errors[0], "is not a parseable JSON") {
		t.Errorf("error = %q, want it to name the parse failure", field.Errors[0])
	}
	if !strings.Contains(field.Errors[0], `Unexpected token 'o', "not json" is not valid JSON`) {
		t.Errorf("error = %q, want the JavaScript engine's own wording", field.Errors[0])
	}
}

func TestJavaScriptJSONError(t *testing.T) {
	for _, test := range []struct {
		body string
		want string
	}{
		{"", "Unexpected end of JSON input"},
		{"   ", "Unexpected end of JSON input"},
		{"not json", `Unexpected token 'o', "not json" is not valid JSON`},
		{"{oops}", `Unexpected token '{', "{oops}" is not valid JSON`},
	} {
		if got := javaScriptJSONError(test.body); got != test.want {
			t.Errorf("javaScriptJSONError(%q) = %q, want %q", test.body, got, test.want)
		}
	}
}
