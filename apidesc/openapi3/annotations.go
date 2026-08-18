package openapi3

import (
	"fmt"
	"github.com/antimatter-studios/vertrag/yamldoc"
	"regexp"
	"sort"
	"strings"

	"github.com/antimatter-studios/vertrag/refract"
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
		name: "OpenAPI Object",
		// `tags` here only declares the vocabulary operations draw from; the
		// declarations change nothing about a run, but warning about them while
		// operation tags filter runs would call one half of a feature
		// unsupported.
		//
		// `$self` is 3.2's, and is read rather than merely tolerated: it is the
		// URI the document gives itself, and a reference written through that
		// URI is a reference into this document. Before it was read, such a
		// reference resolved to nothing and left an empty schema behind, so the
		// response was validated against anything at all.
		//
		// `webhooks` and `jsonSchemaDialect` are 3.1's, and were both reported
		// as invalid keys — the message that means "this is not OpenAPI at all"
		// — for two fields the specification has defined since 3.1. Both are
		// acted on rather than merely tolerated: the dialect decides the rules
		// every schema here is validated under (see resolveDialect), and the
		// webhooks are validated and then declared unsent (see webhooks.go),
		// which is the one thing a client cannot do with them.
		supported: []string{"openapi", "$self", "info", "paths", "webhooks", "jsonSchemaDialect",
			"components", "security", "servers", "tags"},
		unsupported: []string{"externalDocs"},
		required:    []string{"openapi", "info", "paths"},
	}
	specInfo = objectSpec{
		name: "Info Object",
		// `summary` is 3.1's addition and was being reported as an invalid key
		// — telling the author of a valid 3.1 document that a field the
		// specification defines does not exist. It is accepted for 3.0 too:
		// the alternative is a version-dependent key list, and refusing a key
		// that harms nothing buys nothing.
		supported: []string{"title", "version", "summary", "description",
			"termsOfService", "contact", "license"},
		required: []string{"title", "version"},
	}
	specContact = objectSpec{
		name:      "Contact Object",
		supported: []string{"name", "url", "email"},
	}
	specLicense = objectSpec{
		name: "License Object",
		// `identifier` is 3.1's SPDX field, added alongside `summary` on the
		// Info Object and rejected for the same reason.
		supported: []string{"name", "url", "identifier"},
		required:  []string{"name"},
	}
	specServer = objectSpec{
		name: "Server Object",
		// `name` is 3.2's, and belongs with the rest of this object rather than
		// on the unsupported list: no field of a Server Object decides where a
		// request goes, because the endpoint under test comes from the command
		// line. Calling out the one 3.2 added while the `url` beside it passes
		// without comment would describe a distinction that does not exist.
		supported: []string{"url", "name", "description", "variables"},
		required:  []string{"url"},
	}
	specServerVariable = objectSpec{
		name:      "Server Variable Object",
		supported: []string{"default", "description", "enum"},
		required:  []string{"default"},
	}
	// The one table here that the revision changes, because 3.2 gave a Path
	// Item two more places to hold an operation. Validation reaches it through
	// pathItemSpec rather than naming it directly, so use that.
	specPathItem = objectSpec{
		name: "Path Item Object",
		supported: append([]string{"summary", "description", "servers", "parameters"},
			yamldoc.HTTPMethods...),
		unsupported: []string{"$ref"},
	}
	// `query` is the QUERY method, which 3.2 gave a field of its own;
	// `additionalOperations` holds the methods it did not.
	specPathItem32 = objectSpec{
		name: specPathItem.name,
		supported: append(append([]string{}, specPathItem.supported...),
			"query", additionalOperationsKey),
		unsupported: specPathItem.unsupported,
	}
	specOperation = objectSpec{
		name: "Operation Object",
		// `tags` are read: `--tag` narrows a run to the operations carrying
		// one. They are grouping metadata and change nothing about what is
		// sent or validated.
		supported: []string{"summary", "description", "operationId", "responses",
			"requestBody", "parameters", "servers", "security", "tags"},
		unsupported: []string{"externalDocs", "callbacks", "deprecated"},
	}
	specParameter = objectSpec{
		name: "Parameter Object",
		// `style` and `explode` are read: they decide how a list or an object
		// value is laid out on the wire, which both URI expansion and generation
		// follow.
		supported:   []string{"name", "in", "description", "required", "schema", "example", "examples", "explode", "style"},
		unsupported: []string{"deprecated", "allowEmptyValue", "allowReserved", "content"},
		required:    []string{"name", "in"},
	}
	specRequestBody = objectSpec{
		name: "Request Body Object",
		// `required` is read the one way a tester can read it: a required body
		// with nothing to build a body from gets a warning of its own, below.
		supported: []string{"content", "description", "required"},
	}
	specResponse = objectSpec{
		name: "Response Object",
		// `summary` is 3.2's, and is prose beside the `description` it was
		// added next to. `description` stopped being required in the same
		// revision, which responseSpec applies — the requirement stated here
		// is 3.0's and 3.1's.
		supported: []string{"content", "summary", "description", "headers", "links"},
		required:  []string{"description"},
	}
	specMediaType = objectSpec{
		name:      "Media Type Object",
		supported: []string{"schema", "example", "examples"},
		// `itemSchema` describes one item of a sequential body — an SSE event,
		// a line of JSON Lines, a part of a multipart/mixed stream — where
		// `schema` describes a body in one piece. vertrag reads a response to
		// its end and validates it whole, so it has no item to apply this to;
		// a streaming response would have to be split into items first, and
		// that is a different reader, not a different schema. Saying so is the
		// point: a 3.2 document that describes its stream this way and nothing
		// else is a document whose response bodies are going unchecked.
		//
		// `prefixEncoding` and `itemEncoding` are 3.2's positional forms of the
		// `encoding` beside them, and are not acted on for the same reason it
		// is not.
		unsupported: []string{"encoding", "itemSchema", "prefixEncoding", "itemEncoding"},
	}
	specHeader = objectSpec{
		name: "Header Object",
		// `schema` is read: the value a server sends is decoded to the type the
		// schema declares and validated against it, which is a check neither
		// Dredd nor Gavel makes. Warning that it is ignored would tell people
		// the constraint does nothing while it is the thing being enforced.
		supported: []string{"schema"},
		unsupported: []string{"description", "required", "deprecated", "allowEmptyValue",
			"style", "explode", "allowReserved", "content", "example", "examples"},
	}
	specLink = objectSpec{
		name: "Link Object",
		supported: []string{"operationId", "operationRef", "parameters",
			"requestBody", "description"},
		// `server` would point the linked request at a different host. vertrag
		// runs against one endpoint given on the command line, and silently
		// redirecting part of a run elsewhere is the kind of surprise a test
		// tool should not spring.
		unsupported: []string{"server"},
	}
	specExample = objectSpec{
		name: "Example Object",
		// `dataValue` is 3.2's unambiguous spelling of `value`: the example as
		// data, to be validated against the schema and serialised for sending.
		// That is exactly what `value` was already read as, so it is read the
		// same way — anything else would mean a document written the new way
		// lost the examples its author supplied, and sent generated bodies in
		// their place.
		supported: []string{"value", "dataValue"},
		// `serializedValue` is the other half of 3.2's split: the example
		// already serialised, escaping and all. Using it would mean parsing the
		// media type back into data before anything could be compared, which is
		// the work `externalValue` beside it is not done for either.
		unsupported: []string{"summary", "description", "externalValue", "serializedValue"},
	}
	specComponents = objectSpec{
		name: "Components Object",
		// `mediaTypes` is 3.2's, and arrives with the change that gives it a
		// use: a `content` entry may be a Reference Object now, where before it
		// had to be a Media Type Object written in place. Both halves are read
		// — see validateContent and parseContentFor, which resolve a media type
		// before looking at it — so a shared media type carries its schema and
		// examples to every operation that names it.
		supported: []string{"schemas", "parameters", "requestBodies", "responses",
			"headers", "examples", "securitySchemes", "pathItems", "links", "mediaTypes"},
		unsupported: []string{"callbacks"},
	}

	specSecurityScheme = objectSpec{
		name:      "Security Scheme Object",
		supported: []string{"type", "description", "name", "in", "scheme", "flows"},
		// `oauth2MetadataUrl` is 3.2's, and joins the `openIdConnectUrl` beside
		// it: both point at metadata a client would fetch to discover the
		// endpoints, and vertrag fetches neither — the token endpoint it uses
		// comes from the flow, or from the configuration.
		//
		// `deprecated` says the scheme is on its way out. Nothing chooses
		// between schemes on that basis, the same way nothing skips a
		// deprecated operation or parameter, so it is listed where those are.
		unsupported: []string{"bearerFormat", "openIdConnectUrl", "oauth2MetadataUrl", "deprecated"},
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
		// Dredd reports every constraint below as unsupported, because its own
		// parser does not act on them. vertrag passes them into the emitted
		// JSON Schema, where they are enforced against the response and drawn
		// from during generation — so repeating the warning would tell people a
		// constraint does nothing when it is the very thing being checked, and
		// the natural response to that is to delete it. Each was measured
		// against the validator rather than assumed.
		supported: []string{
			// Read, not ignored: a readOnly property is one the server sets,
			// so it is left out of a request and its requiredness with it; a
			// writeOnly one is left out of a response the same way. Reporting
			// them as unsupported told authors a constraint did nothing while
			// it was deciding what vertrag sent.
			"readOnly", "writeOnly", "$ref", "type", "enum", "const", "properties", "items", "required",
			"nullable", "oneOf", "allOf", "anyOf", "not", "additionalProperties",
			"default", "title", "description", "example",
			"multipleOf", "maximum", "exclusiveMaximum", "minimum", "exclusiveMinimum",
			"maxLength", "minLength", "pattern", "format",
			"maxItems", "minItems", "uniqueItems", "maxProperties", "minProperties"},
		// These genuinely are not acted on: they describe presentation, lineage
		// or intent rather than what a valid body looks like.
		unsupported: []string{"discriminator", "xml",
			"externalDocs", "deprecated"},
	}
	specParameterSchema = objectSpec{
		name:             "Schema Object",
		parameterVariant: true,
		// A parameter's constraints are acted on, though in `vertrag fuzz`
		// rather than in `run`: generation draws values from the bounds, and
		// the guard that decides whether a drawn value is really valid runs the
		// whole schema through the validator, so every keyword it enforces
		// decides what gets sent. `run` still sends the single example, which
		// is where these keywords remain inert — but a key that is honoured by
		// one command is not an unsupported key, and saying it is invites
		// deleting the constraint that fuzzing depends on.
		supported: []string{"$ref", "type", "enum", "const", "description", "title", "example",
			"multipleOf", "maximum", "exclusiveMaximum", "minimum", "exclusiveMinimum",
			"maxLength", "minLength", "pattern", "format",
			"maxItems", "minItems", "uniqueItems", "maxProperties", "minProperties",
			"properties", "items", "required", "nullable", "default",
			"oneOf", "allOf", "anyOf", "not", "additionalProperties"},
		unsupported: []string{"discriminator",
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

	root := d.Root
	if !root.IsMapping() {
		return out
	}

	out = append(out, d.validateKeys(root, d.openAPISpec())...)
	out = append(out, d.validateVersion(root.Get("openapi"))...)
	out = append(out, d.validateSurface(root)...)
	if d.dialectDiagnostic != nil {
		out = append(out, d.at(*d.dialectDiagnostic, root.Get("jsonSchemaDialect")))
	}
	out = append(out, d.reportDanglingReferences()...)

	out = append(out, d.validateObject(root.Get("info"), specInfo,
		func(info node) []annotation {
			var nested []annotation
			nested = append(nested, d.validateObject(info.Get("contact"), specContact, nil)...)
			nested = append(nested, d.validateObject(info.Get("license"), specLicense, nil)...)
			return nested
		})...)

	for _, server := range root.Get("servers").Items() {
		out = append(out, d.validateServer(server)...)
	}

	out = append(out, d.validatePaths(root.Get("paths"))...)
	out = append(out, d.validateWebhooks(root.Get("webhooks"))...)
	out = append(out, d.validateComponents(root.Get("components"))...)

	return out
}

// openAPISpec is the OpenAPI Object key list for the revision this document
// declares.
//
// 3.1 stopped requiring `paths` and required instead that a document carry at
// least one of `paths`, `webhooks` or `components` — which is what makes a
// description of nothing but webhooks a complete document. Demanding `paths` of
// one made it an error, and an error stops everything: such a document produced
// no transactions, and the only thing it was told was to add a field the
// specification had stopped asking for.
func (d *document) openAPISpec() objectSpec {
	if !d.atLeast(1) {
		return specOpenAPI
	}
	spec := specOpenAPI
	spec.required = []string{"openapi", "info"}
	return spec
}

// validateSurface reports a 3.1-or-later document that describes no API surface
// at all.
//
// The requirement `paths` used to carry, in the form 3.1 restated it: a
// document must hold at least one of `paths`, `webhooks` or `components`. It is
// an error for the same reason the missing `paths` was — there is nothing to
// run and nothing to check, so saying why is the only useful output — and it is
// checked here rather than through the required list because no single key is
// the required one.
func (d *document) validateSurface(root node) []annotation {
	if !d.atLeast(1) {
		return nil
	}
	for _, key := range []string{"paths", "webhooks", "components"} {
		if root.Get(key).Valid() {
			return nil
		}
	}
	return []annotation{d.at(annotation{
		class: "error",
		message: "'OpenAPI Object' carries none of 'paths', 'webhooks' or 'components', " +
			"so it describes no API at all",
	}, root)}
}

func (d *document) validateServer(server node) []annotation {
	return d.validateObject(server, specServer, func(s node) []annotation {
		var nested []annotation
		for _, variable := range s.Get("variables").Entries() {
			nested = append(nested, d.validateObject(variable.Value, specServerVariable, nil)...)
		}
		return nested
	})
}

// validatePaths checks the Paths Object and everything beneath it.
func (d *document) validatePaths(paths node) []annotation {
	if !paths.IsMapping() {
		return nil
	}

	var out []annotation
	for _, member := range paths.Entries() {
		key := member.Key.Str()

		// Every key of a Paths Object is a path template, and a path template
		// starts with a slash. Anything else is a key in the wrong place.
		if !strings.HasPrefix(key, "/") {
			if strings.HasPrefix(key, "x-") {
				continue
			}
			out = append(out, d.invalidKey("Paths Object", member.Key))
			continue
		}
		out = append(out, d.validatePathItem(member.Value)...)
	}
	return out
}

func (d *document) validatePathItem(pathItem node) []annotation {
	return d.validateObject(pathItem, d.pathItemSpec(), func(item node) []annotation {
		var out []annotation
		for _, parameter := range item.Get("parameters").Items() {
			out = append(out, d.validateParameter(parameter)...)
		}
		for _, member := range item.Entries() {
			if d.isOperationKey(member.Key.Str()) {
				out = append(out, d.validateOperation(member.Value)...)
			}
		}
		for _, member := range item.Get(additionalOperationsKey).Entries() {
			out = append(out, d.validateAdditionalOperation(member)...)
		}
		return out
	})
}

// pathItemSpec is the Path Item key list for the revision this document
// declares.
//
// `query` and `additionalOperations` are keys in the wrong place in a 3.0 or
// 3.1 document, and reporting them there is worth more to their author than
// quietly running requests the specification gave that document no way to ask
// for. So this is the one key list that depends on the version line.
func (d *document) pathItemSpec() objectSpec {
	if d.atLeast(2) {
		return specPathItem32
	}
	return specPathItem
}

// validateAdditionalOperation checks one entry of `additionalOperations`.
//
// The map key is the method, spelled the way it is to be sent. The
// specification forbids naming one that already has a field of its own, and the
// consequence of doing it anyway is worth stating rather than hiding: both
// operations are read, so the path ends up with two sets of transactions under
// one method, and the hooks that address them by name cannot tell which is
// which. It is a warning and not an error because the document is still
// runnable, and dropping one of the two silently would be the worse answer.
func (d *document) validateAdditionalOperation(member entry) []annotation {
	var out []annotation

	method := member.Key.Str()
	switch {
	case method == "":
	case d.isOperationKey(strings.ToLower(method)):
		out = append(out, d.at(annotation{
			class: "warning",
			message: fmt.Sprintf(
				"'Path Item Object' 'additionalOperations' names '%s', which has a field of "+
					"its own; the operations under both are run, so move this one to '%s'",
				method, strings.ToLower(method)),
		}, member.Key))
	case !methodTokenPattern.MatchString(method):
		// A method has to be a token to be sent at all: net/http refuses
		// anything else, and the refusal would otherwise arrive from the
		// transport, reading as though the network broke.
		out = append(out, d.at(annotation{
			class: "error",
			message: fmt.Sprintf(
				"'Path Item Object' 'additionalOperations' names '%s', which is not a "+
					"method that can be sent", method),
		}, member.Key))
	}

	return append(out, d.validateOperation(member.Value)...)
}

// methodTokenPattern is RFC 9110's token production, which a method name is one
// of. It is the same set of characters a header field name may use.
var methodTokenPattern = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)

