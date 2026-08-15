package openapi3

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/antimatter-studios/vertrag/internal/refract"
	"gopkg.in/yaml.v3"
)

// schemaElementName maps a Schema Object's declared type onto the API Elements
// primitive that represents it.
//
// A schema with no declared type becomes a bare "string": the reference treats
// an untyped schema as having no value to offer, and a string element with no
// content is how that is expressed.
func schemaElementName(schema node) string {
	if !schema.valid() {
		return "string"
	}
	switch schema.get("type").str() {
	case "integer", "number":
		return "number"
	case "boolean":
		return "boolean"
	case "array":
		return "array"
	case "object":
		return "object"
	default:
		return "string"
	}
}

// schemaExample derives the value a schema demonstrates.
//
// The order is the document's own preference: an explicit example, then a
// default, then the first allowed value of an enum.
func schemaExample(schema node) (any, bool) {
	if !schema.valid() {
		return nil, false
	}
	if example := schema.get("example"); example.valid() {
		return scalarValue(example), true
	}
	if def := schema.get("default"); def.valid() {
		return scalarValue(def), true
	}
	if enum := schema.get("enum"); enum.isSequence() {
		if items := enum.items(); len(items) > 0 {
			return scalarValue(items[0]), true
		}
	}
	return nil, false
}

// generateValue builds the sample value a message body is rendered from.
//
// It returns ok=false for "no value", which is not the same as an empty value:
// a property with no value is left out of the object entirely, and an array
// whose items have no value disappears rather than becoming [].
//
// The rules are the reference's, and several are surprising enough to be worth
// stating. An array of a plain primitive has no value at all, while an array of
// objects or of a referenced type yields one specimen item. A schema with no
// declared type yields the empty string, which downstream is falsy and so
// produces no body. A reference that leads back to itself has no value, which
// is what stops a recursive schema from generating forever.
func (d *document) generateValue(schema node, seen map[string]bool) (any, bool) {
	if !schema.valid() {
		return nil, false
	}

	// Follow a reference, refusing to re-enter one already being expanded.
	if ref := schema.get("$ref"); ref.isScalar() {
		if seen[ref.Value] {
			return nil, false
		}
		next := map[string]bool{ref.Value: true}
		for k := range seen {
			next[k] = true
		}
		return d.generateValue(d.pointer(ref.Value), next)
	}

	if value, ok := schemaExample(schema); ok {
		return value, true
	}

	// A nullable schema with nothing else to say demonstrates the null.
	if schema.get("nullable").boolValue() {
		return nil, true
	}

	switch schema.get("type").str() {
	case "string":
		return "", true
	case "integer", "number":
		return float64(0), true
	case "boolean":
		return false, true

	case "array":
		items := schema.get("items")
		// An array of a bare primitive has no value: the document has said
		// nothing about what would be in it, so there is no specimen to send.
		if isValuelessPrimitiveSchema(items) {
			return nil, false
		}
		value, ok := d.generateValue(items, seen)
		if !ok {
			return nil, false
		}
		return []any{value}, true

	case "object":
		out := newOrderedMap()
		for _, property := range schema.get("properties").entries() {
			if value, ok := d.generateValue(property.value, seen); ok {
				out.Set(property.key.str(), value)
			}
		}
		return out, true

	default:
		// No declared type. The reference does not infer one from the presence
		// of `properties`, and inferring it here would produce bodies Dredd
		// never sends.
		return "", true
	}
}

// isValuelessPrimitiveSchema reports whether a schema describes a primitive and
// offers no specimen value for it.
func isValuelessPrimitiveSchema(schema node) bool {
	if !schema.isMapping() {
		return true
	}
	if schema.get("$ref").isScalar() {
		return false
	}
	if _, ok := schemaExample(schema); ok {
		return false
	}
	switch schema.get("type").str() {
	case "string", "integer", "number", "boolean":
		return true
	default:
		return false
	}
}

// renderBody serialises a generated value for a media type.
//
// It returns false when there is no body to send: either the media type is not
// one the reference generates for, or the value is falsy by JavaScript's rules,
// which the reference treats as nothing to send.
func renderBody(value any, mediaType string) (string, bool) {
	if !truthy(value) {
		return "", false
	}

	switch {
	case isJSONMediaType(mediaType):
		encoded, err := marshalJSON(value)
		if err != nil {
			return "", false
		}
		return encoded, true
	case isTextMediaType(mediaType):
		text, ok := value.(string)
		if !ok {
			return "", false
		}
		return text, true
	default:
		return "", false
	}
}

