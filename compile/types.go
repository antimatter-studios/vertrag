// Package compile turns an API Elements document into the HTTP transactions
// Dredd executes.
//
// This stage is deliberately format-agnostic: API Blueprint, OpenAPI 2 and
// OpenAPI 3 all reach it as API Elements, so the rules for naming a
// transaction, expanding a URI and reporting a parser annotation are written
// once. Only the parse stage in front of it is format-specific.
//
// The JSON shape produced here is the oracle's comparison surface against the
// reference implementation, so which fields are present and which are omitted
// is part of the contract, not a formatting preference.
package compile

import "encoding/json"

// Header is one HTTP header. API Elements permits repeats, so this is a list
// rather than a map.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Request is the HTTP request Dredd will send.
//
// Body is always present, empty string included: the reference emits it
// unconditionally, and the oracle compares presence.
type Request struct {
	Method  string   `json:"method"`
	URI     string   `json:"uri"`
	Headers []Header `json:"headers"`
	Body    string   `json:"body"`

	// Schema is the JSON Schema the request body was built from, when the
	// description carried one.
	//
	// It is deliberately absent from the JSON. Dredd has no equivalent — it
	// sends the example and never needs to know what else would have been
	// valid — so emitting it would make every compiled transaction differ from
	// the reference for no benefit to the comparison. It exists for generation,
	// which needs the shape of a valid body rather than one instance of it.
	Schema string `json:"-"`

	// Template is the URI template URI was expanded from, and Parameters are
	// the values it was expanded with, plus the parameters that travel as
	// headers.
	//
	// They are kept for the same reason Schema is, and are equally absent from
	// the JSON. Together they are what lets a request be rebuilt with one
	// parameter's value replaced: a URI cannot be edited as text, because the
	// substitute has to be encoded for the position it sits in and a query
	// parameter the description gave no example for is not in the URI at all.
	Template   string      `json:"-"`
	Parameters []Parameter `json:"-"`

	// GraphQLArguments are the arguments this request's query passes, each with
	// the JSON Schema its value must satisfy and the variable it travels under.
	//
	// They sit on the request rather than beside the GraphQL expectation
	// because they are the analogue of Parameters: the parts of the request a
	// generated value can occupy, and what SetGraphQLArgument puts one back
	// into. Absent from the JSON for the same reason Schema is — Dredd has no
	// equivalent, because Dredd cannot test GraphQL at all.
	GraphQLArguments []GraphQLArgument `json:"-"`
}

// Where a parameter travels. They are the OpenAPI names, because that is what
// every description this compiles from calls them and what a report naming one
// should say.
const (
	InPath   = "path"
	InQuery  = "query"
	InHeader = "header"

	// InCookie travels in the Cookie request header, one `name=value` pair
	// among however many others that header carries. It is the only location
	// whose parameters SHARE a header, which is why setting one has to rebuild
	// the line rather than overwrite it — see SetParameter.
	InCookie = "cookie"
)

// Parameter is one path, query, header or cookie parameter of a request, with
// the schema the description said its value must satisfy.
//
// A body carries its constraints in one schema; a parameter carries its own,
// and they are the ones most often left unchecked — a path parameter typed as
// an integer, handed a word, is the classic way to get a 500 out of an
// otherwise careful handler.
type Parameter struct {
	// In is where the value travels: "path", "query", "header" or "cookie".
	// Path and query are read from the URI template rather than from the
	// description, so a query parameter a format parser folded into the href is
	// still recognised as one.
	In string

	// Name is the parameter's name as the URI template, the header list or the
	// Cookie header spells it.
	Name string

	// Schema is the JSON Schema its value must satisfy, empty when the
	// description gave none.
	Schema string

	// Style is how a list or object value is laid out on the wire, when the
	// description chose something RFC 6570 cannot say: "spaceDelimited",
	// "pipeDelimited" or "deepObject". Empty means the template's own rule
	// (form/simple, exploded or not) already applies.
	Style string

	// Value is what the compiled request carries for it. HasValue separates a
	// parameter with no value from one whose value is empty — the first is
	// missing from the URI entirely, the second is present and blank.
	Value    any
	HasValue bool
}