func (d *document) validateOperation(operation node) []annotation {
	return d.validateObject(operation, specOperation, func(op node) []annotation {
		var out []annotation
		for _, parameter := range op.Get("parameters").Items() {
			out = append(out, d.validateParameter(parameter)...)
		}
		out = append(out, d.reportCollidingParameters(op.Get("parameters"))...)
		out = append(out, d.validateRequestBody(op.Get("requestBody"))...)
		out = append(out, d.validateResponses(op.Get("responses"))...)
		for _, server := range op.Get("servers").Items() {
			out = append(out, d.validateServer(server)...)
		}
		return out
	})
}

// reportCollidingParameters warns about one name used in both the path and the
// query.
//
// A URI template expands every occurrence of a variable from a single value, so
// `/collide/{id}{?id}` cannot carry a different id in each position: whichever
// was assigned last wins both, and the request goes to a path the description
// never described. There is no spelling for it in a template, so the honest
// answer is to say so rather than build a URL that looks deliberate.
func (d *document) reportCollidingParameters(parameters node) []annotation {
	inPath := map[string]bool{}
	for _, parameter := range parameters.Items() {
		resolved := d.Resolve(parameter)
		if resolved.Get("in").Str() == "path" {
			inPath[resolved.Get("name").Str()] = true
		}
	}

	var out []annotation
	for _, parameter := range parameters.Items() {
		resolved := d.Resolve(parameter)
		name := resolved.Get("name").Str()
		if resolved.Get("in").Str() != "query" || !inPath[name] {
			continue
		}
		out = append(out, d.at(annotation{
			class: "warning",
			message: fmt.Sprintf(
				"'Parameter Object' %q is declared in both the path and the query; "+
					"a URI template expands both from one value, so the path will carry the query's",
				name),
		}, resolved.Get("name")))
	}
	return out
}

