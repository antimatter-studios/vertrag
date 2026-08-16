package compile

import (
	"testing"

	"github.com/antimatter-studios/vertrag/refract"
)

// buildAPI assembles a minimal API Elements tree, the way a format parser would.
func buildAPI(resources ...*refract.Element) *refract.Element {
	api := refract.Named("category", resources...)
	api.AddClass("api")
	api.SetTitle("Test API")
	return refract.Named("parseResult", api)
}

func transaction(status string) *refract.Element {
	request := refract.Named("httpRequest")
	request.SetAttr("method", refract.String("GET"))

	response := refract.Named("httpResponse")
	response.SetAttr("statusCode", refract.String(status))

	return refract.Named("httpTransaction", request, response)
}

func resource(href, title string, transitions ...*refract.Element) *refract.Element {
	element := refract.Named("resource", transitions...)
	element.SetAttr("href", refract.String(href))
	if title != "" {
		element.SetTitle(title)
	}
	return element
}

func transition(title string, transactions ...*refract.Element) *refract.Element {
	element := refract.Named("transition", transactions...)
	if title != "" {
		element.SetTitle(title)
	}
	return element
}

// TestTransactionName pins the name hooks address a transaction by. Every part
// of this is load-bearing: changing any of it silently disconnects hook files.
func TestTransactionName(t *testing.T) {
	root := buildAPI(resource("/things", "", transition("List", transaction("200"))))

	result := Compile("application/vnd.oai.openapi", root, "api.yml")
	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1", len(result.Transactions))
	}

	got := result.Transactions[0]
	if want := "Test API > /things > List > 200"; got.Name != want {
		t.Errorf("name = %q, want %q", got.Name, want)
	}
	if got.Origin.ResourceName != "/things" {
		t.Errorf("an untitled resource should be named by its href, got %q", got.Origin.ResourceName)
	}
	if got.Origin.Filename != "api.yml" {
		t.Errorf("filename = %q, want api.yml", got.Origin.Filename)
	}
}

// TestResourceTitleWinsOverHref pins the rule that adding a summary to a path
// renames every transaction beneath it.
func TestResourceTitleWinsOverHref(t *testing.T) {
	root := buildAPI(resource("/things", "Things Collection", transition("List", transaction("200"))))

	result := Compile("application/vnd.oai.openapi", root, "")
	if want := "Test API > Things Collection > List > 200"; result.Transactions[0].Name != want {
		t.Errorf("name = %q, want %q", result.Transactions[0].Name, want)
	}
}

// TestActionNameFallsBackToMethod pins that an operation with no summary is
// named by its HTTP method.
func TestActionNameFallsBackToMethod(t *testing.T) {
	root := buildAPI(resource("/things", "", transition("", transaction("200"))))

	result := Compile("application/vnd.oai.openapi", root, "")
	if want := "Test API > /things > GET > 200"; result.Transactions[0].Name != want {
		t.Errorf("name = %q, want %q", result.Transactions[0].Name, want)
	}
}

// TestExampleNameCarriesContentType pins the trailing segment of OpenAPI names.
func TestExampleNameCarriesContentType(t *testing.T) {
	request := refract.Named("httpRequest")
	request.SetAttr("method", refract.String("GET"))

	headers := refract.Named("httpHeaders",
		refract.Member("Content-Type", refract.String("application/json")))
	response := refract.Named("httpResponse")
	response.SetAttr("statusCode", refract.String("200"))
	response.SetAttr("headers", headers)

	root := buildAPI(resource("/things", "", transition("List",
		refract.Named("httpTransaction", request, response))))

	result := Compile("application/vnd.oai.openapi", root, "")
	if want := "Test API > /things > List > 200 > application/json"; result.Transactions[0].Name != want {
		t.Errorf("name = %q, want %q", result.Transactions[0].Name, want)
	}
}

// TestMissingStatusCodeDefaultsTo200 pins the fallback that makes an OpenAPI
// `default` response behave as a 200.
func TestMissingStatusCodeDefaultsTo200(t *testing.T) {
	request := refract.Named("httpRequest")
	request.SetAttr("method", refract.String("GET"))

	root := buildAPI(resource("/things", "", transition("List",
		refract.Named("httpTransaction", request, refract.Named("httpResponse")))))

	result := Compile("application/vnd.oai.openapi", root, "")
	if result.Transactions[0].Response.Status != "200" {
		t.Errorf("status = %q, want 200", result.Transactions[0].Response.Status)
	}
}

