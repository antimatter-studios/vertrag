package openapi3

import (
	"reflect"
	"strings"
	"testing"
)

// namedParameters shows a found and a missing thing by parameter alone, and a
// header that names another tenant: the 404 and the 403 are named after the
// parameter examples that provoke them (#85).
const namedParameters = `
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
        - name: X-Tenant
          in: header
          schema: {type: string}
          examples:
            elsewhere: {value: other-tenant}
      responses:
        "200":
          description: OK
        "403":
          description: Another tenant's
          content:
            application/json:
              examples:
                elsewhere: {value: {error: forbidden}}
        "404":
          description: No such thing
          content:
            application/json:
              examples:
                missing: {value: {error: not_found}}
`

// Each parameter example name a response is named after is a request of its
// own, sent with that name's values and paired with the response of its name;
// a name no response carries pairs with the unnamed ones. Only a value whose
// name a response carries is marked as the exchange's own, which a link leaves
// alone; an optional parameter whose examples are other names' is left out.
func TestNamedParameterExamplesArePaired(t *testing.T) {
	result := compileSource(t, namedParameters)
	type sent struct {
		uri, status, tenant string
		named               bool
	}
	var got []sent
	for _, tx := range result.Transactions {
		s := sent{uri: tx.Request.URI, status: tx.Response.Status}
		for _, p := range tx.Request.Parameters {
			if p.Name == "id" {
				s.named = p.Named
			}
			if p.Name == "X-Tenant" {
				s.tenant, _ = p.Value.(string)
			}
		}
		got = append(got, s)
	}
	want := []sent{
		{uri: "/things/1", status: "200", tenant: "", named: false},
		{uri: "/things/2147483647", status: "404", tenant: "", named: true},
		{uri: "/things/1", status: "403", tenant: "other-tenant", named: false},
	}
	if len(got) != len(want) {
		t.Fatalf("transactions %+v, want %+v (annotations %v)", got, want, result.Annotations)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("transaction %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Two bodyless responses told apart only by their requests: each says its name
// with x-example, so each request is sent with its own status and no other.
func TestABodylessResponseIsNamedByXExample(t *testing.T) {
	result := compileSource(t, `
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
            not_modified: {value: 1}
            precondition_failed: {value: 1}
        - name: If-None-Match
          in: header
          schema: {type: string}
          examples:
            not_modified: {value: "*"}
        - name: If-Match
          in: header
          schema: {type: string}
          examples:
            precondition_failed: {value: '"not-this-one"'}
      responses:
        "200":
          description: OK
          content:
            application/json:
              examples:
                found: {value: {id: 1}}
        "304":
          description: Not modified
          x-example: not_modified
        "412":
          description: Precondition failed
          x-example: precondition_failed
`)
	got := map[string]string{}
	for _, tx := range result.Transactions {
		var conditional []string
		for _, h := range tx.Request.Headers {
			if h.Name == "If-None-Match" || h.Name == "If-Match" {
				conditional = append(conditional, h.Name)
			}
		}
		if _, repeated := got[tx.Response.Status]; repeated {
			t.Fatalf("status %s sent twice: %+v", tx.Response.Status, result.Transactions)
		}
		got[tx.Response.Status] = strings.Join(conditional, ",")
	}
	want := map[string]string{"200": "", "304": "If-None-Match", "412": "If-Match"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
