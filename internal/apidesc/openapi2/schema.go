package openapi2

import (
	"encoding/json"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// swaggerNumberSample is the value a numeric schema demonstrates when the
// document offers nothing better.
//
// It is not a plausible number, and that is the point: the reference uses it so
// a generated example is obviously a placeholder rather than something a reader
// might mistake for real data. Reproduced because it appears in request bodies
// Dredd sends.
const swaggerNumberSample = -100000000

// jsonSchemaDraft is the dialect the reference declares its output in.
const jsonSchemaDraft = "http://json-schema.org/draft-04/schema#"

// generateValue builds the sample value a message body is rendered from.
//
// Swagger's rules are simpler than OpenAPI 3's: every property of an object is
// included whether required or not, and an array always yields one specimen
// item. Only a falsy result — the empty string from a bare string schema —
// leads to no body at all.
func (d *document) generateValue(schema node, seen map[string]bool) (any, bool) {
	schema = d.Resolve(schema)
	if !schema.Valid() {
		return nil, false
	}

	if ref := schema.Get("$ref"); ref.IsScalar() {
		if seen[ref.Value] {
			return nil, false
		}
		next := map[string]bool{ref.Value: true}
		for k := range seen {
			next[k] = true
		}
		return d.generateValue(d.Pointer(ref.Value), next)
	}

	if value, ok := schemaExample(schema); ok {
		return value, true
	}

	switch schema.Get("type").Str() {
	case "string":
		return "", true
	case "integer", "number":
		return float64(swaggerNumberSample), true
	case "boolean":
		return false, true
	case "array":
		items := schema.Get("items")
		if !items.Valid() {
			return []any{}, true
		}
		value, ok := d.generateValue(items, seen)
		if !ok {
			return []any{}, true
		}
		return []any{value}, true
	case "object":
		return d.generateObject(schema, seen), true
	default:
		// An untyped schema describing properties is an object in all but name,
		// and Swagger documents rely on that far more than OpenAPI 3 ones do.
		if schema.Get("properties").IsMapping() {
			return d.generateObject(schema, seen), true
		}
		return "", true
	}
}

func (d *document) generateObject(schema node, seen map[string]bool) *orderedMap {
	out := newOrderedMap()
	for _, property := range schema.Get("properties").Entries() {
		if value, ok := d.generateValue(property.Value, seen); ok {
			out.Set(property.Key.Str(), value)
		}
	}
	return out
}

// schemaExample derives the value a schema or parameter demonstrates outright.
//
// `x-example` comes first because Swagger has no `example` keyword for
// parameters — the extension is how a document says what to send, and Dredd
// honours it above the `default`, which describes what the server assumes when
// nothing is sent rather than what a test should send.
func schemaExample(schema node) (any, bool) {
	if !schema.Valid() {
		return nil, false
	}
	if example := schema.Get("x-example"); example.Valid() {
		return scalarValue(example), true
	}
	if example := schema.Get("example"); example.Valid() {
		return scalarValue(example), true
	}
	if def := schema.Get("default"); def.Valid() {
		return scalarValue(def), true
	}
	if enum := schema.Get("enum"); enum.IsSequence() {
		if items := enum.Items(); len(items) > 0 {
			return scalarValue(items[0]), true
		}
	}
	return nil, false
}

// renderBody serialises a generated value into a message body.
//
// A string is sent as itself rather than as a quoted JSON string: a schema of
// `type: string` describes a body that IS the text, and quoting it would send
// two extra characters the server never agreed to.
//
// Everything else is pretty-printed with two-space indentation, unlike
// OpenAPI 3's compact output. That is visible in every request Dredd sends from
// a Swagger document, so it is reproduced rather than tidied.
func renderBody(value any) (string, bool) {
	if !truthy(value) {
		return "", false
	}
	if text, ok := value.(string); ok {
		return text, true
	}
	encoded, err := marshalJSONIndent(value)
	if err != nil {
		return "", false
	}
	return encoded, true
}

func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case float64:
		return v != 0
	case bool:
		return v
	default:
		return true
	}
}