// TestRequiredParameterWithoutExampleDropsTransaction pins that a URI which
// cannot be made concrete yields diagnostics instead of a guessed request.
func TestRequiredParameterWithoutExampleDropsTransaction(t *testing.T) {
	value := refract.New("string")
	member := refract.Member("id", value)
	member.SetAttr("typeAttributes", refract.Array(refract.String("required")))

	things := resource("/things/{id}", "", transition("Read", transaction("200")))
	things.SetAttr("hrefVariables", refract.Named("hrefVariables", member))

	result := Compile("application/vnd.oai.openapi", buildAPI(things), "")

	if len(result.Transactions) != 0 {
		t.Errorf("transactions = %d, want 0: the URI cannot be built", len(result.Transactions))
	}
	if len(result.Annotations) == 0 {
		t.Fatal("expected diagnostics explaining why")
	}
	want := "Required URI parameter 'id' has no example or default value."
	if result.Annotations[0].Message != want {
		t.Errorf("annotation = %q, want %q", result.Annotations[0].Message, want)
	}
	// Diagnostics are stamped with the transaction they came from, so a reader
	// can trace one back to the operation that provoked it.
	if result.Annotations[0].Name == "" || result.Annotations[0].Origin == nil {
		t.Error("a transaction-level annotation should carry its name and origin")
	}
}

func TestParameterExampleExpandsURI(t *testing.T) {
	member := refract.Member("id", refract.String("abc"))
	member.SetAttr("typeAttributes", refract.Array(refract.String("required")))

	things := resource("/things/{id}", "", transition("Read", transaction("200")))
	things.SetAttr("hrefVariables", refract.Named("hrefVariables", member))

	result := Compile("application/vnd.oai.openapi", buildAPI(things), "")
	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1", len(result.Transactions))
	}
	if got := result.Transactions[0].Request.URI; got != "/things/abc" {
		t.Errorf("uri = %q, want /things/abc", got)
	}
}

// TestEnumParameterUsesFirstValue pins the fallback that makes an enum usable
// without an explicit example.
func TestEnumParameterUsesFirstValue(t *testing.T) {
	value := refract.New("string")
	value.SetAttr("enumerations", refract.Array(refract.String("alpha"), refract.String("beta")))

	member := refract.Member("kind", value)
	member.SetAttr("typeAttributes", refract.Array(refract.String("required")))

	things := resource("/things/{kind}", "", transition("Read", transaction("200")))
	things.SetAttr("hrefVariables", refract.Named("hrefVariables", member))

	result := Compile("application/vnd.oai.openapi", buildAPI(things), "")
	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1", len(result.Transactions))
	}
	if got := result.Transactions[0].Request.URI; got != "/things/alpha" {
		t.Errorf("uri = %q, want /things/alpha", got)
	}
}

// TestExampleNotInEnumIsRejected pins that a contradictory description is
// reported rather than obeyed.
func TestExampleNotInEnumIsRejected(t *testing.T) {
	value := refract.String("gamma")
	value.SetAttr("enumerations", refract.Array(refract.String("alpha")))

	member := refract.Member("kind", value)
	things := resource("/things/{kind}", "", transition("Read", transaction("200")))
	things.SetAttr("hrefVariables", refract.Named("hrefVariables", member))

	result := Compile("application/vnd.oai.openapi", buildAPI(things), "")
	if len(result.Annotations) == 0 {
		t.Fatal("expected a diagnostic")
	}
	want := "URI parameter 'kind' example value is not one of enum values."
	if result.Annotations[0].Message != want {
		t.Errorf("annotation = %q, want %q", result.Annotations[0].Message, want)
	}
}

func TestMultipartBodyGetsCRLFLineEndings(t *testing.T) {
	headers := refract.Named("httpHeaders",
		refract.Member("Content-Type", refract.String("multipart/form-data; boundary=x")))

	body := refract.Text("asset", "--x\nContent-Type: text/plain\n\nvalue\n--x--")
	body.AddClass("messageBody")

	request := refract.Named("httpRequest", body)
	request.SetAttr("method", refract.String("POST"))
	request.SetAttr("headers", headers)

	response := refract.Named("httpResponse")
	response.SetAttr("statusCode", refract.String("200"))

	root := buildAPI(resource("/upload", "", transition("Send",
		refract.Named("httpTransaction", request, response))))

	result := Compile("application/vnd.oai.openapi", root, "")
	got := result.Transactions[0].Request.Body
	if want := "--x\r\nContent-Type: text/plain\r\n\r\nvalue\r\n--x--"; got != want {
		t.Errorf("body = %q, want CRLF line endings %q", got, want)
	}
}

