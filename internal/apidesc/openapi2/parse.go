package openapi2

import (
	"regexp"
	"strings"

	"github.com/antimatter-studios/vertrag/internal/refract"
)

// Parse reads a Swagger 2.0 document into API Elements.
func Parse(source []byte) (*refract.Element, error) {
	doc, err := parseDocument(source)
	if err != nil {
		return nil, err
	}

	parseResult := refract.Named("parseResult")

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

	href := basePath + path.key.str()

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
	// them, so its own list replaces the global one entirely.
	if own := stringList(operation.get("consumes")); len(own) > 0 {
		consumes = own
	}
	if own := stringList(operation.get("produces")); len(own) > 0 {
		produces = own
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
		requests = append(requests, message{contentType: contentType, body: body, hasBody: hasBody})
	}
	return requests
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
// Only the first produced content type is used. Swagger lists what a server
// CAN return, not what it will, and the alternatives describe the same
// response rather than different ones — unlike `consumes`, where each entry is
// a different request to make.
func (d *document) parseResponses(n node, produces []string) []message {
	if !n.isMapping() {
		return nil
	}

	contentType := ""
	if len(produces) > 0 {
		contentType = produces[0]
	}

	var responses []message
	for _, member := range n.entries() {
		key := member.key.str()
		if !statusCodePattern.MatchString(key) && key != "default" {
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
			msg := message{contentType: variant.contentType, headers: headers, schema: converted}
			if statusCodePattern.MatchString(key) {
				msg.statusCode = key
			}

			if variant.example.valid() {
				if body, ok := renderBody(scalarValue(variant.example)); ok {
					msg.body, msg.hasBody = body, true
				}
			} else if schema.valid() {
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
type variant struct {
	contentType string
	example     node
}

// responseVariants lists the exchanges a response describes.
func responseVariants(examples node, produced, override string) []variant {
	if override != "" {
		// An explicit content-type header settles it: one exchange, in that
		// type, whatever the examples are keyed by.
		return []variant{{contentType: override, example: examples.get(override)}}
	}

	if entries := examples.entries(); len(entries) > 0 {
		out := make([]variant, 0, len(entries))
		for _, member := range entries {
			out = append(out, variant{contentType: member.key.str(), example: member.value})
		}
		return out
	}

	return []variant{{contentType: produced}}
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
			if response.contentType != "" {
				headers = append(headers, header{"Accept", response.contentType})
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
