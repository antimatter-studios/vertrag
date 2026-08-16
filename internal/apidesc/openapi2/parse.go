package openapi2

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/antimatter-studios/vertrag/internal/refract"
)

// Parse reads a Swagger 2.0 document into API Elements.
func Parse(source []byte) (*refract.Element, error) {
	doc, err := parseDocument(source)
	if err != nil {
		// A document that will not parse is reported through the result rather
		// than as a Go error: it is the document's problem, and the caller
		// shows it the way it shows every other problem with a description.
		failure := refract.Text("annotation", yamlErrorMessage(source, err))
		failure.AddClass("error")
		return refract.Named("parseResult", failure), nil
	}

	parseResult := refract.Named("parseResult")

	// An error anywhere stops everything, as it does for OpenAPI 3: a document
	// the parser could not fully read cannot be trusted to say what a server
	// should do.
	diagnostics := doc.validate()
	if errs := errorsOnly(diagnostics); len(errs) > 0 {
		for _, element := range annotationElements(errs) {
			parseResult.Append(element)
		}
		return parseResult, nil
	}
	for _, element := range annotationElements(diagnostics) {
		parseResult.Append(element)
	}

	api := refract.Named("category")
	api.AddClass("api")
	if title := doc.root.get("info").get("title").str(); title != "" {
		api.SetTitle(title)
	}

	basePath := doc.root.get("basePath").str()
	consumes := stringList(doc.root.get("consumes"))
	produces := stringList(doc.root.get("produces"))

	for _, path := range doc.root.get("paths").entries() {
		if resource := doc.parsePathItem(path, basePath, consumes, produces); resource != nil {
			api.Append(resource)
		}
	}

	parseResult.Append(api)
	return parseResult, nil
}

// statusCodePattern matches an exact HTTP status code.
var statusCodePattern = regexp.MustCompile(`^\d{3}$`)

// parsePathItem turns one entry of the Paths Object into a resource.
//
// The href carries the document's basePath, because Swagger states the path
// relative to it and the request has to go to the whole thing.
func (d *document) parsePathItem(path entry, basePath string, consumes, produces []string) *refract.Element {
	pathItem := path.value
	if !pathItem.isMapping() {
		return nil
	}

	href := joinPath(basePath, path.key.str())

	resource := refract.Named("resource")
	resource.SetAttr("href", refract.String(href))

	pathParameters := d.parseParameters(pathItem.get("parameters"))

	for _, member := range pathItem.entries() {
		if !isHTTPMethod(member.key.str()) {
			continue
		}
		if transition := d.parseOperation(href, member, pathParameters, consumes, produces); transition != nil {
			resource.Append(transition)
		}
	}

	// A query parameter declared for the whole path belongs on the resource's
	// own href, and so appears in every transaction name beneath it.
	if query := pathParameters.queryNames(); len(query) > 0 {
		resource.SetAttr("href", refract.String(href+"{?"+strings.Join(query, ",")+"}"))
	}
	if hrefVariables := pathParameters.hrefVariables(); hrefVariables != nil {
		resource.SetAttr("hrefVariables", hrefVariables)
	}
	return resource
}

// parseOperation turns one HTTP method into a transition carrying its
// transactions.
func (d *document) parseOperation(href string, member entry, pathParameters *parameters, consumes, produces []string) *refract.Element {
	operation := member.value
	if !operation.isMapping() {
		return nil
	}

	transition := refract.Named("transition")
	if summary := operation.get("summary").str(); summary != "" {
		transition.SetTitle(summary)
	}

	params := pathParameters.merge(d.parseParameters(operation.get("parameters")))

	if query := params.queryNames(); len(query) > 0 {
		transition.SetAttr("href", refract.String(href+"{?"+strings.Join(query, ",")+"}"))
	}
	if hrefVariables := params.hrefVariables(); hrefVariables != nil {
		transition.SetAttr("hrefVariables", hrefVariables)
	}

	// An operation narrows the document's content types rather than adding to
	// them, so its own list replaces the global one entirely — including an
	// empty list, which is how a document says "this operation produces
	// nothing", and which is why presence is tested rather than length.
	if own := operation.get("consumes"); own.valid() {
		consumes = stringList(own)
	}
	if own := operation.get("produces"); own.valid() {
		produces = stringList(own)
	}

	requests := d.buildRequests(params, consumes)
	responses := d.parseResponses(operation.get("responses"), produces)

	method := strings.ToUpper(member.key.str())
	for _, transaction := range buildTransactions(method, requests, responses, params.headers()) {
		transition.Append(transaction)
	}
	return transition
}

// message is a request or response under construction.
type message struct {
	// accept is what the request asks for; contentType is what the message
	// carries. They differ when a response declares its own content type.
	accept      string
	contentType string
	body        string
	hasBody     bool
	schema      string
	statusCode  string
	headers     []header
}

type header struct {
	name  string
	value string
}

