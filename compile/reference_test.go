package compile_test

import (
	"reflect"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
)

// TestABodyPropertyCanReferToAnotherOperationsResource pins how
// `x-vertrag-reference` reaches a transaction (#107): a top-level request body
// property marked with an operationId, and whether its ids are numbers.
func TestABodyPropertyCanReferToAnotherOperationsResource(t *testing.T) {
	parsed, err := apidesc.Parse([]byte(`openapi: 3.1.0
info: {title: References, version: '1'}
paths:
  /moves:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                folder_id: {type: integer, x-vertrag-reference: getFolder}
                label_id: {type: string, x-vertrag-reference: getLabel}
                note: {type: string}
            example: {folder_id: 7, label_id: "l1", note: hi}
      responses: {'200': {description: OK}}
`), "references.yaml")
	if err != nil {
		t.Fatal(err)
	}
	result := compile.Compile(parsed.MediaType, parsed.Elements, "references.yaml")
	if len(result.Transactions) != 1 {
		t.Fatalf("%d transactions", len(result.Transactions))
	}
	want := map[string]compile.Reference{
		"folder_id": {OperationID: "getFolder", Numeric: true},
		"label_id":  {OperationID: "getLabel"},
	}
	if got := result.Transactions[0].References; !reflect.DeepEqual(got, want) {
		t.Errorf("references = %+v, want %+v", got, want)
	}
}
