package openapi3

import (
	"fmt"
	"sort"
	"strings"

	"github.com/antimatter-studios/vertrag/internal/refract"
)

// annotation is a diagnostic about the description document.
type annotation struct {
	class   string // "warning" or "error"
	message string
	line    int
	column  int
	endLine int
	endCol  int
}

// objectSpec describes which keys an OpenAPI object may carry.
//
// The three categories are not the same thing. A supported key is understood; an
// unsupported one is real OpenAPI that this implementation does not act on, and
// is worth one collapsed warning however often it appears; anything else is not
// OpenAPI at all and is reported every time, because each occurrence is a
// separate mistake in the document.
type objectSpec struct {
	name        string
	supported   []string
	unsupported []string
	required    []string

	// parameterVariant marks the stricter Schema Object rules that apply inside
	// a parameter. It cannot be told from name, because both variants are
	// called "Schema Object" in the messages users see.
	parameterVariant bool

	// noExtensions marks an object where `x-` keys are not allowed through.
	noExtensions bool
}

// The key tables, transcribed from the reference parser. They are data rather
// than code because that is what they are there: a list per object type, which
// the specification revises independently of any logic.
var (
	specOpenAPI = objectSpec{
		name:        "OpenAPI Object",
		supported:   []string{"openapi", "info", "paths", "components", "security", "servers"},
		unsupported: []string{"tags", "externalDocs"},
		required:    []string{"openapi", "info", "paths"},
	}
	specInfo = objectSpec{
		name:      "Info Object",
		supported: []string{"title", "version", "description", "termsOfService", "contact", "license"},
		required:  []string{"title", "version"},
	}
	specContact = objectSpec{
		name:      "Contact Object",
		supported: []string{"name", "url", "email"},
	}
	specLicense = objectSpec{
		name:      "License Object",
		supported: []string{"name", "url"},
		required:  []string{"name"},
	}
	specServer = objectSpec{
		name:      "Server Object",
		supported: []string{"url", "description", "variables"},
		required:  []string{"url"},
	}
	specServerVariable = objectSpec{
		name:      "Server Variable Object",
		supported: []string{"default", "description", "enum"},
		required:  []string{"default"},
	}
	specPathItem = objectSpec{
		name: "Path Item Object",
		supported: append([]string{"summary", "description", "servers", "parameters"},
			httpMethods...),
		unsupported: []string{"$ref"},
	}
	specOperation = objectSpec{
		name: "Operation Object",
		supported: []string{"summary", "description", "operationId", "responses",
			"requestBody", "parameters", "servers", "security"},
		unsupported: []string{"tags", "externalDocs", "callbacks", "deprecated"},
	}
	specParameter = objectSpec{
		name:        "Parameter Object",
		supported:   []string{"name", "in", "description", "required", "schema", "example", "explode"},
		unsupported: []string{"deprecated", "allowEmptyValue", "style", "allowReserved", "examples", "content"},
		required:    []string{"name", "in"},
	}
	specRequestBody = objectSpec{
		name:        "Request Body Object",
		supported:   []string{"content", "description"},
		unsupported: []string{"required"},
	}
	specResponse = objectSpec{
		name:        "Response Object",
		supported:   []string{"content", "description", "headers"},
		unsupported: []string{"links"},
		required:    []string{"description"},
	}
	specMediaType = objectSpec{
		name:        "Media Type Object",
		supported:   []string{"schema", "example", "examples"},
		unsupported: []string{"encoding"},
	}
	specHeader = objectSpec{
		name: "Header Object",
		unsupported: []string{"description", "required", "deprecated", "allowEmptyValue",
			"style", "explode", "allowReserved", "schema", "content", "example", "examples"},
	}
	specExample = objectSpec{
		name:        "Example Object",
		supported:   []string{"value"},
		unsupported: []string{"summary", "description", "externalValue"},
	}
	specComponents = objectSpec{
		name: "Components Object",
		supported: []string{"schemas", "parameters", "requestBodies", "responses",
			"headers", "examples", "securitySchemes", "pathItems"},
		unsupported: []string{"links", "callbacks"},
	}

	specSecurityScheme = objectSpec{
		name:        "Security Scheme Object",
		supported:   []string{"type", "description", "name", "in", "scheme", "flows"},
		unsupported: []string{"bearerFormat", "openIdConnectUrl"},
		required:    []string{"type"},
	}

	// A Schema Object is validated differently depending on where it appears.
	// Inside a parameter only a handful of keywords are acted on, so the rest —
	// including everyday ones like `format` — are reported as unsupported.
	specSchema = objectSpec{
		name: "Schema Object",
		// Unlike every other object here, a Schema Object does not permit
		// specification extensions: an `x-` key in one is reported as invalid.
		// JSON Schema has its own extension rules and does not defer to
		// OpenAPI's.
		noExtensions: true,
		supported: []string{"type", "enum", "const", "properties", "items", "required",
			"nullable", "oneOf", "additionalProperties", "default", "title",
			"description", "example"},
		unsupported: []string{"allOf", "anyOf", "not", "multipleOf", "maximum",
			"exclusiveMaximum", "minimum", "exclusiveMinimum", "maxLength", "minLength",
			"pattern", "format", "maxItems", "minItems", "uniqueItems", "maxProperties",
			"minProperties", "discriminator", "readOnly", "writeOnly", "xml",
			"externalDocs", "deprecated"},
	}
	specParameterSchema = objectSpec{
		name:             "Schema Object",
		parameterVariant: true,
		supported:        []string{"type", "enum", "description", "title", "example"},
		unsupported: []string{"$ref", "multipleOf", "maximum", "exclusiveMaximum", "minimum",
			"exclusiveMinimum", "maxLength", "minLength", "pattern", "maxItems", "minItems",
			"uniqueItems", "maxProperties", "minProperties", "properties", "items",
			"required", "nullable", "default", "oneOf", "allOf", "anyOf", "not",
			"additionalProperties", "format", "discriminator", "readOnly", "writeOnly",
			"xml", "externalDocs", "deprecated"},
	}
)

