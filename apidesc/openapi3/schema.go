package openapi3

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/antimatter-studios/vertrag/generate"

	"github.com/antimatter-studios/vertrag/refract"
	"gopkg.in/yaml.v3"
)

// schemaElementName maps a Schema Object's declared type onto the API Elements
// primitive that represents it.
//
// A schema with no declared type becomes a bare "string": the reference treats
// an untyped schema as having no value to offer, and a string element with no
// content is how that is expressed.
func schemaElementName(schema node) string {
	if !schema.Valid() {
		return "string"
	}
	switch schema.Get("type").Str() {
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
	if !schema.Valid() {
		return nil, false
	}
	// A const fixes the value outright — there is nothing else it could be.
	if fixed := schema.Get("const"); fixed.Valid() {
		return scalarValue(fixed), true
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
	if !schema.Valid() {
		return nil, false
	}

	// Follow a reference, refusing to re-enter one already being expanded.
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

	// A nullable schema with nothing else to say demonstrates the null. In 3.0
	// that is the `nullable` keyword; in 3.1 it is "null" among the types, and
	// both mean the same thing to a reader.
	if schema.Get("nullable").Bool() || allowsNull(schema) {
		return nil, true
	}

	switch declaredType(schema) {
	case "string":
		return constraintsOf(schema).String(), true
	case "integer", "number":
		return constraintsOf(schema).Number(0), true
	case "boolean":
		return false, true

	case "array":
		// 2020-12 describes a tuple with prefixItems, one schema per position.
		// An empty array satisfies it, which is what came out before, but
		// demonstrates nothing about the positions the document went to the
		// trouble of describing.
		if prefix := schema.Get("prefixItems"); prefix.IsSequence() {
			if value, ok := d.generateTuple(prefix, seen); ok {
				return value, true
			}
		}

		items := schema.Get("items")

		value, ok := d.generateValue(items, seen)
		if !ok || isValuelessPrimitiveSchema(items) {
			// Dredd treats an array of a bare primitive as having no specimen,
			// on the grounds that the document said nothing about what would be
			// in it. Returning nothing was catastrophic rather than cautious:
			// a required property propagates the failure upward, so a single
			// `tags: [string]` field anywhere in a required chain destroyed the
			// ENTIRE body, and the request went out empty.
			//
			// An empty array is a valid specimen for any array the document did
			// not give a minItems, which is exactly the case in question, and
			// it costs nothing to be right about the rest of the body.
			return []any{}, true
		}

		// One item demonstrates the shape, which is all a document without a
		// minItems asks for. With one, the array has to actually carry that
		// many or it is a specimen the schema forbids.
		length := generate.Items(numberOf(schema, "minItems"))
		out := make([]any, 0, length)
		for i := 0; i < length; i++ {
			out = append(out, value)
		}
		return out, true

	case "object":
		// A property the document requires but cannot demonstrate leaves the
		// whole object with no value: any specimen built without it would be
		// one the document itself says is invalid. An optional property in the
		// same position is simply left out.
		required := requiredProperties(schema)

		out := newOrderedMap()
		for _, property := range schema.Get("properties").Entries() {
			name := property.Key.Str()
			value, ok := d.generateValue(property.Value, seen)
			if !ok {
				if required[name] {
					return nil, false
				}
				continue
			}
			out.Set(name, value)
		}
		return out, true

	default:
		// An untyped schema may still be built out of others.
		//
		// `allOf` has to be satisfied in full — every branch at once — so the
		// branches are merged. Dredd acts on none of these and falls through to
		// the empty string, which for an allOf of two object schemas is a
		// specimen satisfying neither.
		if branches := schema.Get("allOf").Items(); len(branches) > 0 {
			return d.mergeAll(branches, seen)
		}

		// `oneOf` and `anyOf` both permit a value matching one branch, so the
		// first that can be demonstrated is a valid specimen for either. They
		// differ in whether matching several is allowed, which constrains a
		// value being checked rather than one being invented.
		for _, keyword := range []string{"oneOf", "anyOf"} {
			for _, branch := range schema.Get(keyword).Items() {
				if value, ok := d.generateValue(branch, seen); ok {
					return value, true
				}
			}
		}

		// Otherwise there is nothing to go on: no type, no example, no
		// composition. A schema that says nothing permits everything, so the
		// empty string is as good a specimen as any — this is NOT a schema
		// without a specimen, and treating it as one loses the body entirely
		// for an array whose items are untyped.
		return "", true
	}
}

// requiredProperties lists the property names a schema insists on.
func requiredProperties(schema node) map[string]bool {
	names := map[string]bool{}
	for _, item := range schema.Get("required").Items() {
		if name := item.Str(); name != "" {
			names[name] = true
		}
	}
	return names
}

// declaredType reads a schema's type, which OpenAPI 3.1 allows to be a list.
//
// The first type that is not "null" is used: a schema saying a value may be a
// string or absent is describing a string, and the null case is handled before
// this is reached.
func declaredType(schema node) string {
	declared := schema.Get("type")
	if !declared.IsSequence() {
		return declared.Str()
	}
	for _, item := range declared.Items() {
		if name := item.Str(); name != "" && name != "null" {
			return name
		}
	}
	return "null"
}

// allowsNull reports whether a 3.1 type list includes "null".
func allowsNull(schema node) bool {
	declared := schema.Get("type")
	if !declared.IsSequence() {
		return false
	}
	for _, item := range declared.Items() {
		if item.Str() == "null" {
			return true
		}
	}
	return false
}

// isValuelessPrimitiveSchema reports whether a schema describes a primitive and
// offers no specimen value for it.
func isValuelessPrimitiveSchema(schema node) bool {
	if !schema.IsMapping() {
		return true
	}
	if schema.Get("$ref").IsScalar() {
		return false
	}
	if _, ok := schemaExample(schema); ok {
		return false
	}
	switch declaredType(schema) {
	case "string", "integer", "number", "boolean":
		// Dredd stops here: a bare primitive is treated as saying nothing about
		// what would be in the array. But a constrained one says a great deal —
		// a minLength, a pattern, a format or an enum each describe the
		// contents exactly — and an array whose items carry any of those has a
		// specimen worth sending.
		return !constraintsOf(schema).Describes()
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
	// Dredd tests the generated value for JavaScript truthiness here, so a
	// documented body of `false`, `null`, `0` or `""` produces no body at all.
	// That is a language's notion of emptiness leaking into a contract: false
	// is a perfectly good response, and as a REQUEST body the omission means
	// sending nothing to a server that requires one, which then answers 400 and
	// is reported as broken.
	//
	// Whether a specimen exists is already answered by generateValue's second
	// return, so there is nothing for this to second-guess.

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
	case isFormMediaType(mediaType):
		return renderForm(value)
	default:
		return "", false
	}
}

// isFormMediaType reports the encoding HTML forms have posted since forever and
// a great many APIs still accept.
func isFormMediaType(mediaType string) bool {
	return strings.EqualFold(baseMediaType(mediaType), "application/x-www-form-urlencoded")
}

// renderForm encodes an object the way a form post carries it.
//
// Dredd renders nothing here, exactly as it renders nothing for multipart, so
// an endpoint taking a form is sent an empty body and any server requiring its
// fields answers 400 — the endpoint cannot be tested at all. It is the same
// defect as the multipart one and a good deal more common, forms being what a
// great many APIs still accept.
//
// Only a flat object encodes: a nested one has no single spelling here, since
// whether it becomes `a[b]=1`, `a.b=1` or JSON in a field depends on a
// convention the description does not state. Guessing would send something the
// document never described.
func renderForm(value any) (string, bool) {
	object, ok := value.(*orderedMap)
	if !ok {
		return "", false
	}

	form := url.Values{}
	for _, key := range object.Keys() {
		nested, _ := object.Get(key)
		switch nested.(type) {
		case *orderedMap, []any:
			return "", false
		}
		form.Set(key, stringifyScalar(nested))
	}
	if len(form) == 0 {
		return "", false
	}

	// Encoded in the document's own key order rather than sorted, so the body
	// reads as the description does. url.Values.Encode sorts, so it cannot be
	// used directly.
	var b strings.Builder
	for i, key := range object.Keys() {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(key))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(form.Get(key)))
	}
	return b.String(), true
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
	// Deliberately NOT resolved: the reference a document wrote is what gets
	// rewritten into the definitions block, so following it here would inline
	// the target and lose the reference the output is built around.
	if !schema.IsMapping() {
		return "", false
	}

	var references []string
	result, ok := d.convertSubSchema(schema, &references)
	if !ok {
		return "", false
	}

	result.Set("$schema", d.jsonSchemaDialect())

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

			referenced := d.Pointer(reference)
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
	if !schema.IsMapping() {
		return nil, false
	}

	// A reference is recorded and rewritten rather than followed, so the
	// definition it names is emitted once however many times it is used.
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
		// OpenAPI vocabulary with no JSON Schema meaning. `example` is dropped
		// here and re-added below as `examples`, which is the keyword a
		// validator understands.
		case "discriminator", "readOnly", "xml", "externalDocs", "example":
			continue

		case "allOf", "anyOf", "oneOf":
			var list []any
			for _, item := range member.Value.Items() {
				if converted, ok := d.convertSubSchema(item, references); ok {
					list = append(list, converted)
				}
			}
			out.Set(key, list)

		case "not":
			if converted, ok := d.convertSubSchema(member.Value, references); ok {
				out.Set(key, converted)
			}

		case "items":
			// Draft-04 allows items to be a single schema or a list of them.
			if member.Value.IsSequence() {
				var list []any
				for _, item := range member.Value.Items() {
					if converted, ok := d.convertSubSchema(item, references); ok {
						list = append(list, converted)
					}
				}
				out.Set(key, list)
				continue
			}
			if converted, ok := d.convertSubSchema(member.Value, references); ok {
				out.Set(key, converted)
			}

		case "properties", "patternProperties":
			properties := newOrderedMap()
			for _, property := range member.Value.Entries() {
				if converted, ok := d.convertSubSchema(property.Value, references); ok {
					properties.Set(property.Key.Str(), converted)
				}
			}
			out.Set(key, properties)

		case "additionalProperties", "additionalItems":
			if member.Value.IsMapping() {
				if converted, ok := d.convertSubSchema(member.Value, references); ok {
					out.Set(key, converted)
				}
				continue
			}
			out.Set(key, scalarValue(member.Value))

		case "nullable":
			// A 3.0 keyword, handled after the loop where the type it widens is
			// known. In 3.1 it does not exist, and a document using it is
			// saying something the dialect does not define.
			if d.modernSchemas {
				out.Set(key, scalarValue(member.Value))
				continue
			}
			continue

		case "type":
			// OpenAPI 2 carried a `file` type that JSON Schema has no notion
			// of; it is narrowed to the closest thing a validator can check.
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

	if example := schema.Get("example"); example.Valid() {
		// 2020-12 has an `examples` array of its own, so a 3.1 document's
		// example needs no translation and must not be duplicated into one.
		if !d.modernSchemas {
			out.Set("examples", []any{scalarValue(example)})
		}
	}

	if !d.modernSchemas && schema.Get("nullable").Bool() {
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
		// Ordered, because a value from the document may end up serialised as a
		// message body, where key order is part of the bytes being compared.
		out := newOrderedMap()
		for _, member := range n.Entries() {
			out.Set(member.Key.Str(), scalarValue(member.Value))
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

// setPrimitive gives an element the value a document example carries. A list
// or an object example becomes array or object content, so a parameter whose
// example is a list reaches the URI — it used to be dropped here silently, and
// the parameter with it, which is why array-typed query parameters never
// appeared in a compiled request.
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
	case []any:
		element.Kind = refract.ContentArray
		element.Children = nil
		for _, item := range v {
			element.Append(valueElement(item))
		}
	case *orderedMap:
		element.Kind = refract.ContentArray
		element.Children = nil
		for _, key := range v.Keys() {
			item, _ := v.Get(key)
			element.Append(refract.Member(key, valueElement(item)))
		}
	}
}

// valueElement is primitiveElement extended to lists and objects, for the
// children setPrimitive builds.
func valueElement(value any) *refract.Element {
	switch v := value.(type) {
	case []any:
		e := refract.Array()
		for _, item := range v {
			e.Append(valueElement(item))
		}
		return e
	case *orderedMap:
		e := refract.Object()
		for _, key := range v.Keys() {
			item, _ := v.Get(key)
			e.Append(refract.Member(key, valueElement(item)))
		}
		return e
	default:
		return primitiveElement(value)
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

// mergeAll builds one specimen satisfying every branch of an `allOf`.
//
// The branches are almost always object schemas contributing properties, and
// the merged object carries all of them — a value satisfying only the first
// branch satisfies the allOf no better than one satisfying none.
//
// A branch that is not an object cannot be merged with anything, so the first
// such branch is the whole answer. That is a narrowing, not a fudge: an allOf
// combining a scalar with an object describes a value nothing can satisfy, and
// the schema is what is wrong there.
func (d *document) mergeAll(branches []node, seen map[string]bool) (any, bool) {
	merged := newOrderedMap()
	mergedAny := false

	for _, branch := range branches {
		value, ok := d.generateValue(branch, seen)
		if !ok {
			continue
		}

		nested, isObject := value.(*orderedMap)
		if !isObject {
			// Not combinable. Whatever it is, it is the only specimen on offer.
			return value, true
		}
		for _, key := range nested.Keys() {
			v, _ := nested.Get(key)
			merged.Set(key, v)
		}
		mergedAny = true
	}

	if !mergedAny {
		return nil, false
	}
	return merged, true
}

// generateTuple builds a specimen for a 2020-12 prefixItems array.
//
// Every position has to be demonstrable, because a tuple missing one of its
// members is not a shorter tuple — the positions after it shift, and the
// specimen describes something the document did not.
func (d *document) generateTuple(prefix node, seen map[string]bool) (any, bool) {
	out := make([]any, 0, len(prefix.Items()))
	for _, position := range prefix.Items() {
		value, ok := d.generateValue(position, seen)
		if !ok {
			return nil, false
		}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}
