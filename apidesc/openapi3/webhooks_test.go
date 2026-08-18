package openapi3

import (
	"strings"
	"testing"
)

// A description whose whole API surface is webhooks was the worst outcome this
// parser could produce: `webhooks` was called an invalid key, `paths` was
// demanded of a document that 3.1 does not ask it of, and the result was an
// error — no transactions, no diagnostics about anything in the file, and a run
// that had checked nothing at all.
//
// So these tests are about the two halves of the answer: the Path Items under
// `webhooks` are read and checked like anyone else's, and the operations they
// describe are named and counted as not sent. See webhooks.go for why they are
// not sent.

// webhooksOnlyDocument is the shape that motivated this: an API whose entire
// published surface is the requests it makes to its subscribers.
const webhooksOnlyDocument = `
openapi: "3.1.0"
info: {title: Events, version: "1.0.0"}
webhooks:
  newPet:
    post:
      operationId: newPet
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties: {name: {type: string}}
      responses:
        "200": {description: Acknowledged}
  petStatus:
    put:
      operationId: petStatus
      responses:
        "200": {description: Acknowledged}
`

func TestAWebhookIsNotSilentlyDropped(t *testing.T) {
	diagnostics := diagnosticsOf(t, webhooksOnlyDocument)

	// An invalid key means "this is not OpenAPI at all", which `webhooks` has
	// not been since 3.1.
	assertNoDiagnosticMentions(t, diagnostics, "invalid key 'webhooks'")

	// The operations have to be named, because a count on its own leaves the
	// reader to work out which half of their API went untested.
	assertSomeDiagnosticMentions(t, diagnostics, "not sent")
	assertSomeDiagnosticMentions(t, diagnostics, "newPet > POST")
	assertSomeDiagnosticMentions(t, diagnostics, "petStatus > PUT")
	assertSomeDiagnosticMentions(t, diagnostics,
		"2 of the description's webhook operations were")
}

func TestAWebhookIsNotSent(t *testing.T) {
	// The other half of the claim above. A webhook is a request the API sends
	// out, so compiling one into a transaction would point it at the endpoint
	// under test — an API being sent a request it never said it would answer.
	if transactions := transactionsOf(t, webhooksOnlyDocument); len(transactions) != 0 {
		t.Errorf("a webhook was compiled into a transaction: %v", transactions)
	}
}

func TestADescriptionOfNothingButWebhooksIsValid(t *testing.T) {
	// 3.1 stopped requiring `paths`, and a missing required property is an
	// error that stops the whole parse: this document was told to add a field
	// the specification does not ask it for, and nothing else about it was ever
	// looked at.
	assertNoDiagnosticMentions(t, diagnosticsOf(t, webhooksOnlyDocument), "error:")
}

func TestAWebhooksOwnMistakesAreReported(t *testing.T) {
	// Reporting that webhooks are not sent would be worth little if the Path
	// Items under them were still unread: the schemas, parameters and responses
	// of a webhooks-only description are the whole of that description, and
	// leaving them unchecked is the same silence in a new place.
	diagnostics := diagnosticsOf(t, `
openapi: "3.1.0"
info: {title: Events, version: "1.0.0"}
webhooks:
  newPet:
    postt:
      responses:
        "200": {description: OK}
    post:
      parameters:
        - name: signature
          in: body
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: {$ref: "#/components/schemas/Absent"}
`)

	assertSomeDiagnosticMentions(t, diagnostics, "'Path Item Object' contains invalid key 'postt'")
	// `in: body` is Swagger 2's spelling, which an OpenAPI 3 document has no
	// place for. It stands in for the parameter-level mistake this test is
	// really about; it was written as `in: cookie`, which vertrag now sends
	// rather than warns about.
	assertSomeDiagnosticMentions(t, diagnostics, "'Parameter Object' 'in' 'body' is unsupported")
	assertSomeDiagnosticMentions(t, diagnostics, "resolves to nothing")
}

func TestAWebhookCanStopTheParseTheWayAPathCan(t *testing.T) {
	// An error anywhere means the document could not be fully read, and the
	// parse stops on one wherever it came from. A webhook is no different: a
	// description that is only webhooks is a description whose every mistake is
	// in one.
	assertSomeDiagnosticMentions(t, diagnosticsOf(t, `
openapi: "3.1.0"
info: {title: Events, version: "1.0.0"}
webhooks:
  newPet:
    post:
      responses:
        "200": {}
`), "'Response Object' is missing required property 'description'")
}

func TestAWebhookWrittenAsAReferenceIsStillNamed(t *testing.T) {
	// The idiomatic spelling: the Path Item lives in `components.pathItems` and
	// the webhook points at it. Left unresolved it looks like a webhook with no
	// operations under it, and a count of nothing withheld is exactly the
	// reassuring silence this feature exists to remove.
	assertSomeDiagnosticMentions(t, diagnosticsOf(t, `
openapi: "3.1.0"
info: {title: Events, version: "1.0.0"}
webhooks:
  newPet:
    $ref: "#/components/pathItems/PetEvent"
components:
  pathItems:
    PetEvent:
      post:
        responses:
          "200": {description: OK}
`), "newPet > POST")
}

func TestASharedPathItemIsChecked(t *testing.T) {
	// `components.pathItems` is 3.1's and was accepted without ever being
	// walked, so a mistake in a shared Path Item was reported only if some
	// other corner of the document happened to need it — and never at all in
	// the corner it is most likely to sit in, one nothing references yet.
	assertSomeDiagnosticMentions(t, diagnosticsOf(t, `
openapi: "3.1.0"
info: {title: Events, version: "1.0.0"}
paths: {}
components:
  pathItems:
    PetEvent:
      poost:
        responses:
          "200": {description: OK}
`), "'Path Item Object' contains invalid key 'poost'")
}

func TestADocumentDescribingNoAPIAtAllIsAnError(t *testing.T) {
	// The requirement `paths` used to carry, in the form 3.1 restated it. There
	// is nothing to run and nothing to check in such a document, so saying why
	// is the only useful thing to say about it.
	assertSomeDiagnosticMentions(t, diagnosticsOf(t, `
openapi: "3.1.0"
info: {title: Empty, version: "1.0.0"}
`), "carries none of 'paths', 'webhooks' or 'components'")
}

func TestAPathIsStillRequiredBefore31(t *testing.T) {
	// The requirement was real until 3.1 restated it, and dropping the check
	// for every revision would stop reporting a 3.0 document that is genuinely
	// incomplete.
	assertSomeDiagnosticMentions(t, diagnosticsOf(t, `
openapi: "3.0.3"
info: {title: Empty, version: "1.0.0"}
`), "'OpenAPI Object' is missing required property 'paths'")
}

func TestAWebhookDeclaringNoOperationWithholdsNothing(t *testing.T) {
	// A `webhooks` map with nothing under it withholds nothing, and announcing
	// that nothing was withheld is noise — the reason the unsupported-key
	// warnings are collapsed in the first place.
	for _, diagnostic := range diagnosticsOf(t, `
openapi: "3.1.0"
info: {title: Events, version: "1.0.0"}
paths: {}
webhooks:
  newPet: {}
`) {
		if strings.Contains(diagnostic, "not sent") {
			t.Errorf("a webhook with no operations was announced as withheld: %s", diagnostic)
		}
	}
}