// validate walks the document and collects every diagnostic it can raise.
//
// The walk is separate from the parse that produces transactions, and covers
// parts of the document the parse has no use for — component schemas that
// nothing references, for instance. The reference reports those too, and a
// document's problems should not depend on which corners of it happened to be
// needed.
func (d *document) validate() []annotation {
	var out []annotation

	root := d.root
	if !root.isMapping() {
		return out
	}

	out = append(out, d.validateKeys(root, specOpenAPI)...)

	out = append(out, d.validateObject(root.get("info"), specInfo,
		func(info node) []annotation {
			var nested []annotation
			nested = append(nested, d.validateObject(info.get("contact"), specContact, nil)...)
			nested = append(nested, d.validateObject(info.get("license"), specLicense, nil)...)
			return nested
		})...)

	for _, server := range root.get("servers").items() {
		out = append(out, d.validateServer(server)...)
	}

	out = append(out, d.validatePaths(root.get("paths"))...)
	out = append(out, d.validateComponents(root.get("components"))...)

	return out
}

func (d *document) validateServer(server node) []annotation {
	return d.validateObject(server, specServer, func(s node) []annotation {
		var nested []annotation
		for _, variable := range s.get("variables").entries() {
			nested = append(nested, d.validateObject(variable.value, specServerVariable, nil)...)
		}
		return nested
	})
}

// validatePaths checks the Paths Object and everything beneath it.
func (d *document) validatePaths(paths node) []annotation {
	if !paths.isMapping() {
		return nil
	}

	var out []annotation
	for _, member := range paths.entries() {
		key := member.key.str()

		// Every key of a Paths Object is a path template, and a path template
		// starts with a slash. Anything else is a key in the wrong place.
		if !strings.HasPrefix(key, "/") {
			if strings.HasPrefix(key, "x-") {
				continue
			}
			out = append(out, d.invalidKey("Paths Object", member.key))
			continue
		}
		out = append(out, d.validatePathItem(member.value)...)
	}
	return out
}

func (d *document) validatePathItem(pathItem node) []annotation {
	return d.validateObject(pathItem, specPathItem, func(item node) []annotation {
		var out []annotation
		for _, parameter := range item.get("parameters").items() {
			out = append(out, d.validateParameter(parameter)...)
		}
		for _, member := range item.entries() {
			if isHTTPMethod(member.key.str()) {
				out = append(out, d.validateOperation(member.value)...)
			}
		}
		return out
	})
}

func (d *document) validateOperation(operation node) []annotation {
	return d.validateObject(operation, specOperation, func(op node) []annotation {
		var out []annotation
		for _, parameter := range op.get("parameters").items() {
			out = append(out, d.validateParameter(parameter)...)
		}
		out = append(out, d.validateRequestBody(op.get("requestBody"))...)
		out = append(out, d.validateResponses(op.get("responses"))...)
		for _, server := range op.get("servers").items() {
			out = append(out, d.validateServer(server)...)
		}
		return out
	})
}

func (d *document) validateParameter(parameter node) []annotation {
	return d.validateObject(parameter, specParameter, func(p node) []annotation {
		var out []annotation

		// A parameter travels in the path, the query string or a header.
		// Anything else — a cookie — is a place this implementation does not
		// put values, so the parameter would silently not be sent.
		if in := p.get("in"); in.isScalar() && in.Value != "path" && in.Value != "query" && in.Value != "header" {
			out = append(out, d.at(annotation{
				class:   "warning",
				message: fmt.Sprintf("'Parameter Object' 'in' '%s' is unsupported", in.Value),
			}, in))
		}

		return append(out, d.validateSchema(p.get("schema"), specParameterSchema)...)
	})
}

