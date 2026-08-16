package compile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/refract"
)

// parameterMember builds the hrefVariables member a format parser produces for
// a parameter, carrying its example and the schema its value must satisfy.
func parameterMember(name, example, schema string) *refract.Element {
	value := refract.String(example)
	if schema != "" {
		value.SetAttr(refract.SchemaAttribute, refract.String(schema))
	}
	member := refract.Member(name, value)
	member.SetAttr("typeAttributes", refract.Array(refract.String("required")))
	return member
}

// readTransaction compiles a resource whose href carries the given template and
// parameters, and returns the one transaction it yields.
func readTransaction(t *testing.T, href string, members ...*refract.Element) Transaction {
	t.Helper()

	things := resource(href, "", transition("Read", transaction("200")))
	things.SetAttr("hrefVariables", refract.Named("hrefVariables", members...))

	result := Compile("application/vnd.oai.openapi", buildAPI(things), "")
	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1", len(result.Transactions))
	}
	return result.Transactions[0]
}

// TestParameterSchemasReachTheCompiledRequest pins the carriage that makes
// parameter generation possible at all.
//
// A parameter's schema is read at the parse stage, used to pick an example, and
// was then dropped — so by the time anything could generate from it, the only
// thing left of `{type: integer, minimum: 1}` was the string "7" in a URI.
// Nothing fails loudly when this stops working: fuzzing simply finds no
// parameters to probe and reports a clean run.
func TestParameterSchemasReachTheCompiledRequest(t *testing.T) {
	const idSchema = `{"type":"integer","minimum":1}`
	const limitSchema = `{"type":"integer","maximum":100}`

	compiled := readTransaction(t, "/things/{id}{?limit}",
		parameterMember("id", "7", idSchema),
		parameterMember("limit", "10", limitSchema))

	if compiled.Request.URI != "/things/7?limit=10" {
		t.Fatalf("uri = %q", compiled.Request.URI)
	}

	if len(compiled.Request.Parameters) != 2 {
		t.Fatalf("parameters = %d, want 2", len(compiled.Request.Parameters))
	}

	// Where each one travels is read from the template's operator, since that
	// is the only place the compiled request records the difference.
	for i, want := range []Parameter{
		{In: "path", Name: "id", Schema: idSchema, Value: "7", HasValue: true},
		{In: "query", Name: "limit", Schema: limitSchema, Value: "10", HasValue: true},
	} {
		if got := compiled.Request.Parameters[i]; got != want {
			t.Errorf("parameter %d = %+v, want %+v", i, got, want)
		}
	}
}

// TestParametersAreAbsentFromTheCompiledJSON pins the oracle's comparison
// surface.
//
// Everything the compiler emits is compared against Dredd's output byte for
// byte. Dredd substitutes a parameter's example into the URI and then has no
// further use for it, so a parameter list in the JSON would make every
// transaction differ from the reference — the failure would be real, and would
// be about a field that exists only for vertrag's own generation.
func TestParametersAreAbsentFromTheCompiledJSON(t *testing.T) {
	compiled := readTransaction(t, "/things/{id}",
		parameterMember("id", "7", `{"type":"integer"}`))

	encoded, err := json.Marshal(compiled)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	for _, absent := range []string{"parameters", "template", refract.SchemaAttribute} {
		if text := strings.ToLower(string(encoded)); strings.Contains(text, strings.ToLower(absent)) {
			t.Errorf("%q appears in the compiled JSON, which the oracle compares against Dredd:\n%s",
				absent, encoded)
		}
	}
}

// TestHeaderParametersAreDistinguishedFromMessageHeaders pins which headers are
// generated for.
//
// A request's headers are a mixture: Content-Type is a statement the message
// makes about itself, while X-Tenant may be a declared parameter with a schema
// of its own. Only the second has constraints a server can fail to enforce, and
// generating values for the first would fuzz the content negotiation instead of
// the API.
func TestHeaderParametersAreDistinguishedFromMessageHeaders(t *testing.T) {
	tenant := refract.String("acme")
	tenant.SetAttr(refract.SchemaAttribute, refract.String(`{"type":"string","minLength":3}`))

	request := refract.Named("httpRequest")
	request.SetAttr("method", refract.String("GET"))
	request.SetAttr("headers", refract.Named("httpHeaders",
		refract.Member("Content-Type", refract.String("application/json")),
		refract.Member("X-Tenant", tenant)))

	things := resource("/things", "", transition("Read",
		refract.Named("httpTransaction", request, refract.Named("httpResponse"))))

	result := Compile("application/vnd.oai.openapi", buildAPI(things), "")
	parameters := result.Transactions[0].Request.Parameters

	if len(parameters) != 1 {
		t.Fatalf("parameters = %+v, want only the declared one", parameters)
	}
	want := Parameter{In: "header", Name: "X-Tenant",
		Schema: `{"type":"string","minLength":3}`, Value: "acme", HasValue: true}
	if parameters[0] != want {
		t.Errorf("parameter = %+v, want %+v", parameters[0], want)
	}
}

// TestSubstitutingTheOriginalValueReproducesTheURI is the property the whole
// substitution rests on: a generated request differs from the compiled one in
// exactly one parameter.
//
// If re-expansion produced a different URI for the same values — a dropped query
// parameter, a differently encoded segment — then every finding would be
// suspect, because the request that failed would differ from the compiled one in
// ways nobody asked for.
func TestSubstitutingTheOriginalValueReproducesTheURI(t *testing.T) {
	compiled := readTransaction(t, "/things/{id}{?limit,q}",
		parameterMember("id", "7", `{"type":"integer"}`),
		parameterMember("limit", "10", `{"type":"integer"}`),
		parameterMember("q", "a b", `{"type":"string"}`))

	for _, parameter := range compiled.Request.Parameters {
		value, _ := parameter.Value.(string)
		again, err := compiled.Request.SetParameter(parameter, value)
		if err != nil {
			t.Fatalf("%s: %v", parameter.Name, err)
		}
		if again.URI != compiled.Request.URI {
			t.Errorf("re-expanding %q gave %q, want the compiled %q",
				parameter.Name, again.URI, compiled.Request.URI)
		}
	}
}

