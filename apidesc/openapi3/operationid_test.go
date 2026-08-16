package openapi3

import "testing"

// TestOperationIDIsCarried pins that an operation's identifier survives into
// the compiled transaction.
//
// Dredd discards it, having nothing that refers to an operation by name. An
// OpenAPI Link Object refers to its target that way and can do nothing without
// it, so it is carried — off the JSON, so the comparison against Dredd is
// unaffected.
func TestOperationIDIsCarried(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: T, version: "1.0.0"}
paths:
  /things:
    get:
      operationId: listThings
      responses:
        "200": {description: OK}
    post:
      responses:
        "201": {description: Created}
`)

	byMethod := map[string]string{}
	for _, transaction := range result.Transactions {
		byMethod[transaction.Request.Method] = transaction.OperationID
	}

	if got := byMethod["GET"]; got != "listThings" {
		t.Errorf("GET operationId = %q, want listThings", got)
	}
	// An operation without one is not an error; it simply cannot be a link
	// target.
	if got := byMethod["POST"]; got != "" {
		t.Errorf("POST operationId = %q, want empty", got)
	}
}
