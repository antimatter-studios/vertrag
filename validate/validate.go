// Package validate decides whether a response matches what the API description
// promised.
//
// It is a port of Gavel, the library Dredd uses for the same job, and its
// judgements are what turn into a passing or failing test. The rules are looser
// than they first look, deliberately so:
//
//   - A status code must match exactly.
//   - Expected headers must be present; their values are not compared. A
//     description says which headers exist, not what they will contain.
//   - A JSON body is checked for the presence of the keys the expected body
//     has, and nothing else — neither values nor types. The expected body is an
//     example, not a fixture, so demanding it byte for byte would fail every
//     server that returns real data.
//   - A JSON body IS checked properly when the description supplied a schema,
//     which is the only place it says what the values must look like.
//   - A non-JSON body must match exactly, since there is no structure to
//     compare instead.
package validate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Message is one side of an exchange, as the runner records it.
type Message struct {
	StatusCode string
	Headers    map[string]string
	Body       string
	// BodySchema is the JSON Schema the description attached to the response,
	// when it had one. It turns body checking from key presence into real
	// validation.
	BodySchema json.RawMessage
}

// FieldResult is the verdict on one part of the response.
//
// Kind names how the comparison was made — "text" or "json". It is a pointer so
// that "not compared at all" serialises as null rather than as an empty string:
// a body that could not be parsed was never compared by either method, and
// saying so is different from saying it was compared as text.
type FieldResult struct {
	Valid  bool     `json:"valid"`
	Kind   *string  `json:"kind"`
	Errors []string `json:"errors"`
}

func kind(name string) *string { return &name }

// Result is the verdict on a whole response.
type Result struct {
	Valid  bool                   `json:"valid"`
	Fields map[string]FieldResult `json:"fields"`
}

// Validate compares a real response against the expected one.
func Validate(expected, real Message) Result {
	result := Result{Valid: true, Fields: map[string]FieldResult{}}

	result.add("statusCode", validateStatusCode(expected.StatusCode, real.StatusCode))

	if len(expected.Headers) > 0 {
		result.add("headers", validateHeaders(expected.Headers, real.Headers))
	}

	// With nothing expected and no schema, there is nothing to say about the
	// body, and the field is left out entirely rather than reported as trivially
	// valid — which is what the reference does.
	if expected.Body != "" || len(expected.BodySchema) > 0 {
		result.add("body", validateBody(expected, real))
	}

	return result
}

func (r *Result) add(name string, field FieldResult) {
	r.Fields[name] = field
	if !field.Valid {
		r.Valid = false
	}
}

func validateStatusCode(expected, real string) FieldResult {
	field := FieldResult{Valid: true, Kind: kind("text"), Errors: []string{}}
	if strings.TrimSpace(expected) != strings.TrimSpace(real) {
		field.Valid = false
		field.Errors = append(field.Errors,
			fmt.Sprintf("Expected status code '%s', but got '%s'.", expected, real))
	}
	return field
}

// validateHeaders reports the expected headers the response did not carry.
//
// Only presence is checked. Header names are compared case-insensitively,
// because HTTP does not distinguish them.
func validateHeaders(expected, real map[string]string) FieldResult {
	field := FieldResult{Valid: true, Kind: kind("json"), Errors: []string{}}

	present := make(map[string]bool, len(real))
	for name := range real {
		present[strings.ToLower(name)] = true
	}

	names := make([]string, 0, len(expected))
	for name := range expected {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if !present[strings.ToLower(name)] {
			field.Valid = false
			field.Errors = append(field.Errors,
				fmt.Sprintf("At '/%s' Missing required property: %s", strings.ToLower(name), strings.ToLower(name)))
		}
	}
	return field
}

// validateBody checks the response body against the schema when there is one,
// and against the shape of the expected body when there is not.
func validateBody(expected, real Message) FieldResult {
	if len(expected.BodySchema) > 0 {
		return validateAgainstSchema(expected.BodySchema, real.Body)
	}

	if !isJSONContent(expected.Headers) && !looksLikeJSON(expected.Body) {
		return validateText(expected.Body, real.Body)
	}

	// A JSON expected body is turned into "these keys must exist" and used as
	// the schema, which is exactly how the reference treats it.
	schema, err := inferSchema(expected.Body)
	if err != nil {
		return validateText(expected.Body, real.Body)
	}
	return validateAgainstSchema(schema, real.Body)
}

func validateText(expected, real string) FieldResult {
	field := FieldResult{Valid: true, Kind: kind("text"), Errors: []string{}}
	if expected != real {
		field.Valid = false
		field.Errors = append(field.Errors, "Actual and expected data do not match.")
	}
	return field
}

func isJSONContent(headers map[string]string) bool {
	for name, value := range headers {
		if strings.EqualFold(name, "content-type") {
			base := value
			if i := strings.IndexByte(base, ';'); i >= 0 {
				base = base[:i]
			}
			base = strings.ToLower(strings.TrimSpace(base))
			return base == "application/json" || strings.HasSuffix(base, "+json")
		}
	}
	return false
}

func looksLikeJSON(body string) bool {
	trimmed := strings.TrimSpace(body)
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}

// inferSchema builds a JSON Schema from an example body.
//
// Every key present becomes required, and nothing else is asserted: no types,
// no values, and additional keys are allowed. That is the whole of the guarantee
// a body without a schema gives.
func inferSchema(body string) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		return nil, err
	}
	schema := inferSchemaValue(value)
	return json.Marshal(schema)
}

func inferSchemaValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		properties := map[string]any{}
		required := make([]string, 0, len(v))
		for key, nested := range v {
			properties[key] = inferSchemaValue(nested)
			required = append(required, key)
		}
		sort.Strings(required)

		schema := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema

	case []any:
		return map[string]any{"type": "array"}

	default:
		// A leaf constrains nothing: the example's value is an illustration, and
		// even its type is not a promise.
		return map[string]any{}
	}
}
