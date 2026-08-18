package compile

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/antimatter-studios/vertrag/refract"
	"github.com/antimatter-studios/vertrag/validate"
)

// MediaTypeAPIBlueprint is the one media type that needs special handling here.
// API Blueprint groups request/response pairs into numbered examples, a concept
// API Elements does not carry, so it has to be reconstructed from source maps.
const MediaTypeAPIBlueprint = "text/vnd.apiblueprint"

// Compile turns a parsed API description into executable transactions.
func Compile(mediaType string, root *refract.Element, filename string) Result {
	result := Result{
		MediaType:    mediaType,
		Transactions: []Transaction{},
		Annotations:  []Annotation{},
	}

	// Parser annotations are the parse result's own direct children, not every
	// annotation anywhere in the tree — a nested one belongs to the element that
	// holds it, and is reported through that element instead.
	for _, element := range childrenNamed(root, "annotation") {
		result.Annotations = append(result.Annotations, compileAnnotation(element))
	}

	for _, relevant := range findRelevantTransactions(mediaType, root) {
		transaction, annotations := compileTransaction(
			mediaType, filename, relevant.element, relevant.exampleNo)
		if transaction != nil {
			result.Transactions = append(result.Transactions, *transaction)
		}
		result.Annotations = append(result.Annotations, annotations...)
	}

	return result
}

// relevantTransaction is an httpTransaction element paired with the API
// Blueprint example number it belongs to, where that concept applies.
type relevantTransaction struct {
	element   *refract.Element
	exampleNo int
}

// findRelevantTransactions selects the transactions to test, in document order.
//
// For every format but API Blueprint that is simply all of them. API Blueprint
// allows several request/response pairs per example, of which Dredd tests only
// the first, so the pairs have to be grouped into examples and the rest
// dropped — and the example number survives into the transaction name, which
// hooks address transactions by.
func findRelevantTransactions(mediaType string, root *refract.Element) []relevantTransaction {
	var relevant []relevantTransaction

	for _, transition := range root.FindRecursive("resource", "transition") {
		transactions := childrenNamed(transition, "httpTransaction")

		if mediaType != MediaTypeAPIBlueprint {
			for _, element := range transactions {
				relevant = append(relevant, relevantTransaction{element: element})
			}
			continue
		}

		exampleNumbers := detectTransactionExampleNumbers(transition)
		hasMoreExamples := maxInt(exampleNumbers) > 1

		// Walk the pairs and keep only the first of each example: an example
		// number that differs from the previous one marks a new example.
		previousExampleNo := 0
		for i, element := range transactions {
			exampleNo := 0
			if i < len(exampleNumbers) {
				exampleNo = exampleNumbers[i]
			}
			if exampleNo != previousExampleNo {
				entry := relevantTransaction{element: element}
				if hasMoreExamples {
					entry.exampleNo = exampleNo
				}
				relevant = append(relevant, entry)
			}
			previousExampleNo = exampleNo
		}
	}

	return relevant
}

func childrenNamed(element *refract.Element, name string) []*refract.Element {
	var out []*refract.Element
	for _, child := range element.ContentChildren() {
		if child.Name == name {
			out = append(out, child)
		}
	}
	return out
}

func maxInt(values []int) int {
	max := 0
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	return max
}