func (d *document) validateRequestBody(body node) []annotation {
	return d.validateObject(body, specRequestBody, func(b node) []annotation {
		return d.validateContent(b.get("content"))
	})
}

func (d *document) validateResponses(responses node) []annotation {
	if !responses.isMapping() {
		return nil
	}

	var out []annotation
	for _, member := range responses.entries() {
		key := member.key.str()
		if strings.HasPrefix(key, "x-") {
			continue
		}

		switch {
		case statusCodePattern.MatchString(key) || key == "default":
			// A response proper.
		case isStatusCodeRange(key):
			out = append(out, d.at(annotation{
				class:   "warning",
				message: "'Responses Object' response status code ranges are unsupported",
			}, member.key))
		default:
			out = append(out, d.invalidKey("Responses Object", member.key))
			continue
		}

		out = append(out, d.validateObject(member.value, specResponse, func(response node) []annotation {
			var nested []annotation
			nested = append(nested, d.validateContent(response.get("content"))...)
			for _, header := range response.get("headers").entries() {
				nested = append(nested, d.validateObject(header.value, specHeader, nil)...)
			}
			return nested
		})...)
	}
	return out
}

func (d *document) validateContent(content node) []annotation {
	if !content.isMapping() {
		return nil
	}

	var out []annotation
	for _, member := range content.entries() {
		out = append(out, d.validateObject(member.value, specMediaType, func(mediaType node) []annotation {
			var nested []annotation
			nested = append(nested, d.validateSchema(mediaType.get("schema"), specSchema)...)

			examples := mediaType.get("examples").entries()
			for _, example := range examples {
				nested = append(nested, d.validateObject(example.value, specExample, nil)...)
			}
			// A transaction carries one body, so only the first example can be
			// sent. The rest are dropped, and saying so is the difference
			// between a deliberate choice and silently ignoring the document.
			if len(examples) > 1 {
				nested = append(nested, d.at(annotation{
					class:   "warning",
					message: "'Media Type Object' 'examples' only one example is supported, other examples have been ignored",
				}, examples[1].key))
			}
			return nested
		})...)
	}
	return out
}

func (d *document) validateComponents(components node) []annotation {
	return d.validateObject(components, specComponents, func(c node) []annotation {
		var out []annotation
		for _, schema := range c.get("schemas").entries() {
			out = append(out, d.validateSchema(schema.value, specSchema)...)
		}
		for _, parameter := range c.get("parameters").entries() {
			out = append(out, d.validateParameter(parameter.value)...)
		}
		for _, body := range c.get("requestBodies").entries() {
			out = append(out, d.validateRequestBody(body.value)...)
		}
		for _, header := range c.get("headers").entries() {
			out = append(out, d.validateObject(header.value, specHeader, nil)...)
		}
		for _, example := range c.get("examples").entries() {
			out = append(out, d.validateObject(example.value, specExample, nil)...)
		}
		for _, scheme := range c.get("securitySchemes").entries() {
			out = append(out, d.validateObject(scheme.value, specSecurityScheme, nil)...)
		}
		return out
	})
}

// validateSchema checks a Schema Object and recurses through its subschemas.
func (d *document) validateSchema(schema node, spec objectSpec) []annotation {
	if !schema.isMapping() {
		return nil
	}
	// A schema that is only a reference is a Reference Object, not a Schema
	// Object, and is checked where it is defined rather than at every use.
	// Inside a parameter the reference is not followed at all, so `$ref` is
	// reported there as an unsupported key instead.
	if schema.get("$ref").isScalar() && !spec.parameterVariant {
		return nil
	}

	out := d.validateKeys(schema, spec)

	// A schema under additionalProperties describes what unlisted properties
	// must look like. That is not acted on, and saying so is the difference
	// between "checked and passed" and "never looked at".
	if additional := schema.get("additionalProperties"); additional.isMapping() {
		out = append(out, d.at(annotation{
			class:   "warning",
			message: "'Schema Object' 'additionalProperties' containing a Schema Object is currently unsupported",
		}, additional))
	}

	// Subschemas are always full Schema Objects, even when reached from a
	// parameter, so the stricter parameter rules do not propagate downwards.
	for _, property := range schema.get("properties").entries() {
		out = append(out, d.validateSchema(property.value, specSchema)...)
	}
	if items := schema.get("items"); items.isMapping() {
		out = append(out, d.validateSchema(items, specSchema)...)
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		for _, item := range schema.get(keyword).items() {
			out = append(out, d.validateSchema(item, specSchema)...)
		}
	}
	return out
}

