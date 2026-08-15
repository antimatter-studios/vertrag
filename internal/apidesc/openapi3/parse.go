package openapi3

import (
	"strings"

	"github.com/antimatter-studios/vertrag/internal/refract"
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
	for _, element := range annotationElements(deduplicateUnsupported(diagnostics)) {
		parseResult.Append(element)
	}

	api := refract.Named("category")
	api.AddClass("api")
	if title := doc.root.get("info").get("title").str(); title != "" {
		api.SetTitle(title)
	}

	for _, path := range doc.root.get("paths").entries() {
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
	pathItem := d.resolve(path.value)
	if !pathItem.isMapping() {
		return nil
	}

	resource := refract.Named("resource")
	resource.SetAttr("href", refract.String(path.key.str()))

	pathParameters := d.parseParameters(pathItem.get("parameters"))

	for _, member := range pathItem.entries() {
		if !isHTTPMethod(member.key.str()) {
			continue
		}
		transition := d.parseOperation(path.key.str(), member, pathParameters)
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
	operation := member.value
	if !operation.isMapping() {
		return nil
	}

	transition := refract.Named("transition")
	if summary := operation.get("summary").str(); summary != "" {
		transition.SetTitle(summary)
	}

	// Operation parameters override path parameters of the same name and
	// location, which is what the specification asks for.
	params := pathParameters.merge(d.parseParameters(operation.get("parameters")))

	// Query parameters are appended to the href as a template expression, so
	// the compiler expands the ones that have example values and drops the
	// rest.
	if query := params.queryNames(); len(query) > 0 {
		transition.SetAttr("href", refract.String(path+"{?"+strings.Join(query, ",")+"}"))
	}
	if hrefVariables := params.hrefVariables(); hrefVariables != nil {
		transition.SetAttr("hrefVariables", hrefVariables)
	}

	requests := d.parseRequestBody(operation.get("requestBody"))
	responses := d.parseResponses(operation.get("responses"))

	method := strings.ToUpper(member.key.str())
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
}

type header struct {
	name  string
	value string
}

// buildTransactions pairs every request with every response.
//
// The cartesian product is deliberate and matches the reference: a document
// offering two request content types and three responses describes six
// exchanges, and each is a separate test.
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
			httpRequest := refract.Named("httpRequest")
			httpRequest.SetAttr("method", refract.String(method))

			headers := make([]header, 0, len(request.headers)+len(headerParams)+1)
			// Accept advertises what the response promises, so the server is
			// asked for the representation the document is being tested against.
			if response.contentType != "" {
				headers = append(headers, header{"Accept", response.contentType})
			}
			if request.contentType != "" {
				headers = append(headers, header{"Content-Type", request.contentType})
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