// compileTransaction builds one transaction and any annotations it provoked.
func compileTransaction(mediaType, filename string, element *refract.Element, exampleNo int) (*Transaction, []Annotation) {
	origin := compileOrigin(mediaType, filename, element, exampleNo)
	name := compileTransactionName(origin)

	requestElement := element.Child("httpRequest")
	request, annotations := compileRequest(requestElement)

	// Annotations are stamped with the transaction they came from so a
	// diagnostic can be traced back to the operation that provoked it.
	originCopy := origin
	for i := range annotations {
		annotations[i].Name = name
		annotations[i].Origin = &originCopy
	}

	if request == nil {
		return nil, annotations
	}

	response := compileResponse(element.Child("httpResponse"))

	// A schema vertrag cannot compile validates nothing, and validation is
	// the wrong place to say so: failing a transaction there would blame the
	// server for the description's problem, and passing it silently — which
	// is what happened — tells the reader their body was checked when nothing
	// checked it. So it is said here, once, about the document, naming the
	// operation, in the same place as every other diagnostic about the
	// description.
	for _, unusable := range unusableSchemas(*request, response) {
		annotations = append(annotations, Annotation{
			Type: "warning", Component: "apiDescription", Message: unusable,
			Name: name, Origin: &originCopy,
		})
	}

	return &Transaction{
		Request:  *request,
		Response: response,
		Name:     name,
		Origin:   origin,
		// The identifier sits on the transition rather than the transaction,
		// because one operation can describe several exchanges and they all
		// share it.
		OperationID: element.FindParent("transition").Attr("operationId").String(),
		Links:       compileLinks(element.Child("httpResponse")),
		Security:    compileSecurity(element.FindParent("transition")),
		Tags:        compileTags(element.FindParent("transition")),
	}, annotations
}

// unusableSchemas reports the schemas on a transaction that cannot be
// compiled, and so would check nothing.
func unusableSchemas(request Request, response Response) []string {
	var out []string

	if err := validate.Usable(json.RawMessage(request.Schema)); err != nil {
		out = append(out, fmt.Sprintf(
			"the request body schema cannot be read, so nothing generated from it "+
				"would be shaped by it: %v", err))
	}
	if err := validate.Usable(json.RawMessage(response.Schema)); err != nil {
		out = append(out, fmt.Sprintf(
			"the response body schema cannot be read, so the response body will not "+
				"be validated against it: %v", err))
	}

	names := make([]string, 0, len(response.HeaderSchemas))
	for header := range response.HeaderSchemas {
		names = append(names, header)
	}
	sort.Strings(names)
	for _, header := range names {
		if err := validate.Usable(response.HeaderSchemas[header]); err != nil {
			out = append(out, fmt.Sprintf(
				"the schema for response header %q cannot be read, so its value will "+
					"not be validated: %v", header, err))
		}
	}
	return out
}