// validateObject checks an object's keys and then its children.
func (d *document) validateObject(n node, spec objectSpec, children func(node) []annotation) []annotation {
	if !n.isMapping() {
		return nil
	}
	// An object that is a reference is a Reference Object, whatever position it
	// occupies. Checking it against the host object's key list would report its
	// `$ref` as invalid; the target is checked where it is defined.
	//
	// A Path Item is the exception: it lists `$ref` as unsupported, so the
	// reference there is a diagnostic rather than something to follow.
	if n.get("$ref").isScalar() && !contains(spec.unsupported, "$ref") {
		return nil
	}
	out := d.validateKeys(n, spec)
	if children != nil {
		out = append(out, children(n)...)
	}
	return out
}

// validateKeys reports the keys an object should not have, and the required
// ones it lacks.
func (d *document) validateKeys(n node, spec objectSpec) []annotation {
	var out []annotation

	supported := toSet(spec.supported)
	unsupported := toSet(spec.unsupported)

	for _, member := range n.entries() {
		key := member.key.str()
		switch {
		case supported[key]:
		case unsupported[key]:
			out = append(out, d.at(annotation{
				class:   "warning",
				message: fmt.Sprintf("'%s' contains unsupported key '%s'", spec.name, key),
			}, member.key))
		case strings.HasPrefix(key, "x-") && !spec.noExtensions:
			// Specification extensions are explicitly allowed to be anything.
		default:
			out = append(out, d.invalidKey(spec.name, member.key))
		}
	}

	for _, required := range spec.required {
		if !n.get(required).valid() {
			out = append(out, d.at(annotation{
				class:   "error",
				message: fmt.Sprintf("'%s' is missing required property '%s'", spec.name, required),
			}, n))
		}
	}

	return out
}

func (d *document) invalidKey(objectName string, key node) annotation {
	return d.at(annotation{
		class:   "warning",
		message: fmt.Sprintf("'%s' contains invalid key '%s'", objectName, key.str()),
	}, key)
}

// at attaches the source range of the node a diagnostic is about.
func (d *document) at(a annotation, n node) annotation {
	if !n.valid() {
		return a
	}
	a.line, a.column, a.endLine, a.endCol = d.span(n)
	return a
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func toSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

// deduplicateUnsupported collapses repeated "unsupported key" warnings.
//
// The first occurrence is kept, with its position, and gains a count. A
// document that uses `format` two hundred times has one problem, not two
// hundred, and reporting it once is the difference between a readable summary
// and a wall of noise. Invalid keys are deliberately not collapsed: each one is
// a separate mistake at a separate place.
func deduplicateUnsupported(annotations []annotation) []annotation {
	counts := map[string]int{}
	for _, a := range annotations {
		if isUnsupportedWarning(a) {
			counts[a.message]++
		}
	}

	seen := map[string]bool{}
	out := annotations[:0]
	for _, a := range annotations {
		if !isUnsupportedWarning(a) {
			out = append(out, a)
			continue
		}
		if seen[a.message] {
			continue
		}
		seen[a.message] = true
		if count := counts[a.message]; count > 1 {
			// "occurances" is the reference's spelling. It reaches users in
			// Dredd's output today, so correcting it here would be a difference.
			a.message = fmt.Sprintf("%s (%d occurances)", a.message, count)
		}
		out = append(out, a)
	}
	return out
}

// isUnsupportedWarning selects the warnings that are collapsed with a count.
//
// The ignored-examples warning is collapsed alongside the unsupported-key ones,
// which is not obvious from its wording but is what the reference does: both
// say "this document uses something not acted on", and repeating either per
// occurrence would drown the rest.
func isUnsupportedWarning(a annotation) bool {
	if a.class != "warning" {
		return false
	}
	return strings.Contains(a.message, "contains unsupported key") ||
		a.message == "'Media Type Object' 'examples' only one example is supported, other examples have been ignored"
}

// elements renders the collected diagnostics as API Elements annotations.
func annotationElements(annotations []annotation) []*refract.Element {
	out := make([]*refract.Element, 0, len(annotations))
	for _, a := range annotations {
		element := refract.Text("annotation", a.message)
		element.AddClass(a.class)
		if a.line > 0 {
			element.SetSourceMap(a.line, a.column, a.endLine, a.endCol)
		}
		out = append(out, element)
	}
	return out
}

// errorsOnly selects the error-class diagnostics.
func errorsOnly(annotations []annotation) []annotation {
	var out []annotation
	for _, a := range annotations {
		if a.class == "error" {
			out = append(out, a)
		}
	}
	return out
}

// sortStable keeps diagnostics in document order, which is the order a reader
// works through the file in.
func sortByPosition(annotations []annotation) {
	sort.SliceStable(annotations, func(i, j int) bool {
		if annotations[i].line != annotations[j].line {
			return annotations[i].line < annotations[j].line
		}
		return annotations[i].column < annotations[j].column
	})
}
