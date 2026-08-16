package validate

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// A JSON Schema draft-04 validator, limited to the keywords the reference's own
// validator reports on, and producing its message wording.
//
// The messages are not incidental. They are what a failing Dredd run prints, so
// a user comparing vertrag's output against Dredd's is comparing these strings.
// They follow tv4, the library underneath Gavel.

func validateAgainstSchema(schema json.RawMessage, body string) FieldResult {
	field := FieldResult{Valid: true, Kind: kind("json"), Errors: []string{}}

	var document any
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		// A body that claims to be JSON but will not parse is reported as its
		// own kind of failure, with no kind at all: there is no structure to
		// have compared, so calling the result a JSON comparison would be a
		// lie about what was checked.
		field.Valid = false
		field.Kind = nil
		field.Errors = append(field.Errors, fmt.Sprintf(
			"Can't validate: actual body 'Content-Type' header is 'application/json' but body is not a parseable JSON:\n%s",
			javaScriptJSONError(body)))
		return field
	}

	var parsed any
	if err := json.Unmarshal(schema, &parsed); err != nil {
		return field
	}

	errors := checkSchema(parsed, document, "")
	if len(errors) > 0 {
		field.Valid = false
		field.Errors = append(field.Errors, errors...)
	}
	return field
}

// checkSchema validates a value, returning messages already prefixed with the
// JSON pointer they occurred at.
func checkSchema(schema, value any, pointer string) []string {
	rules, ok := schema.(map[string]any)
	if !ok {
		// `true` and `false` schemas: the first accepts anything, the second
		// nothing. A non-object that is not a boolean is not a schema at all.
		if allow, isBool := schema.(bool); isBool && !allow {
			return []string{at(pointer, "Invalid value")}
		}
		return nil
	}

	var errors []string

	if declared, ok := rules["type"]; ok {
		if message := checkType(declared, value); message != "" {
			// A value of the wrong type cannot be examined further; reporting
			// its missing properties as well would be noise about a value that
			// was never the right shape.
			return []string{at(pointer, message)}
		}
	}

	if enum, ok := rules["enum"].([]any); ok {
		if !containsValue(enum, value) {
			errors = append(errors, at(pointer, "No enum match for: "+render(value)))
		}
	}

	object, isObject := value.(map[string]any)

	if required, ok := rules["required"].([]any); ok && isObject {
		names := make([]string, 0, len(required))
		for _, entry := range required {
			if name, ok := entry.(string); ok {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			if _, present := object[name]; !present {
				errors = append(errors,
					at(join(pointer, name), "Missing required property: "+name))
			}
		}
	}

	if properties, ok := rules["properties"].(map[string]any); ok && isObject {
		names := make([]string, 0, len(properties))
		for name := range properties {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			nested, present := object[name]
			if !present {
				continue
			}
			errors = append(errors, checkSchema(properties[name], nested, join(pointer, name))...)
		}
	}

	if items, ok := rules["items"]; ok {
		if list, isList := value.([]any); isList {
			for i, item := range list {
				errors = append(errors, checkSchema(items, item, fmt.Sprintf("%s/%d", pointer, i))...)
			}
		}
	}

	return errors
}

// checkType reports a type mismatch, or "" when the value is acceptable.
func checkType(declared, value any) string {
	switch expected := declared.(type) {
	case string:
		if matchesType(expected, value) {
			return ""
		}
		return fmt.Sprintf("Invalid type: %s (expected %s)", typeName(value), expected)

	case []any:
		names := make([]string, 0, len(expected))
		for _, entry := range expected {
			name, ok := entry.(string)
			if !ok {
				continue
			}
			if matchesType(name, value) {
				return ""
			}
			names = append(names, name)
		}
		return fmt.Sprintf("Invalid type: %s (expected %s)", typeName(value), strings.Join(names, "/"))

	default:
		return ""
	}
}

func matchesType(expected string, value any) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		// JSON has one number type, so an integer is a number that happens to
		// have no fractional part.
		number, ok := value.(float64)
		return ok && number == math.Trunc(number)
	default:
		return true
	}
}

func typeName(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64:
		// Always "number", never "integer". JSON has one numeric type, and the
		// reference names the value's actual type by that type — `integer`
		// exists only as something a schema can ask FOR, never as something a
		// value IS.
		_ = v
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func containsValue(values []any, target any) bool {
	rendered := render(target)
	for _, value := range values {
		if render(value) == rendered {
			return true
		}
	}
	return false
}

func render(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}

// javaScriptJSONError reproduces the message a JavaScript engine gives for an
// unparseable JSON document.
//
// The text reaches the user through Dredd's output, so it is reproduced rather
// than replaced with Go's own wording. Only the two common shapes are covered;
// anything else falls back to the leading-token form, which is what a body that
// is not JSON at all almost always produces.
func javaScriptJSONError(body string) string {
	if strings.TrimSpace(body) == "" {
		return "Unexpected end of JSON input"
	}

	trimmed := strings.TrimLeft(body, " \t\r\n")
	first, _ := utf8DecodeFirst(trimmed)

	// The engine quotes the offending token, not the first character: for bare
	// words it reports the second character, having consumed the first while
	// trying to read a literal such as `null` or `true`.
	offending := first
	if len(trimmed) > 1 && isWordStart(first) {
		offending, _ = utf8DecodeFirst(trimmed[1:])
	}

	return fmt.Sprintf("Unexpected token '%s', %s is not valid JSON", string(offending), render(body))
}

func utf8DecodeFirst(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}

func isWordStart(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// at prefixes a message with the location it applies to.
func at(pointer, message string) string {
	if pointer == "" {
		pointer = "/"
	}
	return fmt.Sprintf("At '%s' %s", pointer, message)
}

func join(pointer, name string) string {
	return pointer + "/" + escapePointer(name)
}

// escapePointer encodes the two characters JSON Pointer reserves.
func escapePointer(name string) string {
	name = strings.ReplaceAll(name, "~", "~0")
	return strings.ReplaceAll(name, "/", "~1")
}