// reportDanglingReferences warns about a `$ref` pointing at nothing.
//
// An unresolvable reference was entirely silent: no error, no warning, and the
// schema it should have supplied simply absent — so the response body was
// validated against nothing at all and the run passed while checking less than
// it appeared to. A typo in a pointer is a common thing to write and the worst
// possible thing to swallow, because the result looks exactly like success.
func (d *document) reportDanglingReferences() []annotation {
	var out []annotation

	for _, ref := range d.findReferences(d.Root) {
		target, local := d.Local(ref.Value)
		if !local {
			// A reference into another document. vertrag reads one file, so it
			// cannot follow this, and saying it is dangling would be wrong.
			//
			// A reference written through the document's own `$self` URI is not
			// one of these, however much it looks like one, which is why the
			// judgement is Local's and not a prefix test here.
			continue
		}
		if d.Pointer(target).Valid() {
			continue
		}
		out = append(out, d.at(annotation{
			class: "warning",
			message: fmt.Sprintf(
				"'$ref' %q resolves to nothing, so whatever it described is missing", target),
		}, ref))
	}
	return out
}

// findReferences collects every `$ref` scalar in the document.
func (d *document) findReferences(n node) []node {
	var out []node

	switch {
	case n.IsMapping():
		for _, member := range n.Entries() {
			if member.Key.Str() == "$ref" && member.Value.IsScalar() {
				out = append(out, member.Value)
				continue
			}
			out = append(out, d.findReferences(member.Value)...)
		}
	case n.IsSequence():
		for _, item := range n.Items() {
			out = append(out, d.findReferences(item)...)
		}
	}
	return out
}