// truthy applies JavaScript's notion of truthiness, which is what the reference
// tests a generated value against before emitting a body.
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

// jsonSchemaDraft is the dialect the reference declares its output in.
const jsonSchemaDraft = "http://json-schema.org/draft-04/schema#"

// convertSchema turns a Schema Object into the JSON Schema a consumer validates
// a body against.
//
// References are not inlined. They are rewritten to point into a `definitions`
// block gathered alongside, which keeps a recursive schema finite — inlining a
// type that refers to itself would not terminate.
func (d *document) convertSchema(schema node) (string, bool) {
	resolved := node{schema.Node}
	if !resolved.isMapping() {
		return "", false
	}

	var references []string
	result, ok := d.convertSubSchema(resolved, &references)
	if !ok {
		return "", false
	}

	result.Set("$schema", jsonSchemaDraft)

	if len(references) > 0 {
		definitions := newOrderedMap()
		result.Set("definitions", definitions)

		// Converting a definition can discover further references, so the list
		// is drained rather than iterated: each one appends to the same list.
		for len(references) > 0 {
			reference := references[len(references)-1]
			references = references[:len(references)-1]

			id := referenceID(reference)
			if id == "" || definitions.Has(id) {
				continue
			}
			// Claim the name before converting, so a schema that refers to
			// itself finds the entry already present and stops.
			definitions.Set(id, newOrderedMap())

			referenced := d.pointer(reference)
			if converted, ok := d.convertSubSchema(referenced, &references); ok {
				definitions.Set(id, converted)
			}
		}
	}

	// A schema that is nothing but a reference cannot carry sibling keywords in
	// draft-04, where `$ref` makes a validator ignore them. If the target needs
	// no definitions of its own it is used directly; otherwise the reference is
	// wrapped in an allOf, which is the one place siblings survive.
	if ref, isRef := result.Get("$ref"); isRef {
		definitions, _ := result.Get("definitions")
		definitionMap, _ := definitions.(*orderedMap)
		id := referenceID(ref.(string))

		if definitionMap != nil {
			if target, ok := definitionMap.Get(id); ok {
				if targetMap, ok := target.(*orderedMap); ok && !schemaHasReferences(targetMap) {
					return encodeToString(targetMap)
				}
			}
		}

		wrapper := newOrderedMap()
		inner := newOrderedMap()
		inner.Set("$ref", ref)
		wrapper.Set("allOf", []any{inner})
		wrapper.Set("definitions", definitionMap)
		return encodeToString(wrapper)
	}

	return encodeToString(result)
}

// convertSubSchema converts one schema node, collecting the references it makes.
func (d *document) convertSubSchema(schema node, references *[]string) (*orderedMap, bool) {
	if !schema.isMapping() {
		return nil, false
	}

	// A reference is recorded and rewritten rather than followed, so the
	// definition it names is emitted once however many times it is used.
	if ref := schema.get("$ref"); ref.isScalar() {
		*references = append(*references, ref.Value)
		out := newOrderedMap()
		out.Set("$ref", localReference(ref.Value))
		return out, true
	}

	out := newOrderedMap()
	for _, member := range schema.entries() {
		key := member.key.str()

		switch key {
		// OpenAPI vocabulary with no JSON Schema meaning. `example` is dropped
		// here and re-added below as `examples`, which is the keyword a
		// validator understands.
		case "discriminator", "readOnly", "xml", "externalDocs", "example":
			continue

		case "allOf", "anyOf", "oneOf":
			var list []any
			for _, item := range member.value.items() {
				if converted, ok := d.convertSubSchema(item, references); ok {
					list = append(list, converted)
				}
			}
			out.Set(key, list)

		case "not":
			if converted, ok := d.convertSubSchema(member.value, references); ok {
				out.Set(key, converted)
			}

		case "items":
			// Draft-04 allows items to be a single schema or a list of them.
			if member.value.isSequence() {
				var list []any
				for _, item := range member.value.items() {
					if converted, ok := d.convertSubSchema(item, references); ok {
						list = append(list, converted)
					}
				}
				out.Set(key, list)
				continue
			}
			if converted, ok := d.convertSubSchema(member.value, references); ok {
				out.Set(key, converted)
			}

		case "properties", "patternProperties":
			properties := newOrderedMap()
			for _, property := range member.value.entries() {
				if converted, ok := d.convertSubSchema(property.value, references); ok {
					properties.Set(property.key.str(), converted)
				}
			}
			out.Set(key, properties)

		case "additionalProperties", "additionalItems":
			if member.value.isMapping() {
				if converted, ok := d.convertSubSchema(member.value, references); ok {
					out.Set(key, converted)
				}
				continue
			}
			out.Set(key, scalarValue(member.value))

		case "nullable":
			// Handled after the loop, where the type it has to widen is known.
			continue

		case "type":
			// OpenAPI 2 carried a `file` type that JSON Schema has no notion
			// of; it is narrowed to the closest thing a validator can check.
			if member.value.str() == "file" {
				out.Set(key, "string")
				continue
			}
			out.Set(key, scalarValue(member.value))

		default:
			if strings.HasPrefix(key, "x-") {
				continue
			}
			out.Set(key, scalarValue(member.value))
		}
	}

	if example := schema.get("example"); example.valid() {
		out.Set("examples", []any{scalarValue(example)})
	}

	if schema.get("nullable").boolValue() {
		applyNullable(out)
	}

	return out, true
}

