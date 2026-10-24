package openapi3

import (
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