func (d *document) validateParameter(parameter node) []annotation {
	return d.validateObject(parameter, specParameter, func(p node) []annotation {
		var out []annotation

		// A parameter travels in the path, the query string or a header.
		// Anything else is a place this implementation does not put values, so
		// the parameter would silently not be sent: a cookie, or 3.2's
		// `querystring`, which is not one parameter among others but the entire
		// query string given as a single value to be serialised through a media
		// type. Building one means form-encoding an object through its Encoding
		// Objects, which is work the request builder does for bodies and not
		// for URLs.
		if in := p.Get("in"); in.IsScalar() && in.Value != "path" && in.Value != "query" && in.Value != "header" {
			out = append(out, d.at(annotation{
				class:   "warning",
				message: fmt.Sprintf("'Parameter Object' 'in' '%s' is unsupported", in.Value),
			}, in))
		}

		// A header value carrying a carriage return or a line feed cannot be
		// sent at all: net/http refuses it, and rightly, since splitting a
		// header value across lines is how a request is forged. Caught here
		// because the failure otherwise arrives from the transport, reading as
		// though the server or the network broke — so the reader goes looking
		// at their server when the fault is in their document.
		if p.Get("in").Str() == "header" {
			if example, ok := schemaExample(p); ok {
				if text, isText := example.(string); isText && !sendableHeaderValue(text) {
					out = append(out, d.at(annotation{
						class: "error",
						message: fmt.Sprintf(
							"'Parameter Object' %q gives a header value containing a line break, "+
								"which cannot be sent",
							p.Get("name").Str()),
					}, p.Get("name")))
				}
			}
		}

		return append(out, d.validateSchema(p.Get("schema"), specParameterSchema)...)
	})
}