// Response is the response the API description promises.
//
// Body and Schema are omitted when empty, matching the reference, which only
// assigns them when truthy. The distinction is meaningful downstream: an absent
// schema means "do not validate the body against a schema", which is not the
// same as validating against an empty one.
type Response struct {
	Status  string   `json:"status"`
	Headers []Header `json:"headers"`
	Body    string   `json:"body,omitempty"`
	Schema  string   `json:"schema,omitempty"`

	// HeaderSchemas are the JSON Schemas the description gave individual
	// response headers, keyed by header name.
	//
	// Absent from the JSON for the same reason as Request.Schema: Dredd has no
	// equivalent — it checks that a declared header is present and never looks
	// at its value — so emitting it would make every compiled transaction
	// differ from the reference for nothing. The header-schema check reads it.
	HeaderSchemas map[string]json.RawMessage `json:"-"`
}

// Origin records where in the API description a transaction came from. Its
// fields, joined, form the transaction name that hooks address transactions by.
type Origin struct {
	Filename          string `json:"filename"`
	APIName           string `json:"apiName"`
	ResourceGroupName string `json:"resourceGroupName"`
	ResourceName      string `json:"resourceName"`
	ActionName        string `json:"actionName"`
	ExampleName       string `json:"exampleName"`
}

// Transaction is a single request/response pair to be tested.
type Transaction struct {
	Request  Request  `json:"request"`
	Response Response `json:"response"`
	Name     string   `json:"name"`
	Origin   Origin   `json:"origin"`

	// Links are what the response this transaction expects declares it leads
	// to, and are what `--sequence` orders a run by. Absent from the JSON for
	// the same reason OperationID is.
	Links []Link `json:"-"`

	// Security is what the operation requires a caller to prove. Absent from
	// the JSON for the same reason OperationID is.
	Security []Security `json:"-"`

	// Tags are the operation's grouping labels, which `--tag` narrows a run
	// by. Absent from the JSON for the same reason OperationID is.
	Tags []string `json:"-"`

	// GraphQL is what the response must satisfy when this transaction came
	// from a GraphQL schema, and nil for every other format.
	//
	// Absent from the JSON for the same reason OperationID is, and it carries
	// the whole of what makes a GraphQL exchange judgeable: the wire form is a
	// POST of an opaque string to one path, so a runner that had only the
	// request could not tell a GraphQL transaction from any other, let alone
	// what it asked for.
	GraphQL *GraphQL `json:"-"`

	// OperationID is how the description names this transaction's operation,
	// and is how an OpenAPI Link Object refers to the operation it leads to.
	//
	// Absent from the JSON for the same reason Request.Schema is: Dredd has
	// nothing that refers to an operation by name, so it discards the key
	// entirely, and emitting it would make every compiled transaction differ
	// from the reference for no benefit to the comparison.
	OperationID string `json:"-"`
}

// Annotation is a diagnostic about the API description itself — a parser error,
// an unusable URI template, a contradictory parameter.
//
// Location is a pair of [line, column] pairs, or null. It is an explicit
// pointer so that "no location" serialises as null rather than being dropped:
// the reference always emits the key.
type Annotation struct {
	Type      string  `json:"type"`
	Component string  `json:"component"`
	Message   string  `json:"message"`
	Location  [][]int `json:"location"`

	// Name and Origin are attached only to annotations raised while compiling a
	// specific transaction, so that a diagnostic can be traced back to the
	// operation that provoked it.
	Name   string  `json:"name,omitempty"`
	Origin *Origin `json:"origin,omitempty"`
}

// Result is the compiler's output for one API description document.
type Result struct {
	MediaType    string        `json:"mediaType"`
	Transactions []Transaction `json:"transactions"`
	Annotations  []Annotation  `json:"annotations"`
}