// applyNullable widens a schema so null validates against it.
//
// An allOf becomes an anyOf including null, because null cannot satisfy every
// branch of an allOf; a plain type simply gains null alongside it.
func applyNullable(out *orderedMap) {
	if allOf, ok := out.Get("allOf"); ok {
		if branches, ok := allOf.([]any); ok {
			nullBranch := newOrderedMap()
			nullBranch.Set("type", "null")
			out.Set("anyOf", append(append([]any{}, branches...), nullBranch))
			out.Delete("allOf")
			out.Delete("type")
			return
		}
	}
	if declared, ok := out.Get("type"); ok {
		if name, ok := declared.(string); ok {
			out.Set("type", []any{name, "null"})
		}
	}
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

// localReference rewrites a document reference to point into the definitions
// block this converter emits.
func localReference(reference string) string {
	return "#/definitions/" + referenceID(reference)
}

func referenceID(reference string) string {
	parts := strings.Split(reference, "/")
	return parts[len(parts)-1]
}

func encodeToString(value any) (string, bool) {
	encoded, err := marshalJSON(value)
	if err != nil {
		return "", false
	}
	return encoded, true
}

// scalarValue converts a YAML node to a plain Go value.
func scalarValue(n node) any {
	if !n.valid() {
		return nil
	}
	switch n.Kind {
	case yaml.ScalarNode:
		return scalarFromTag(n)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, item := range n.items() {
			out = append(out, scalarValue(item))
		}
		return out
	case yaml.MappingNode:
		// Ordered, because a value from the document may end up serialised as a
		// message body, where key order is part of the bytes being compared.
		out := newOrderedMap()
		for _, member := range n.entries() {
			out.Set(member.key.str(), scalarValue(member.value))
		}
		return out
	default:
		return nil
	}
}

// scalarFromTag decodes a scalar using the type YAML resolved for it, so that
// `200` is a number and `"200"` is a string — a distinction the specification
// leans on for status codes.
func scalarFromTag(n node) any {
	switch n.Tag {
	case "!!int":
		if v, err := strconv.ParseFloat(n.Value, 64); err == nil {
			return v
		}
	case "!!float":
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

func primitiveElement(value any) *refract.Element {
	switch v := value.(type) {
	case string:
		return refract.String(v)
	case float64:
		return refract.Number(v)
	case bool:
		return refract.Bool(v)
	default:
		return refract.Null()
	}
}

func setPrimitive(element *refract.Element, value any) {
	switch v := value.(type) {
	case string:
		element.Kind = refract.ContentPrimitive
		element.Primitive = v
	case float64:
		element.Kind = refract.ContentPrimitive
		element.Primitive = v
	case bool:
		element.Kind = refract.ContentPrimitive
		element.Primitive = v
	}
}

// marshalJSON encodes without HTML escaping, so a body containing `<`, `>` or
// `&` is sent as written rather than as escape sequences the server never
// promised.
func marshalJSON(value any) (string, error) {
	var buf strings.Builder
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// isJSONMediaType reports whether a media type carries JSON, covering the
// `+json` suffix conventions as well as application/json itself.
func isJSONMediaType(mediaType string) bool {
	base := baseMediaType(mediaType)
	if !strings.HasPrefix(base, "application/") {
		return false
	}
	subtype := strings.TrimPrefix(base, "application/")
	return subtype == "json" || strings.HasSuffix(subtype, "+json")
}

func isTextMediaType(mediaType string) bool {
	return strings.HasPrefix(baseMediaType(mediaType), "text/")
}

func baseMediaType(mediaType string) string {
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}
	return strings.ToLower(strings.TrimSpace(mediaType))
}
