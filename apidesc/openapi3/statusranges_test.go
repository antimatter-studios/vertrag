package openapi3

import (
	"strings"
	"testing"
)

const rangeDocument = `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /things:
    get:
      summary: S
      responses:
        "2XX":
          description: Any success
          content:
            application/json: {schema: {type: object}}
`

// TestAStatusRangeIsExpectedAsARange pins the whole point: a `2XX` response
// used to compile to an expectation of exactly 200, which is a promise the
// document never made.
func TestAStatusRangeIsExpectedAsARange(t *testing.T) {
	result := compileSource(t, rangeDocument)

	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1", len(result.Transactions))
	}
	if got := result.Transactions[0].Response.Status; got != "2XX" {
		t.Errorf("expected status = %q, want %q", got, "2XX")
	}
}

// TestAStatusRangeNamesItsTransactionByTheRange pins the name, which is what
// hooks and `--only` address and therefore may not drift.
func TestAStatusRangeNamesItsTransactionByTheRange(t *testing.T) {
	result := compileSource(t, rangeDocument)

	if want := "P > /things > S > 2XX > application/json"; result.Transactions[0].Name != want {
		t.Errorf("name = %q, want %q", result.Transactions[0].Name, want)
	}
}

func TestAStatusRangeIsNoLongerReportedAsUnsupported(t *testing.T) {
	result := compileSource(t, rangeDocument)

	for _, annotation := range result.Annotations {
		if strings.Contains(annotation.Message, "status code ranges are unsupported") {
			t.Errorf("ranges are acted on now, so warning about them is false: %q", annotation.Message)
		}
	}
}

// TestAnExactStatusAndARangeEachDescribeTheirOwnExchange pins the precedence
// decision. Both keys describe a response, so both become transactions; what
// the specific one wins is the judging of a body, which its own transaction
// does with its own schema.
func TestAnExactStatusAndARangeEachDescribeTheirOwnExchange(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /things:
    get:
      summary: S
      responses:
        "200": {description: Exact}
        "2XX": {description: Any success}
`)

	var statuses []string
	for _, transaction := range result.Transactions {
		statuses = append(statuses, transaction.Response.Status)
	}
	if len(statuses) != 2 || statuses[0] != "200" || statuses[1] != "2XX" {
		t.Errorf("expected statuses = %v, want [200 2XX]", statuses)
	}
}

// TestADefaultResponseStillExpectsTwoHundred pins what was NOT changed.
// `default` is "every code not described here", which is not a band, and it
// keeps the treatment it has always had.
func TestADefaultResponseStillExpectsTwoHundred(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /things:
    get:
      summary: S
      responses:
        default: {description: Whatever happens}
`)

	if got := result.Transactions[0].Response.Status; got != "200" {
		t.Errorf("expected status = %q, want %q", got, "200")
	}
}

// TestAMalformedStatusRangeIsReported keeps the standing rule that nothing is
// silently ignored: `22X` is not a range the specification defines, and it
// used to be accepted as one.
func TestAMalformedStatusRangeIsReported(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: P, version: "1.0.0"}
paths:
  /things:
    get:
      summary: S
      responses:
        "22X": {description: Not a range}
`)

	for _, annotation := range result.Annotations {
		if strings.Contains(annotation.Message, "22X") {
			return
		}
	}
	t.Errorf("no annotation named the malformed key; annotations = %v", result.Annotations)
}
