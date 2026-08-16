package openapi3

import (
	"strings"
	"testing"
)

const linkedDocument = `
openapi: "3.0.0"
info: {title: Linked, version: "1.0.0"}
paths:
  /users:
    post:
      operationId: createUser
      responses:
        "201":
          description: Created
          content:
            application/json:
              schema: {type: object, properties: {id: {type: integer}}}
          links:
            ReadUser:
              operationId: readUser
              parameters:
                userId: "$response.body#/id"
            DeleteUser:
              operationId: deleteUser
              parameters:
                path.userId: "$response.body#/id"
  /users/{userId}:
    get:
      operationId: readUser
      parameters:
        - {name: userId, in: path, required: true, example: 1, schema: {type: integer}}
      responses:
        "200": {description: OK}
`

// TestLinksReachTheCompiledTransaction pins that a Response Object's links
// survive parse and compile.
//
// Dredd's parser marks `links` unsupported, so an API whose behaviour is
// "create, then read what you created" could only ever be tested one isolated
// request at a time.
func TestLinksReachTheCompiledTransaction(t *testing.T) {
	result := compileSource(t, linkedDocument)

	var created *struct {
		id    string
		links int
	}
	for _, transaction := range result.Transactions {
		if transaction.OperationID != "createUser" {
			continue
		}
		created = &struct {
			id    string
			links int
		}{transaction.OperationID, len(transaction.Links)}

		if len(transaction.Links) != 2 {
			t.Fatalf("got %d link(s), want 2", len(transaction.Links))
		}

		byName := map[string]string{}
		for _, link := range transaction.Links {
			byName[link.Name] = link.OperationID
			if len(link.Parameters) != 1 {
				t.Errorf("%s: got %d parameter(s), want 1", link.Name, len(link.Parameters))
			}
		}
		if byName["ReadUser"] != "readUser" {
			t.Errorf("ReadUser targets %q, want readUser", byName["ReadUser"])
		}
		if byName["DeleteUser"] != "deleteUser" {
			t.Errorf("DeleteUser targets %q, want deleteUser", byName["DeleteUser"])
		}

		// The qualified form names the location the value is for, and is kept
		// verbatim: one operation can have a query and a path parameter of the
		// same name, and the bare form cannot say which is meant.
		for _, link := range transaction.Links {
			if link.Name != "DeleteUser" {
				continue
			}
			if _, qualified := link.Parameters["path.userId"]; !qualified {
				t.Errorf("qualified key lost: %v", link.Parameters)
			}
		}
	}

	if created == nil {
		t.Fatal("no transaction carried operationId createUser")
	}
}

// TestLinksAreNoLongerReportedUnsupported pins the warning's removal. Telling a
// reader that `links` does nothing, while acting on it, is the mistake the
// support guard exists to catch.
func TestLinksAreNoLongerReportedUnsupported(t *testing.T) {
	for _, annotation := range compileSource(t, linkedDocument).Annotations {
		if strings.Contains(annotation.Message, "links") {
			t.Errorf("unexpected annotation about links: %s", annotation.Message)
		}
	}
}
