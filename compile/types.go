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
