package link_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/link"
	"github.com/antimatter-studios/vertrag/runner"
)

// linkedNamed reads a thing created first, by a link, and shows a missing one
// by its named path example (#85).
const linkedNamed = `
openapi: "3.1.0"
info: {title: T, version: "1"}
paths:
  /things/{id}:
    get:
      operationId: getThing
      parameters:
        - name: id
          in: path
          required: true
          schema: {type: integer}
          examples:
            found: {value: 1}
            missing: {value: 2147483647}
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: {type: object, properties: {id: {type: integer}}}
        "404":
          description: No such thing
          content:
            application/json:
              schema: {type: object}
              examples:
                missing: {value: {error: not_found}}
  /things:
    post:
      operationId: createThing
      responses:
        "201":
          description: Created
          content:
            application/json:
              schema: {type: object, properties: {id: {type: integer}}}
          links:
            Read:
              operationId: getThing
              parameters: {id: "$response.body#/id"}
`

// A link fills the id of the read it leads to, but not the id a request names
// as its own example: the missing thing is still asked for, does not wait for
// the create, and is answered 404, while the found one reads what the create
// made.
func TestALinkLeavesANamedExampleAlone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/things":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":7}`))
		case r.Method == http.MethodGet && r.URL.Path == "/things/7":
			_, _ = w.Write([]byte(`{"id":7}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
		}
	}))
	defer server.Close()

	parsed, err := apidesc.Parse([]byte(linkedNamed), "linked")
	if err != nil {
		t.Fatal(err)
	}
	compiled := compile.Compile(parsed.MediaType, parsed.Elements, "linked")
	sequencer := link.NewSequencer(compiled.Transactions)
	engine := runner.New(server.URL)
	engine.Plan = sequencer
	results, err := engine.Run(context.Background(), compiled.Transactions)
	if err != nil {
		t.Fatal(err)
	}

	sent := map[string]string{}
	for _, result := range results {
		if result.Status != runner.StatusPass {
			t.Errorf("%s: %s %v", result.Name, result.Status, result.Errors)
		}
		sent[result.Request.Method+" "+result.Request.URI] = string(result.Status)
	}
	for _, want := range []string{"POST /things", "GET /things/7", "GET /things/2147483647"} {
		if _, ok := sent[want]; !ok {
			t.Errorf("%s was not sent: %v", want, sent)
		}
	}

	plan := link.Build(compiled.Transactions)
	for _, step := range plan.Steps {
		tx := compiled.Transactions[step.Index]
		if tx.Request.URI == "/things/2147483647" && step.After != -1 {
			t.Errorf("the missing read waits for %s", compiled.Transactions[step.After].Name)
		}
		if tx.Request.URI == "/things/1" && step.After == -1 {
			t.Errorf("the found read does not follow the create")
		}
	}
}