// sendableHeaderValue reports whether a value can travel as a header.
//
// Only the line breaks are refused. Bytes above ASCII are technically outside
// what the grammar permits and are sent by every client in practice, so
// rejecting them would fail documents that work.
func sendableHeaderValue(value string) bool {
	return !strings.ContainsAny(value, "\r\n")
}

func (d *document) validateRequestBody(body node) []annotation {
	return d.validateObject(body, specRequestBody, func(b node) []annotation {
		out := d.validateContent(b.Get("content"))

		// `required: true` says the server refuses a bodiless request. Every
		// body vertrag sends is built from a schema or an example, so when no
		// media type carries either, the requests for this operation go out
		// empty against an operation that promises empty is refused — and the
		// failures that produces would otherwise read as the server's fault.
		if b.Get("required").Bool() && !d.anyMediaTypeFillsABody(b.Get("content")) {
			out = append(out, d.at(annotation{
				class: "warning",
				message: "'Request Body Object' is required, but no media type carries " +
					"a schema or example to build a body from",
			}, b))
		}
		return out
	})
}

// anyMediaTypeFillsABody reports whether at least one media type gives the
// compiler something a request body can be built from.
func (d *document) anyMediaTypeFillsABody(content node) bool {
	for _, member := range content.Entries() {
		mediaType := d.Resolve(member.Value)
		if !mediaType.IsMapping() {
			continue
		}
		if mediaType.Get("schema").Valid() || mediaType.Get("example").Valid() ||
			mediaType.Get("examples").Valid() {
			return true
		}
	}
	return false
}

