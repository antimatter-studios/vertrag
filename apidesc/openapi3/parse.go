package openapi3

import (
	"encoding/json"
	"strings"

	"github.com/antimatter-studios/vertrag/refract"
)

// Parse reads an OpenAPI 3 document into API Elements.
func Parse(source []byte) (*refract.Element, error) {
	doc, err := parseDocument(source)
	if err != nil {
		return nil, err
	}

	parseResult := refract.Named("parseResult")

	// Diagnostics come first: the compiler reads the parse result's own
	// annotations before it walks anything else, and appends its own after.
	diagnostics := doc.validate()
	sortByPosition(diagnostics)

	// An error anywhere in the document stops everything. The result is the
	// errors alone — no warnings, and no transactions, not even from the parts
	// that were fine.
	//
	// That looks harsh, and it is the reference's behaviour. The reasoning
	// holds up: a document the parser could not fully understand cannot be
	// trusted to describe what a server should do, and testing an API against a
	// half-read description would report failures that say nothing about the
	// server. Warnings go too, because they describe a document that is no
	// longer being acted on.
	if errors := errorsOnly(diagnostics); len(errors) > 0 {
		for _, element := range annotationElements(errors) {
			parseResult.Append(element)
		}
		return parseResult, nil
	}

	for _, element := range annotationElements(deduplicateUnsupported(diagnostics)) {
		parseResult.Append(element)
	}

	api := refract.Named("category")
	api.AddClass("api")
	if title := doc.Root.Get("info").Get("title").Str(); title != "" {
		api.SetTitle(title)
	}

	for _, path := range doc.Root.Get("paths").Entries() {
		if resource := doc.parsePathItem(path); resource != nil {
			api.Append(resource)
		}
	}

	parseResult.Append(api)
	return parseResult, nil
}

// parsePathItem turns one entry of the Paths Object into a resource.
//
// The path template becomes the resource's href; each operation under it
// becomes a transition. The resource is left untitled, so the compiler names it
// after its href — which is why OpenAPI transaction names contain the path.
func (d *document) parsePathItem(path entry) *refract.Element {
	pathItem := d.Resolve(path.Value)
	if !pathItem.IsMapping() {
		return nil
	}

	resource := refract.Named("resource")
	resource.SetAttr("href", refract.String(path.Key.Str()))

	// A path item's summary titles the resource, and the compiler prefers a
	// title over an href when naming transactions. So adding a summary to a
	// path renames every transaction under it — and therefore every hook that
	// addresses one by name.
	if summary := pathItem.Get("summary").Str(); summary != "" {
		resource.SetTitle(summary)
	}

	pathParameters := d.parseParameters(pathItem.Get("parameters"))

	for _, member := range pathItem.Entries() {
		if !isHTTPMethod(member.Key.Str()) {
			continue
		}
		transition := d.parseOperation(path.Key.Str(), member, pathParameters)
		if transition != nil {
			resource.Append(transition)
		}
	}

	// Path-level parameters belong to the resource even when no operation
	// declares any of its own, because the compiler cascades resource
	// parameters down into every request beneath it.
	if hrefVariables := pathParameters.hrefVariables(); hrefVariables != nil {
		resource.SetAttr("hrefVariables", hrefVariables)
	}

	return resource
}

// parseOperation turns one HTTP method of a Path Item into a transition
// carrying its transactions.
func (d *document) parseOperation(path string, member entry, pathParameters *parameters) *refract.Element {
	operation := member.Value
	if !operation.IsMapping() {
		return nil
	}

	transition := refract.Named("transition")
	if summary := operation.Get("summary").Str(); summary != "" {
		transition.SetTitle(summary)
	}

	// Operation parameters override path parameters of the same name and
	// location, which is what the specification asks for.
	params := pathParameters.merge(d.parseParameters(operation.Get("parameters")))

	// Query parameters are appended to the href as a template expression, so
	// the compiler expands the ones that have example values and drops the
	// rest.
	if query := params.queryNames(); len(query) > 0 {
		transition.SetAttr("href", refract.String(path+"{?"+strings.Join(query, ",")+"}"))
	}
	if hrefVariables := params.hrefVariables(); hrefVariables != nil {
		transition.SetAttr("hrefVariables", hrefVariables)
	}

	requests := d.parseRequestBody(operation.Get("requestBody"))
	responses := d.parseResponses(operation.Get("responses"))

	method := strings.ToUpper(member.Key.Str())
	for _, transaction := range buildTransactions(method, requests, responses, params.headers()) {
		transition.Append(transaction)
	}

	return transition
}