// compileTags reads the operation's tags off the transition, where the parser
// left them. They sit there rather than on the transaction because every
// exchange an operation describes shares them.
func compileTags(transition *refract.Element) []string {
	if transition == nil {
		return nil
	}
	container := transition.Attr("tags")
	if container == nil {
		return nil
	}

	var out []string
	for _, child := range container.ContentChildren() {
		if tag := child.String(); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// compileRequest builds the request, or nil when no concrete URI could be
// derived — in which case the transaction is not testable and is dropped.
func compileRequest(element *refract.Element) (*Request, []Annotation) {
	uri, annotations := compileURI(element)

	// Source map locations are dropped from URI annotations. The reference does
	// the same: the location it would report points at the href, which is not
	// where the reader can fix the problem.
	for i := range annotations {
		annotations[i].Location = nil
	}

	if uri.uri == "" {
		return nil, annotations
	}

	headers := compileHeaders(element.Attr("headers"))
	request := &Request{
		Method:   element.Attr("method").String(),
		URI:      uri.uri,
		Headers:  headers,
		Body:     compileBody(element.ChildWithClass("asset", "messageBody"), hasMultipartBody(headers)),
		Template: uri.template,
		Parameters: append(append(uri.parameters,
			headerParameters(element.Attr("headers"))...),
			cookieParameters(element.Attr(refract.CookiesAttribute))...),
	}
	if schema := element.ChildWithClass("asset", "messageBodySchema"); schema != nil {
		request.Schema = schema.String()
	}
	return request, annotations
}

// headerParameters picks out the headers that are parameters in their own right.
//
// A header carrying a schema was declared as a parameter by the description; one
// without is either a header the message itself states, such as Content-Type, or
// came from a parser that does not record parameter schemas. Only the first has
// constraints to generate against, so only the first is reported.
func headerParameters(element *refract.Element) []Parameter {
	var out []Parameter
	for _, member := range element.ContentChildren() {
		if member.Kind != refract.ContentMember {
			continue
		}
		schema := member.Value.Attr(refract.SchemaAttribute).String()
		if schema == "" {
			continue
		}
		name, _ := member.Key.StringValue()
		value := member.Value.String()
		out = append(out, Parameter{
			In:       InHeader,
			Name:     name,
			Schema:   schema,
			Value:    value,
			HasValue: value != "",
		})
	}
	return out
}

// cookieParameters reads the cookie parameters a parser recorded beside the
// request's headers.
//
// They are parameters like any other, and are listed as such so that the
// checks and probes which vary a parameter by name and location can reach a
// cookie — a session identifier or a tenant selector is exactly the kind of
// input a handler forgets to validate, and it was previously not sent at all,
// let alone varied.
func cookieParameters(element *refract.Element) []Parameter {
	var out []Parameter
	for _, member := range element.ContentChildren() {
		if member.Kind != refract.ContentMember {
			continue
		}
		name, _ := member.Key.StringValue()
		value := member.Value.String()
		out = append(out, Parameter{
			In:       InCookie,
			Name:     name,
			Schema:   member.Value.Attr(refract.SchemaAttribute).String(),
			Value:    value,
			HasValue: value != "",
		})
	}
	return out
}

func compileResponse(element *refract.Element) Response {
	status := element.Attr("statusCode").String()
	if status == "" {
		status = "200"
	}

	headers := compileHeaders(element.Attr("headers"))
	response := Response{
		Status:  status,
		Headers: headers,
		Body:    compileBody(element.ChildWithClass("asset", "messageBody"), hasMultipartBody(headers)),
	}
	if schema := element.ChildWithClass("asset", "messageBodySchema"); schema != nil {
		response.Schema = schema.String()
	}
	if schemas := element.ChildWithClass("asset", "messageHeadersSchema"); schemas != nil {
		response.HeaderSchemas = compileHeaderSchemas(schemas.String())
	}
	return response
}

// compileHeaderSchemas reads the asset holding one JSON Schema per response
// header.
//
// A payload that will not parse yields no schemas rather than an error. The
// asset is vertrag's own doing, so a malformed one is a bug here, not a fault of
// the server under test — and failing a server over it would send the reader
// looking in entirely the wrong place.
func compileHeaderSchemas(payload string) map[string]json.RawMessage {
	var schemas map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &schemas); err != nil || len(schemas) == 0 {
		return nil
	}
	return schemas
}

func compileHeaders(element *refract.Element) []Header {
	headers := []Header{}
	if element == nil {
		return headers
	}
	for _, member := range element.ContentChildren() {
		if member.Kind != refract.ContentMember {
			continue
		}
		name, _ := member.Key.StringValue()
		headers = append(headers, Header{Name: name, Value: member.Value.String()})
	}
	return headers
}

func hasMultipartBody(headers []Header) bool {
	for _, h := range headers {
		if strings.EqualFold(h.Name, "content-type") &&
			strings.Contains(strings.ToLower(h.Value), "multipart") {
			return true
		}
	}
	return false
}

// compileBody reads a message body asset.
//
// Multipart bodies get their line endings normalised to CRLF. Hand-written
// API Blueprint bodies routinely use bare newlines, which are not valid in a
// multipart payload and make servers reject an otherwise correct request.
func compileBody(element *refract.Element, isMultipart bool) string {
	if element == nil {
		return ""
	}
	body := element.String()
	if !isMultipart {
		return body
	}
	return strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
}

// compileOrigin locates a transaction within the API description.
func compileOrigin(mediaType, filename string, element *refract.Element, exampleNo int) Origin {
	apiElement := element.FindParentWithClass("api")
	resourceGroup := element.FindParentWithClass("resourceGroup")
	resource := element.FindParent("resource")
	transition := element.FindParent("transition")
	request := element.Child("httpRequest")
	response := element.Child("httpResponse")

	apiName := apiElement.MetaValue("title")
	if apiName == "" {
		apiName = filename
	}

	resourceName := resource.MetaValue("title")
	if resourceName == "" {
		resourceName = resource.Attr("href").String()
	}

	actionName := transition.MetaValue("title")
	if actionName == "" {
		actionName = request.Attr("method").String()
	}

	return Origin{
		Filename:          filename,
		APIName:           apiName,
		ResourceGroupName: resourceGroup.MetaValue("title"),
		ResourceName:      resourceName,
		ActionName:        actionName,
		ExampleName:       compileOriginExampleName(mediaType, response, exampleNo),
	}
}

// compileOriginExampleName names the specific request/response pair.
//
// API Blueprint numbers its examples, so the number is the name. Every other
// format distinguishes pairs by what they return, so the status code and
// content type are used instead — which is why OpenAPI transaction names end
// in something like "200 > application/json".
func compileOriginExampleName(mediaType string, response *refract.Element, exampleNo int) string {
	if mediaType == MediaTypeAPIBlueprint {
		if exampleNo != 0 {
			return fmt.Sprintf("Example %d", exampleNo)
		}
		return ""
	}

	status := response.Attr("statusCode").String()
	if status == "" {
		status = "200"
	}

	var contentType string
	for _, h := range compileHeaders(response.Attr("headers")) {
		if strings.EqualFold(h.Name, "content-type") {
			contentType = h.Value
			break
		}
	}

	segments := []string{status}
	if contentType != "" {
		segments = append(segments, contentType)
	}
	return strings.Join(segments, " > ")
}

// compileTransactionName joins the non-empty parts of an origin into the name
// hooks use to address a transaction.
func compileTransactionName(origin Origin) string {
	parts := []string{
		origin.APIName,
		origin.ResourceGroupName,
		origin.ResourceName,
		origin.ActionName,
		origin.ExampleName,
	}
	nonEmpty := parts[:0]
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, " > ")
}