func (d *document) validateResponses(responses node) []annotation {
	if !responses.IsMapping() {
		return nil
	}

	var out []annotation
	for _, member := range responses.Entries() {
		key := member.Key.Str()
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
			}, member.Key))
		default:
			out = append(out, d.invalidKey("Responses Object", member.Key))
			continue
		}

		out = append(out, d.validateObject(member.Value, d.responseSpec(), func(response node) []annotation {
			var nested []annotation
			nested = append(nested, d.validateContent(response.Get("content"))...)
			for _, header := range response.Get("headers").Entries() {
				nested = append(nested, d.validateObject(header.Value, specHeader, nil)...)
			}
			// A malformed Link Object gets the same key-level diagnostics
			// every other object gets, now that they are acted on.
			for _, link := range response.Get("links").Entries() {
				nested = append(nested, d.validateObject(link.Value, specLink, nil)...)
			}
			return nested
		})...)
	}
	return out
}

// responseSpec is the Response Object key list for the revision this document
// declares.
//
// 3.2 stopped requiring `description`, and the difference is not cosmetic: a
// missing required property is an error, an error stops everything, and a valid
// 3.2 document that leaves a 204 undescribed produced no transactions at all —
// the whole run lost to a field the document was right to omit.
func (d *document) responseSpec() objectSpec {
	if !d.atLeast(2) {
		return specResponse
	}
	spec := specResponse
	spec.required = nil
	return spec
}