// buildRequests produces one request per consumed content type.
//
// A document that accepts JSON and XML describes two distinct exchanges, and
// each is tested. With nothing declared there is one request and no
// Content-Type — the document has not said what it accepts.
func (d *document) buildRequests(params *parameters, consumes []string) []message {
	body, hasBody := d.requestBody(params)

	if len(consumes) == 0 {
		return []message{{body: body, hasBody: hasBody}}
	}

	requests := make([]message, 0, len(consumes))
	for _, contentType := range consumes {
		request := message{contentType: contentType, body: body, hasBody: hasBody}

		// A multipart request carries its fields in the body rather than as a
		// JSON document, so the body is assembled from the form parameters and
		// the boundary the content type names.
		if boundary := multipartBoundary(contentType); boundary != "" {
			if assembled, ok := multipartBody(params, boundary); ok {
				request.body, request.hasBody = assembled, true
			}
		}

		requests = append(requests, request)
	}
	return requests
}

// multipartBoundary reads the boundary out of a multipart content type.
func multipartBoundary(contentType string) string {
	if !strings.HasPrefix(strings.ToLower(contentType), "multipart/") {
		return ""
	}
	for _, part := range strings.Split(contentType, ";")[1:] {
		name, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if found && strings.EqualFold(name, "boundary") {
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

// multipartBody assembles the form parameters into a multipart payload.
//
// Line endings are CRLF throughout, because that is what the format requires
// and what a server parsing it will expect.
func multipartBody(params *parameters, boundary string) (string, bool) {
	fields := params.in("formData")
	if len(fields) == 0 {
		return "", false
	}

	var body strings.Builder
	for _, field := range fields {
		value := ""
		if field.hasValue {
			value = stringifyScalar(field.value)
		}
		fmt.Fprintf(&body, "--%s\r\nContent-Disposition: form-data; name=%q\r\n\r\n%s\r\n",
			boundary, field.name, value)
	}
	fmt.Fprintf(&body, "\r\n--%s--\r\n", boundary)

	return body.String(), true
}

// requestBody renders the `in: body` parameter, which is how Swagger carries a
// payload.
func (d *document) requestBody(params *parameters) (string, bool) {
	for _, param := range params.in("body") {
		if !param.schema.valid() {
			continue
		}
		if value, ok := d.generateValue(param.schema, nil); ok {
			if body, ok := renderBody(value); ok {
				return body, true
			}
		}
	}
	return "", false
}

// parseResponses turns a Responses Object into the responses to expect.
//
// Two rules here are surprising enough to state. Only a JSON media type from
// `produces` is used, and a document producing only XML or plain text gets no
// content type at all — Swagger lists what a server CAN return rather than what
// it will, and the reference only commits to one it can generate a body for.
// And `default` responses are ignored outright: a response with no status code
// says nothing about what to assert.
func (d *document) parseResponses(n node, produces []string) []message {
	if !n.isMapping() {
		return nil
	}

	contentType := jsonMediaType(produces)

	var responses []message
	for _, member := range n.entries() {
		key := member.key.str()
		if !statusCodePattern.MatchString(key) {
			continue
		}

		response := d.resolve(member.value)
		if !response.isMapping() {
			continue
		}

		// A response's own content-type header replaces the produced type
		// rather than joining it: the document is saying what THIS response
		// returns, which is more specific than what the operation can produce.
		headers, override := d.parseResponseHeaders(response.get("headers"))

		schema := response.get("schema")
		converted := ""
		if schema.valid() {
			if encoded, ok := d.convertSchema(schema); ok {
				converted = encoded
			}
		}

		// Examples decide how many exchanges this response describes: one per
		// media type demonstrated. Without them the response is described once,
		// as the first thing the operation produces.
		variants := responseVariants(response.get("examples"), contentType, override)

		for _, variant := range variants {
			msg := message{accept: variant.accept, contentType: variant.contentType, headers: headers, schema: converted}
			msg.statusCode = key

			if variant.example.valid() {
				if body, ok := renderBody(scalarValue(variant.example)); ok {
					msg.body, msg.hasBody = body, true
				}
			} else if schema.valid() && isJSONMediaType(variant.contentType) {
				// Without a JSON media type there is nothing to render the
				// generated value into, so the response carries its schema but
				// no example body.
				if value, ok := d.generateValue(schema, nil); ok {
					if body, ok := renderBody(value); ok {
						msg.body, msg.hasBody = body, true
					}
				}
			}

			responses = append(responses, msg)
		}
	}
	return responses
}

// variant is one media type a response is described in.
//
// accept and contentType are separate because a response's declared
// content-type header changes what comes back without changing what the request
// asks for: the operation still produces what it says it produces.
type variant struct {
	accept      string
	contentType string
	example     node
}

// responseVariants lists the exchanges a response describes.
func responseVariants(examples node, produced, override string) []variant {
	if override != "" {
		return []variant{{accept: produced, contentType: override, example: examples.get(override)}}
	}

	if entries := examples.entries(); len(entries) > 0 {
		out := make([]variant, 0, len(entries))
		for _, member := range entries {
			mediaType := member.key.str()
			out = append(out, variant{accept: mediaType, contentType: mediaType, example: member.value})
		}
		return out
	}

	return []variant{{accept: produced, contentType: produced}}
}

// parseResponseHeaders reads a Headers Object, separating out a declared
// content-type, which replaces the produced media type rather than being sent
// alongside it.
func (d *document) parseResponseHeaders(n node) ([]header, string) {
	var headers []header
	override := ""

	for _, member := range n.entries() {
		name := member.key.str()
		value := ""
		if example, ok := schemaExample(member.value); ok {
			value = stringifyScalar(example)
		}
		if strings.EqualFold(name, "content-type") {
			override = value
			continue
		}
		headers = append(headers, header{name: name, value: value})
	}
	return headers, override
}

// buildTransactions pairs every request with every response.
func buildTransactions(method string, requests, responses []message, headerParams []header) []*refract.Element {
	if len(requests) == 0 {
		requests = []message{{}}
	}
	if len(responses) == 0 {
		responses = []message{{}}
	}

	var transactions []*refract.Element
	for _, response := range responses {
		for _, request := range requests {
			httpRequest := refract.Named("httpRequest")
			httpRequest.SetAttr("method", refract.String(method))

			// Content-Type before Accept: the reference builds the request's
			// own headers first and prepends what the response asks for after.
			headers := make([]header, 0, len(headerParams)+2)
			if request.contentType != "" {
				headers = append(headers, header{"Content-Type", request.contentType})
			}
			if response.accept != "" {
				headers = append(headers, header{"Accept", response.accept})
			}
			for _, param := range headerParams {
				if !containsHeader(headers, param.name) {
					headers = append(headers, param)
				}
			}
			setHeaders(httpRequest, headers)
			if request.hasBody {
				httpRequest.Append(bodyAsset(request.body, request.contentType))
			}

			httpResponse := refract.Named("httpResponse")
			if response.statusCode != "" {
				httpResponse.SetAttr("statusCode", refract.String(response.statusCode))
			}
			responseHeaders := make([]header, 0, len(response.headers)+1)
			if response.contentType != "" {
				responseHeaders = append(responseHeaders, header{"Content-Type", response.contentType})
			}
			responseHeaders = append(responseHeaders, response.headers...)
			setHeaders(httpResponse, responseHeaders)
			if response.hasBody {
				httpResponse.Append(bodyAsset(response.body, response.contentType))
			}
			if response.schema != "" {
				httpResponse.Append(schemaAsset(response.schema))
			}

			transactions = append(transactions,
				refract.Named("httpTransaction", httpRequest, httpResponse))
		}
	}
	return transactions
}

func containsHeader(headers []header, name string) bool {
	for _, h := range headers {
		if strings.EqualFold(h.name, name) {
			return true
		}
	}
	return false
}

func setHeaders(element *refract.Element, headers []header) {
	if len(headers) == 0 {
		return
	}
	httpHeaders := refract.Named("httpHeaders")
	for _, h := range headers {
		httpHeaders.Append(refract.Member(h.name, refract.String(h.value)))
	}
	element.SetAttr("headers", httpHeaders)
}

func bodyAsset(body, contentType string) *refract.Element {
	asset := refract.Text("asset", body)
	asset.AddClass("messageBody")
	if contentType != "" {
		asset.SetAttr("contentType", refract.String(contentType))
	}
	return asset
}

func schemaAsset(schema string) *refract.Element {
	asset := refract.Text("asset", schema)
	asset.AddClass("messageBodySchema")
	asset.SetAttr("contentType", refract.String("application/schema+json"))
	return asset
}

// joinPath puts basePath in front of a path without doubling the separator.
//
// `basePath: /` is common and means "no prefix"; concatenating it blindly would
// send every request to a doubled slash, which some servers route differently
// and others reject.
func joinPath(basePath, path string) string {
	basePath = strings.TrimSuffix(basePath, "/")
	if basePath == "" {
		return path
	}
	return basePath + path
}

// jsonMediaType picks the media type a response is described in: the first JSON
// one offered, or none at all.
func jsonMediaType(produces []string) string {
	for _, mediaType := range produces {
		if isJSONMediaType(mediaType) {
			return mediaType
		}
	}
	return ""
}

// isJSONMediaType covers application/json and the `+json` suffix conventions.
func isJSONMediaType(mediaType string) bool {
	base := mediaType
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = base[:i]
	}
	base = strings.ToLower(strings.TrimSpace(base))
	if !strings.HasPrefix(base, "application/") {
		return false
	}
	subtype := strings.TrimPrefix(base, "application/")
	return subtype == "json" || strings.HasSuffix(subtype, "+json")
}

// stringList reads a sequence of strings, which Swagger uses for consumes,
// produces and schemes.
func stringList(n node) []string {
	var out []string
	for _, item := range n.items() {
		if value := item.str(); value != "" {
			out = append(out, value)
		}
	}
	return out
}