// compileAnnotation converts a parser annotation element into a diagnostic.
func compileAnnotation(element *refract.Element) Annotation {
	annotation := Annotation{
		Component: "apiDescriptionParser",
		Message:   element.String(),
		Location:  compileLocation(element.Attr("sourceMap")),
	}
	if classes := element.Classes(); len(classes) > 0 {
		annotation.Type = classes[0]
	}
	return annotation
}

// compileLocation reduces a source map to a [[startLine, startColumn],
// [endLine, endColumn]] pair, or nil when the shape is not what is expected.
func compileLocation(sourceMap *refract.Element) [][]int {
	start := firstLeaf(sourceMap)
	end := lastLeaf(sourceMap)
	if start == nil || end == nil {
		return nil
	}

	startLine, ok1 := intAttr(start, "line")
	startColumn, ok2 := intAttr(start, "column")
	endLine, ok3 := intAttr(end, "line")
	endColumn, ok4 := intAttr(end, "column")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil
	}
	return [][]int{{startLine, startColumn}, {endLine, endColumn}}
}

// firstLeaf and lastLeaf walk three levels down, matching the reference's
// `sourceMap.first.first.first` and `.last.last.last`.
func firstLeaf(element *refract.Element) *refract.Element {
	for i := 0; i < 3; i++ {
		children := element.ContentChildren()
		if len(children) == 0 {
			return nil
		}
		element = children[0]
	}
	return element
}

func lastLeaf(element *refract.Element) *refract.Element {
	for i := 0; i < 3; i++ {
		children := element.ContentChildren()
		if len(children) == 0 {
			return nil
		}
		element = children[len(children)-1]
	}
	return element
}

func intAttr(element *refract.Element, name string) (int, bool) {
	attr := element.Attr(name)
	if attr == nil || attr.Kind != refract.ContentPrimitive {
		return 0, false
	}
	n, ok := attr.Primitive.(float64)
	return int(n), ok
}