// convertSchema turns a Swagger schema into the JSON Schema a consumer
// validates a body against.
//
// Swagger schemas are already JSON Schema, so this mostly gathers the
// definitions a reference points at rather than rewriting anything.
func (d *document) convertSchema(schema node) (string, bool) {
	if !schema.IsMapping() {
		return "", false
	}

	var references []string
	result, ok := d.convertSubSchema(schema, &references)
	if !ok {
		return "", false
	}

	result.Set("$schema", jsonSchemaDraft)

	if len(references) > 0 {
		definitions := newOrderedMap()
		result.Set("definitions", definitions)

		for len(references) > 0 {
			reference := references[len(references)-1]
			references = references[:len(references)-1]

			id := referenceID(reference)
			if id == "" || definitions.Has(id) {
				continue
			}
			definitions.Set(id, newOrderedMap())
			if converted, ok := d.convertSubSchema(d.Pointer(reference), &references); ok {
				definitions.Set(id, converted)
			}
		}
	}

	// A schema that is only a reference stands in for its target. Draft-04
	// makes a validator ignore anything beside a $ref, so carrying the
	// definitions along would leave a schema that validates nothing.
	if ref, isRef := result.Get("$ref"); isRef {
		definitions, _ := result.Get("definitions")
		if definitionMap, ok := definitions.(*orderedMap); ok {
			if target, ok := definitionMap.Get(referenceID(ref.(string))); ok {
				if targetMap, ok := target.(*orderedMap); ok && !schemaHasReferences(targetMap) {
					encoded, err := marshalJSON(targetMap)
					return encoded, err == nil
				}
			}
		}
	}

	encoded, err := marshalJSON(result)
	if err != nil {
		return "", false
	}
	return encoded, true
}

// schemaHasReferences reports whether a converted schema still points at a
// definition, which decides whether it can stand alone.
func schemaHasReferences(value any) bool {
	switch v := value.(type) {
	case *orderedMap:
		if v.Has("$ref") {
			return true
		}
		for _, key := range v.Keys() {
			nested, _ := v.Get(key)
			if schemaHasReferences(nested) {
				return true
			}
		}
		return false
	case []any:
		for _, item := range v {
			if schemaHasReferences(item) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func (d *document) convertSubSchema(schema node, references *[]string) (*orderedMap, bool) {
	if !schema.IsMapping() {
		return nil, false
	}

	if ref := schema.Get("$ref"); ref.IsScalar() {
		*references = append(*references, ref.Value)
		out := newOrderedMap()
		out.Set("$ref", localReference(ref.Value))
		return out, true
	}

	out := newOrderedMap()
	for _, member := range schema.Entries() {
		key := member.Key.Str()
		switch key {
		case "discriminator", "readOnly", "xml", "externalDocs", "example":
			continue

		case "properties", "patternProperties":
			properties := newOrderedMap()
			for _, property := range member.Value.Entries() {
				if converted, ok := d.convertSubSchema(property.Value, references); ok {
					properties.Set(property.Key.Str(), converted)
				}
			}
			out.Set(key, properties)

		case "items", "not", "additionalProperties", "additionalItems":
			if member.Value.IsMapping() {
				if converted, ok := d.convertSubSchema(member.Value, references); ok {
					out.Set(key, converted)
				}
				continue
			}
			out.Set(key, scalarValue(member.Value))

		case "allOf", "anyOf", "oneOf":
			var list []any
			for _, item := range member.Value.Items() {
				if converted, ok := d.convertSubSchema(item, references); ok {
					list = append(list, converted)
				}
			}
			out.Set(key, list)

		case "type":
			// Swagger's `file` type has no JSON Schema equivalent.
			if member.Value.Str() == "file" {
				out.Set(key, "string")
				continue
			}
			out.Set(key, scalarValue(member.Value))

		default:
			if strings.HasPrefix(key, "x-") {
				continue
			}
			out.Set(key, scalarValue(member.Value))
		}
	}
	return out, true
}

func localReference(reference string) string {
	return "#/definitions/" + referenceID(reference)
}

func referenceID(reference string) string {
	parts := strings.Split(reference, "/")
	return parts[len(parts)-1]
}

// scalarValue converts a YAML node to a plain Go value.
func scalarValue(n node) any {
	if !n.Valid() {
		return nil
	}
	switch n.Kind {
	case yaml.ScalarNode:
		return scalarFromTag(n)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, item := range n.Items() {
			out = append(out, scalarValue(item))
		}
		return out
	case yaml.MappingNode:
		out := newOrderedMap()
		for _, member := range n.Entries() {
			out.Set(member.Key.Str(), scalarValue(member.Value))
		}
		return out
	default:
		return nil
	}
}

func scalarFromTag(n node) any {
	switch n.Tag {
	case "!!int", "!!float":
		if v, err := strconv.ParseFloat(n.Value, 64); err == nil {
			return v
		}
	case "!!bool":
		return n.Value == "true"
	case "!!null":
		return nil
	}
	return n.Value
}

func stringifyScalar(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case nil:
		return ""
	default:
		encoded, err := marshalJSON(v)
		if err != nil {
			return ""
		}
		return encoded
	}
}

func marshalJSON(value any) (string, error) {
	var buf strings.Builder
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func marshalJSONIndent(value any) (string, error) {
	var buf strings.Builder
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}