func (d *document) validateContent(content node) []annotation {
	if !content.IsMapping() {
		return nil
	}

	var out []annotation
	for _, member := range content.Entries() {
		// Resolved, because 3.2 allows a `content` entry to reference a Media
		// Type Object held in `components.mediaTypes`. Left unresolved, such an
		// entry looks like a media type with no schema and no example, which is
		// exactly what an unshared media type looks like when its author forgot
		// one — so nothing is reported and nothing is checked.
		out = append(out, d.validateObject(d.Resolve(member.Value), specMediaType, func(mediaType node) []annotation {
			var nested []annotation
			nested = append(nested, d.validateSchema(mediaType.Get("schema"), specSchema)...)

			examples := mediaType.Get("examples").Entries()
			for _, example := range examples {
				nested = append(nested, d.validateObject(example.Value, specExample, nil)...)
			}
			// Dredd warns here that only the first example is used and the
			// rest are ignored. vertrag sends them all, one exchange each, so
			// there is nothing to warn about.
			return nested
		})...)
	}
	return out
}

func (d *document) validateComponents(components node) []annotation {
	return d.validateObject(components, specComponents, func(c node) []annotation {
		var out []annotation
		for _, schema := range c.Get("schemas").Entries() {
			out = append(out, d.validateSchema(schema.Value, specSchema)...)
		}
		for _, parameter := range c.Get("parameters").Entries() {
			out = append(out, d.validateParameter(parameter.Value)...)
		}
		for _, body := range c.Get("requestBodies").Entries() {
			out = append(out, d.validateRequestBody(body.Value)...)
		}
		for _, header := range c.Get("headers").Entries() {
			out = append(out, d.validateObject(header.Value, specHeader, nil)...)
		}
		for _, example := range c.Get("examples").Entries() {
			out = append(out, d.validateObject(example.Value, specExample, nil)...)
		}
		for _, scheme := range c.Get("securitySchemes").Entries() {
			out = append(out, d.validateObject(scheme.Value, specSecurityScheme, nil)...)
		}
		// 3.1's shared Path Items, which are where a `webhooks` entry
		// idiomatically points. Nothing checked them: a webhook or a path
		// written as a reference is resolved when it is read, so its target's
		// mistakes reached the compiler unreported, and a shared Path Item
		// nothing references yet was never looked at at all.
		for _, pathItem := range c.Get("pathItems").Entries() {
			out = append(out, d.validatePathItem(pathItem.Value)...)
		}
		// 3.2's shared media types. Checked here rather than only where they
		// are referenced, for the reason this whole walk exists: a document's
		// problems should not depend on which corners of it happened to be
		// needed, and a shared media type nothing references yet is the corner
		// most likely to be wrong.
		for _, mediaType := range c.Get("mediaTypes").Entries() {
			out = append(out, d.validateObject(mediaType.Value, specMediaType, func(m node) []annotation {
				return d.validateSchema(m.Get("schema"), specSchema)
			})...)
		}
		return out
	})
}