// TestSubstitutedValuesAreEncodedForTheirPosition pins the reason substitution
// goes through the template rather than through the URI text.
//
// A value is the one the server sees after decoding, not the characters in the
// request, and the two differ for anything outside the unreserved set. Editing
// the URI as a string would send the value raw, and a space or an ampersand
// would then change the shape of the request rather than the parameter's value.
func TestSubstitutedValuesAreEncodedForTheirPosition(t *testing.T) {
	compiled := readTransaction(t, "/things/{id}{?q}",
		parameterMember("id", "7", `{"type":"string"}`),
		parameterMember("q", "plain", `{"type":"string"}`))

	for _, test := range []struct {
		name  string
		which string
		value string
		want  string
	}{
		{"a space in the path", "id", "a b", "/things/a%20b?q=plain"},
		{"an ampersand in the query", "q", "a&b", "/things/7?q=a%26b"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parameter := findParameter(t, compiled.Request, test.which)
			request, err := compiled.Request.SetParameter(parameter, test.value)
			if err != nil {
				t.Fatalf("substituting: %v", err)
			}
			if request.URI != test.want {
				t.Errorf("uri = %q, want %q", request.URI, test.want)
			}
		})
	}
}

// TestSubstitutingAQueryParameterWithNoExampleAddsIt covers the case URI text
// editing cannot handle at all: a parameter the description declared and gave no
// example for is not in the compiled URI, so there is nothing to replace.
func TestSubstitutingAQueryParameterWithNoExampleAddsIt(t *testing.T) {
	optional := refract.Member("limit", refract.New("string"))
	optional.Value.SetAttr(refract.SchemaAttribute, refract.String(`{"type":"integer"}`))

	compiled := readTransaction(t, "/things/{id}{?limit}",
		parameterMember("id", "7", `{"type":"integer"}`),
		optional)

	if compiled.Request.URI != "/things/7" {
		t.Fatalf("uri = %q, want the limit absent", compiled.Request.URI)
	}

	request, err := compiled.Request.SetParameter(findParameter(t, compiled.Request, "limit"), "5")
	if err != nil {
		t.Fatalf("substituting: %v", err)
	}
	if request.URI != "/things/7?limit=5" {
		t.Errorf("uri = %q, want the limit added", request.URI)
	}
}

// TestSubstitutingAHeaderLeavesTheCompiledRequestAlone pins that a probe's
// requests are independent of each other.
//
// A Request is passed by value and its header list is not, so replacing a header
// in place would leak into the transaction every later request is built from —
// and a run would end up testing the accumulation of everything it had generated
// rather than one value at a time.
func TestSubstitutingAHeaderLeavesTheCompiledRequestAlone(t *testing.T) {
	compiled := Request{
		Method:  "GET",
		URI:     "/things",
		Headers: []Header{{Name: "Accept", Value: "application/json"}, {Name: "X-Tenant", Value: "acme"}},
		Parameters: []Parameter{
			{In: "header", Name: "X-Tenant", Schema: `{"type":"string"}`, Value: "acme", HasValue: true},
		},
	}

	changed, err := compiled.SetParameter(compiled.Parameters[0], "other")
	if err != nil {
		t.Fatalf("substituting: %v", err)
	}

	if changed.Headers[1].Value != "other" {
		t.Errorf("header = %q, want the substituted value", changed.Headers[1].Value)
	}
	if compiled.Headers[1].Value != "acme" {
		t.Errorf("the compiled request's header became %q; substitution must not write through",
			compiled.Headers[1].Value)
	}
}

// TestSubstitutingAHeaderTheRequestDoesNotCarryAddsIt pins the case of a header
// parameter the description declared without an example: the compiled request
// carries it empty or not at all, and a generated value still has to arrive.
func TestSubstitutingAHeaderTheRequestDoesNotCarryAddsIt(t *testing.T) {
	compiled := Request{Method: "GET", URI: "/things"}

	changed, err := compiled.SetParameter(Parameter{In: "header", Name: "X-Tenant"}, "acme")
	if err != nil {
		t.Fatalf("substituting: %v", err)
	}
	if len(changed.Headers) != 1 || changed.Headers[0].Name != "X-Tenant" {
		t.Fatalf("headers = %+v, want the parameter added", changed.Headers)
	}
}

// TestSubstitutingWithoutATemplateIsAnError pins that a request compiled without
// a URI template — which is what a hand-built one in a test looks like — reports
// the problem rather than silently sending the unmodified URI, which would test
// the same request over and over and report it as sound.
func TestSubstitutingWithoutATemplateIsAnError(t *testing.T) {
	compiled := Request{Method: "GET", URI: "/things/7"}

	if _, err := compiled.SetParameter(Parameter{In: "path", Name: "id"}, "8"); err == nil {
		t.Error("substituting into a request with no template should report the problem")
	}
}

func findParameter(t *testing.T, request Request, name string) Parameter {
	t.Helper()
	for _, parameter := range request.Parameters {
		if parameter.Name == name {
			return parameter
		}
	}
	t.Fatalf("no parameter named %q in %+v", name, request.Parameters)
	return Parameter{}
}
