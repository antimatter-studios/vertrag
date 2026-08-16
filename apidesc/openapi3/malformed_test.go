package openapi3

import (
	"strings"
	"testing"
)

// TestMalformedDocumentsAreDiagnosedRatherThanSwallowed covers the axis every
// other test in this package leaves out: documents that are wrong.
//
// The corpus is made of valid descriptions, which says nothing about what
// happens to an invalid one — and the failure that matters here is not a crash
// but silence. A document vertrag cannot fully read, that produces no
// diagnostic, looks exactly like one it read perfectly.
func TestMalformedDocumentsAreDiagnosedRatherThanSwallowed(t *testing.T) {
	for _, test := range []struct {
		name     string
		document string
		// expect is a fragment the diagnostics must mention. Empty means the
		// document is legitimately quiet.
		expect string
	}{
		{
			// A typo in a pointer is a common thing to write and the worst
			// thing to swallow: the schema it should have supplied is simply
			// absent, so the body is validated against nothing and the run
			// passes while checking less than it appears to.
			name: "a reference to nothing",
			document: `
openapi: "3.0.0"
info: {title: T, version: "1.0"}
paths:
  /a:
    get:
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: {$ref: "#/components/schemas/Absent"}
`,
			expect: "resolves to nothing",
		},
		{
			name: "a reference inside components that goes nowhere",
			document: `
openapi: "3.0.0"
info: {title: T, version: "1.0"}
components:
  schemas:
    Present:
      type: object
      properties:
        child: {$ref: "#/components/schemas/Missing"}
paths:
  /a:
    get:
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: {$ref: "#/components/schemas/Present"}
`,
			expect: "resolves to nothing",
		},
		{
			// Legitimately quiet: every reference resolves.
			name: "references that all resolve",
			document: `
openapi: "3.0.0"
info: {title: T, version: "1.0"}
components:
  schemas:
    A: {type: string}
    B: {type: object, properties: {a: {$ref: "#/components/schemas/A"}}}
paths:
  /a:
    get:
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: {$ref: "#/components/schemas/B"}
`,
			expect: "",
		},
		{
			// A reference into another document. vertrag reads one file, so it
			// cannot follow this — and calling it dangling would be wrong.
			name: "a reference to another file is not called dangling",
			document: `
openapi: "3.0.0"
info: {title: T, version: "1.0"}
paths:
  /a:
    get:
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: {$ref: "shared.yml#/components/schemas/A"}
`,
			expect: "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := compileSource(t, test.document)

			var messages []string
			for _, annotation := range result.Annotations {
				messages = append(messages, annotation.Message)
			}
			joined := strings.Join(messages, "\n")

			if test.expect == "" {
				if len(messages) > 0 {
					t.Errorf("expected no diagnostics, got:\n%s", joined)
				}
				return
			}
			if !strings.Contains(joined, test.expect) {
				t.Errorf("diagnostics do not mention %q:\n%s", test.expect, joined)
			}
		})
	}
}

// TestABrokenDocumentNeverPanicsOrHangs is a breadth check over documents that
// are wrong in structural ways.
//
// None of these should produce a usable result; all of them should produce a
// result rather than a crash. A contract tester that panics on a malformed
// description is one nobody can use to find out that their description is
// malformed.
func TestABrokenDocumentNeverPanicsOrHangs(t *testing.T) {
	for _, document := range []string{
		``,
		`{{{ not yaml at all`,
		`openapi: "3.0.0"` + "\n" + `info: {title: T, version: "1.0"}` + "\n" + `paths: [a, b, c]`,
		`openapi: "3.0.0"` + "\n" + `info: {title: T, version: "1.0"}` + "\n" + `paths:` + "\n" + `  /a:` + "\n" + `    get:` + "\n" + `      responses: "not an object"`,
		`openapi: "3.0.0"` + "\n" + `info: {title: T, version: "1.0"}` + "\n" + `paths:` + "\n" + `  /a:` + "\n" + `    get: {}`,
		`openapi: "3.0.0"` + "\n" + `info: {title: T, version: "1.0"}` + "\n" + `components:` + "\n" + `  schemas:` + "\n" + `    A: {$ref: "#/components/schemas/B"}` + "\n" + `    B: {$ref: "#/components/schemas/A"}` + "\n" + `paths:` + "\n" + `  /a:` + "\n" + `    get:` + "\n" + `      responses:` + "\n" + `        "200":` + "\n" + `          description: OK` + "\n" + `          content:` + "\n" + `            application/json:` + "\n" + `              schema: {$ref: "#/components/schemas/A"}`,
	} {
		// Parse rather than compileSource: several of these fail to parse at
		// all, which is a legitimate outcome and not a reason to fail the test.
		elements, err := Parse([]byte(document))
		if err != nil {
			continue
		}
		if elements == nil {
			t.Errorf("parsed without error but returned nothing:\n%s", document)
		}
	}
}

// TestAnUnsendableHeaderValueIsTheDocumentsFault pins where the blame goes.
//
// A header value carrying a line break cannot be sent — net/http refuses it,
// and rightly, since splitting a value across lines is how a request is forged.
// Without this the refusal arrived from the transport as
// `net/http: invalid header field value`, which reads as the server or the
// network breaking, and sends the reader to look at a server that was never
// asked anything.
func TestAnUnsendableHeaderValueIsTheDocumentsFault(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: T, version: "1.0"}
paths:
  /h:
    get:
      parameters:
        - name: X-Forged
          in: header
          example: "value\nX-Injected: evil"
          schema: {type: string}
      responses:
        "200": {description: OK}
`)

	var said string
	for _, annotation := range result.Annotations {
		if strings.Contains(annotation.Message, "line break") {
			said = annotation.Message
			if annotation.Type != "error" {
				t.Errorf("reported as %q; a request that cannot be sent is not a warning", annotation.Type)
			}
		}
	}
	if said == "" {
		t.Fatalf("nothing said about a header value that cannot be sent: %v", result.Annotations)
	}
	if !strings.Contains(said, "X-Forged") {
		t.Errorf("the message does not name the parameter: %s", said)
	}
}

// TestNonASCIIHeaderValuesAreStillSent pins the other side of that rule.
//
// Bytes above ASCII are outside what the header grammar strictly permits and
// are sent by every client in practice. Refusing them would fail documents that
// work perfectly well.
func TestNonASCIIHeaderValuesAreStillSent(t *testing.T) {
	result := compileSource(t, `
openapi: "3.0.0"
info: {title: T, version: "1.0"}
paths:
  /h:
    get:
      parameters:
        - name: X-Unicode
          in: header
          example: "café-中文-🎉"
          schema: {type: string}
      responses:
        "200": {description: OK}
`)

	for _, annotation := range result.Annotations {
		t.Errorf("unexpected diagnostic for a non-ASCII header: %s", annotation.Message)
	}
	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1", len(result.Transactions))
	}

	var sent string
	for _, header := range result.Transactions[0].Request.Headers {
		if header.Name == "X-Unicode" {
			sent = header.Value
		}
	}
	if sent != "café-中文-🎉" {
		t.Errorf("header value = %q, want it carried through intact", sent)
	}
}