// TestAnUnreadableHeaderSchemaAssetCarriesNothing pins that a payload only
// vertrag could have written badly never becomes a verdict about the server.
//
// The asset is produced by vertrag's own parsers, so a malformed one is a bug in
// this repository. Reporting it as a failure would send the reader hunting
// through their API for a problem that is not there.
func TestAnUnreadableHeaderSchemaAssetCarriesNothing(t *testing.T) {
	for _, payload := range []string{`{not json`, `{}`, `[]`} {
		schemas := refract.Text("asset", payload)
		schemas.AddClass("messageHeadersSchema")

		request := refract.Named("httpRequest")
		request.SetAttr("method", refract.String("GET"))

		response := refract.Named("httpResponse", schemas)
		response.SetAttr("statusCode", refract.String("200"))

		root := buildAPI(resource("/things", "", transition("List",
			refract.Named("httpTransaction", request, response))))

		result := Compile("application/vnd.oai.openapi", root, "")
		if got := result.Transactions[0].Response.HeaderSchemas; got != nil {
			t.Errorf("payload %q yielded %v, want no schemas", payload, got)
		}
	}
}

// TestParserAnnotationsComeFirst pins the ordering the reporters rely on.
func TestParserAnnotationsComeFirst(t *testing.T) {
	parserAnnotation := refract.Text("annotation", "something about the document")
	parserAnnotation.AddClass("warning")

	api := refract.Named("category", resource("/things", "", transition("List", transaction("200"))))
	api.AddClass("api")
	root := refract.Named("parseResult", parserAnnotation, api)

	result := Compile("application/vnd.oai.openapi", root, "")
	if len(result.Annotations) != 1 {
		t.Fatalf("annotations = %d, want 1", len(result.Annotations))
	}
	if result.Annotations[0].Component != "apiDescriptionParser" {
		t.Errorf("component = %q, want apiDescriptionParser", result.Annotations[0].Component)
	}
	if result.Annotations[0].Type != "warning" {
		t.Errorf("type = %q, want warning", result.Annotations[0].Type)
	}
}

// TestNestedAnnotationsAreNotParserAnnotations pins that only the parse result's
// own children count.
func TestNestedAnnotationsAreNotParserAnnotations(t *testing.T) {
	nested := refract.Text("annotation", "buried")
	nested.AddClass("warning")

	things := resource("/things", "", transition("List", transaction("200")))
	things.Append(nested)

	result := Compile("application/vnd.oai.openapi", buildAPI(things), "")
	for _, annotation := range result.Annotations {
		if annotation.Message == "buried" {
			t.Error("an annotation nested in the tree must not be reported as a parser annotation")
		}
	}
}

func TestEmptyDocumentCompilesToNothing(t *testing.T) {
	result := Compile("application/vnd.oai.openapi", refract.Named("parseResult"), "")
	if len(result.Transactions) != 0 || len(result.Annotations) != 0 {
		t.Errorf("an empty document should yield nothing, got %d transactions and %d annotations",
			len(result.Transactions), len(result.Annotations))
	}
	if result.MediaType != "application/vnd.oai.openapi" {
		t.Errorf("mediaType = %q", result.MediaType)
	}
}

func TestTransactionOrderFollowsDocument(t *testing.T) {
	root := buildAPI(
		resource("/first", "", transition("A", transaction("200"))),
		resource("/second", "", transition("B", transaction("201"))),
	)

	result := Compile("application/vnd.oai.openapi", root, "")
	if len(result.Transactions) != 2 {
		t.Fatalf("transactions = %d, want 2", len(result.Transactions))
	}
	// Order is the order tests run in, so it is part of the contract.
	if result.Transactions[0].Request.URI != "/first" || result.Transactions[1].Request.URI != "/second" {
		t.Errorf("transactions out of document order: %q then %q",
			result.Transactions[0].Request.URI, result.Transactions[1].Request.URI)
	}
}