// validateSchema checks a Schema Object and recurses through its subschemas.
func (d *document) validateSchema(schema node, spec objectSpec) []annotation {
	if !schema.IsMapping() {
		return nil
	}

	// In 3.1 a Schema Object is JSON Schema 2020-12, which acts on far more
	// keywords than 3.0's subset and permits unrecognised ones outright as
	// annotations. Applying 3.0's list would warn about `exclusiveMinimum`,
	// `const` and `prefixItems` — all of them valid, all of them acted on.
	if d.modernSchemas {
		return nil
	}
	// A schema that is only a reference is a Reference Object, not a Schema
	// Object, and is checked where it is defined rather than at every use.
	// Inside a parameter the reference is not followed at all, so `$ref` is
	// reported there as an unsupported key instead.
	if schema.Get("$ref").IsScalar() && !spec.parameterVariant {
		return nil
	}

	out := d.validateKeys(schema, spec)

	// Subschemas are always full Schema Objects, even when reached from a
	// parameter, so the stricter parameter rules do not propagate downwards.
	// A schema under additionalProperties is one of them: it is converted and
	// enforced like any other, so it is checked here rather than warned about
	// — this used to claim it was unsupported while the validator acted on it.
	if additional := schema.Get("additionalProperties"); additional.IsMapping() {
		out = append(out, d.validateSchema(additional, specSchema)...)
	}
	for _, property := range schema.Get("properties").Entries() {
		out = append(out, d.validateSchema(property.Value, specSchema)...)
	}
	if items := schema.Get("items"); items.IsMapping() {
		out = append(out, d.validateSchema(items, specSchema)...)
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		for _, item := range schema.Get(keyword).Items() {
			out = append(out, d.validateSchema(item, specSchema)...)
		}
	}
	return out
}

// validateVersion reports a version string the specification does not permit.
//
// `openapi` MUST be major.minor.patch, so `openapi: 3.0` is malformed. It is
// still READ as OpenAPI 3 — the alternative, which the reference pattern
// takes, is to not recognise the document at all and tell its author that API
// Blueprint is unsupported, which is the least useful thing a tool can say
// about an OpenAPI 3 file. So the document is parsed and the version is
// reported for what it is: something to fix, not something that stops the run.
func (d *document) validateVersion(version node) []annotation {
	if !version.IsScalar() {
		return nil
	}
	text := strings.TrimSpace(version.Str())
	if text == "" || versionPattern.MatchString(text) {
		return nil
	}
	return []annotation{d.at(annotation{
		class: "warning",
		message: fmt.Sprintf("'OpenAPI Object' 'openapi' is %q; the specification requires "+
			"major.minor.patch, as in %q. It is being read as OpenAPI 3.", text, text+".0"),
	}, version)}
}

// versionPattern is the specification's own: major.minor.patch.
var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// validateObject checks an object's keys and then its children.
func (d *document) validateObject(n node, spec objectSpec, children func(node) []annotation) []annotation {
	if !n.IsMapping() {
		return nil
	}
	// An object that is a reference is a Reference Object, whatever position it
	// occupies. Checking it against the host object's key list would report its
	// `$ref` as invalid; the target is checked where it is defined.
	//
	// A Path Item is the exception: it lists `$ref` as unsupported, so the
	// reference there is a diagnostic rather than something to follow.
	if n.Get("$ref").IsScalar() && !contains(spec.unsupported, "$ref") {
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

	for _, member := range n.Entries() {
		key := member.Key.Str()
		switch {
		case supported[key]:
		case unsupported[key]:
			out = append(out, d.at(annotation{
				class:   "warning",
				message: fmt.Sprintf("'%s' contains unsupported key '%s'", spec.name, key),
			}, member.Key))
		case strings.HasPrefix(key, "x-") && !spec.noExtensions:
			// Specification extensions are explicitly allowed to be anything.
		default:
			out = append(out, d.invalidKey(spec.name, member.Key))
		}
	}

	for _, required := range spec.required {
		if !n.Get(required).Valid() {
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
		message: fmt.Sprintf("'%s' contains invalid key '%s'", objectName, key.Str()),
	}, key)
}

// at attaches the source range of the node a diagnostic is about.
func (d *document) at(a annotation, n node) annotation {
	if !n.Valid() {
		return a
	}
	a.line, a.column, a.endLine, a.endCol = d.Span(n)
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
			a.message = fmt.Sprintf("%s (%d occurrences)", a.message, count)
		}
		out = append(out, a)
	}
	return out
}

// isUnsupportedWarning selects the warnings that are collapsed with a count.
//
// Repeating "this document uses something not acted on" once per occurrence
// would drown everything else in a document that uses the key throughout.
func isUnsupportedWarning(a annotation) bool {
	return a.class == "warning" && strings.Contains(a.message, "contains unsupported key")
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
