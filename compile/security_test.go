package compile_test

import (
	"reflect"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
)

func TestSecurityKeepsAlternativesRolesAndPublicOverrides(t *testing.T) {
	parsed, err := apidesc.Parse([]byte(`openapi: 3.1.0
info: {title: Security, version: '1'}
security: [{Bearer: [admin]}]
components:
  securitySchemes:
    Bearer: {type: http, scheme: bearer}
    Key: {type: apiKey, in: header, name: X-Key}
paths:
  /inherited:
    get:
      responses: {'200': {description: OK}}
  /public:
    get:
      security: []
      responses: {'200': {description: OK}}
  /optional:
    get:
      security: [{}, {Bearer: [reader]}]
      responses: {'200': {description: OK}}
  /alternatives:
    get:
      security:
        - Bearer: [reader, writer]
          Key: [operator]
        - Key: [auditor]
      responses: {'200': {description: OK}}
  /undefined:
    get:
      security: [{Missing: [], Bearer: [admin]}]
      responses: {'200': {description: OK}}
`), "security.yaml")
	if err != nil {
		t.Fatal(err)
	}
	compiled := compile.Compile(parsed.MediaType, parsed.Elements, "security.yaml")
	byPath := map[string]compile.Security{}
	for _, transaction := range compiled.Transactions {
		byPath[transaction.Request.URI] = transaction.Security
	}
	if len(byPath) != 5 {
		t.Fatalf("compiled %d operations, want 5", len(byPath))
	}
	inherited := byPath["/inherited"]
	if !inherited.Declared || inherited.Public || len(inherited.Requirements) != 1 ||
		!reflect.DeepEqual(inherited.Requirements[0][0].Roles, []string{"admin"}) {
		t.Fatalf("inherited security: %+v", inherited)
	}
	if public := byPath["/public"]; !public.Declared || !public.Public || len(public.Requirements) != 0 {
		t.Errorf("public override: %+v", public)
	}
	if optional := byPath["/optional"]; !optional.Public || len(optional.Requirements) != 1 {
		t.Errorf("optional security: %+v", optional)
	}
	alternatives := byPath["/alternatives"]
	if len(alternatives.Requirements) != 2 || len(alternatives.Requirements[0]) != 2 {
		t.Fatalf("AND/OR grouping lost: %+v", alternatives)
	}
	if !reflect.DeepEqual(alternatives.Requirements[0][0].Roles, []string{"reader", "writer"}) ||
		!reflect.DeepEqual(alternatives.Requirements[0][1].Roles, []string{"operator"}) ||
		!reflect.DeepEqual(alternatives.Requirements[1][0].Roles, []string{"auditor"}) {
		t.Errorf("roles lost: %+v", alternatives)
	}
	if len(alternatives.Schemes()) != 2 {
		t.Errorf("duplicate schemes: %+v", alternatives.Schemes())
	}
	if undefined := byPath["/undefined"]; !undefined.Declared || undefined.Public || len(undefined.Requirements) != 0 {
		t.Errorf("an undefined scheme weakened the requirement: %+v", undefined)
	}
}