// message is a request or response under construction, before it is paired with
// its counterpart into a transaction.
type message struct {
	contentType string
	body        string
	hasBody     bool
	schema      string
	statusCode  string
	headers     []header

	// example is the name the document gave this body in an Examples Object,
	// empty for a body that was not named. It decides which request goes with
	// which response: see pairs.
	example string
}

type header struct {
	name  string
	value string

	// schema is the JSON Schema the document gave this header's value, empty
	// when it gave none or gave one vertrag will not act on. Only a response
	// header carries it: a request header's value is vertrag's own doing, so
	// there is nothing to check it against.
	schema string
}

// buildTransactions pairs each request with the responses it belongs with.
//
// The product is deliberate: a document offering two request content types and
// three responses describes six exchanges, and each is a separate test. Named
// examples narrow it — see pairs.
func buildTransactions(method string, requests, responses []message, headerParams []header) []*refract.Element {
	if len(requests) == 0 {
		requests = []message{{}}
	}
	if len(responses) == 0 {
		responses = []message{{}}
	}

	var transactions []*refract.Element
	for _, request := range requests {
		for _, response := range responses {
			if !pairs(request, response) {
				continue
			}

			httpRequest := refract.Named("httpRequest")
			httpRequest.SetAttr("method", refract.String(method))

			headers := make([]header, 0, len(request.headers)+len(headerParams)+1)
			// Accept advertises what the response promises, so the server is
			// asked for the representation the document is being tested against.
			if response.contentType != "" {
				headers = append(headers, header{name: "Accept", value: response.contentType})
			}
			if request.contentType != "" {
				headers = append(headers, header{name: "Content-Type", value: request.contentType})
			}
			// A parameter must not displace a header the message already
			// carries; the message's own is the more specific statement.
			for _, param := range headerParams {
				if !containsHeader(headers, param.name) {
					headers = append(headers, param)
				}
			}
			setHeaders(httpRequest, headers)
			if request.hasBody {
				httpRequest.Append(bodyAsset(request.body, request.contentType))
			}
			if request.schema != "" {
				httpRequest.Append(schemaAsset(request.schema))
			}

			httpResponse := refract.Named("httpResponse")
			if response.statusCode != "" {
				httpResponse.SetAttr("statusCode", refract.String(response.statusCode))
			}
			responseHeaders := make([]header, 0, len(response.headers)+1)
			if response.contentType != "" {
				responseHeaders = append(responseHeaders, header{name: "Content-Type", value: response.contentType})
			}
			responseHeaders = append(responseHeaders, response.headers...)
			setHeaders(httpResponse, responseHeaders)
			if response.hasBody {
				httpResponse.Append(bodyAsset(response.body, response.contentType))
			}
			if response.schema != "" {
				httpResponse.Append(schemaAsset(response.schema))
			}
			if asset := headerSchemasAsset(responseHeaders); asset != nil {
				httpResponse.Append(asset)
			}

			transactions = append(transactions,
				refract.Named("httpTransaction", httpRequest, httpResponse))
		}
	}
	return transactions
}

// pairs reports whether a request and a response describe the same exchange.
//
// A document naming its examples is saying which request produces which
// response: a request named "rejected" carrying an invalid payload goes with
// the 400 named "rejected", not with the 200. Pairing those by the product
// instead would generate a test asserting that the invalid request succeeds,
// which is guaranteed to fail and tells nobody anything.
//
// A name on only one side constrains nothing, so those still pair with
// everything — that is the case of a document that names its request examples
// but describes its responses by schema alone.
func pairs(request, response message) bool {
	if request.example == "" || response.example == "" {
		return true
	}
	return request.example == response.example
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

// headerSchemasAsset carries the schemas a Response Object gave its headers.
//
// One asset holding an object of header name to schema rather than one asset per
// header, because an element holds a single asset of any given class and the
// compiler finds assets by class. Nothing in Dredd reads it; it exists so the
// header-schema check has something to check against.
func headerSchemasAsset(headers []header) *refract.Element {
	schemas := newOrderedMap()
	for _, h := range headers {
		if h.schema != "" {
			schemas.Set(h.name, json.RawMessage(h.schema))
		}
	}
	if schemas.Len() == 0 {
		return nil
	}
	encoded, ok := encodeToString(schemas)
	if !ok {
		return nil
	}

	asset := refract.Text("asset", encoded)
	asset.AddClass("messageHeadersSchema")
	// A map of schemas is not itself a schema, so it is described as the JSON
	// object it is rather than borrowing the body schema's media type.
	asset.SetAttr("contentType", refract.String("application/json"))
	return asset
}
