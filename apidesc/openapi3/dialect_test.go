package openapi3

import (
	"fmt"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/validate"
)

// `jsonSchemaDialect` was reported as an invalid key — "this is not OpenAPI at
// all" — for a field 3.1 defines, and the dialect a document declared decided
// nothing. That is not a cosmetic complaint: the `$schema` vertrag stamps on
// the schemas it emits is the only thing the validator reads to choose its
// rules, so a document written in one dialect and validated under another
// under-enforces its own constraints and passes.

// dialectOf compiles a document declaring a dialect and returns the `$schema`
// the emitted response schema carries, which is the only place the choice
// becomes visible.
func dialectOf(t *testing.T, version, declared string) string {
	t.Helper()

	line := ""
	if declared != "" {
		line = "jsonSchemaDialect: " + declared + "\n"
	}
	result := compileSource(t, fmt.Sprintf(`
openapi: %q
%sinfo: {title: T, version: "1.0.0"}
paths:
  /a:
    get:
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema: {type: object, properties: {a: {type: string}}}
`, version, line))

	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d: %v", len(result.Transactions), result.Annotations)
	}
	schema := result.Transactions[0].Response.Schema

	const key = `"$schema":"`
	start := strings.Index(schema, key)
	if start < 0 {
		t.Fatalf("no $schema in %s", schema)
	}
	rest := schema[start+len(key):]
	return rest[:strings.Index(rest, `"`)]
}

func TestADeclaredDialectIsNotAnInvalidKey(t *testing.T) {
	assertNoDiagnosticMentions(t, diagnosticsOf(t, `
openapi: "3.1.0"
jsonSchemaDialect: https://json-schema.org/draft/2020-12/schema
info: {title: T, version: "1.0.0"}
paths:
  /a:
    get:
      responses:
        "200": {description: OK}
`), "invalid key 'jsonSchemaDialect'")
}

func TestTheDeclaredDialectDecidesWhatTheSchemasAreReadAs(t *testing.T) {
	// The point of acting on the field rather than merely accepting it. A 3.1
	// document written in draft-07 says so, and reading it as the 2020-12 it
	// never claimed is how a sibling of `$ref` — ignored in draft-07, honoured
	// in 2020-12 — comes to be enforced against a body that was never meant to
	// satisfy it.
	if got := dialectOf(t, "3.1.0", "http://json-schema.org/draft-07/schema#"); !strings.Contains(got, "draft-07") {
		t.Errorf("$schema = %q, want the declared draft-07", got)
	}
}

func TestTheDefaultDialectIsUnchangedByDeclaringIt(t *testing.T) {
	// 3.1's own default is the OAS base dialect: 2020-12 with OpenAPI's
	// annotation vocabulary layered on, which asserts nothing a body can
	// violate. It is what vertrag already did, so a document spelling it out
	// must get the same answer as one that says nothing — and must not be told
	// its dialect went unhonoured.
	for _, declared := range []string{"", "https://spec.openapis.org/oas/3.1/dialect/base"} {
		if got := dialectOf(t, "3.1.0", declared); got != dialect2020 {
			t.Errorf("jsonSchemaDialect %q gave $schema %q, want %q", declared, got, dialect2020)
		}
	}

	assertNoDiagnosticMentions(t, diagnosticsOf(t, `
openapi: "3.1.0"
jsonSchemaDialect: https://spec.openapis.org/oas/3.1/dialect/base
info: {title: T, version: "1.0.0"}
paths:
  /a:
    get:
      responses:
        "200": {description: OK}
`), "jsonSchemaDialect")
}

func TestADialectVertragCannotHonourIsSaidRatherThanSilenced(t *testing.T) {
	// The standing rule: act on a construct or say so. Passing an unrecognised
	// URI through would be the worst of the three answers — the validator reads
	// a `$schema` it does not know as draft-04, so a document with a bespoke
	// dialect line would have every 2020-12 constraint in it read under the
	// oldest rules and quietly under-enforced.
	assertSomeDiagnosticMentions(t, diagnosticsOf(t, `
openapi: "3.1.0"
jsonSchemaDialect: https://example.com/schema/dialect/house-style
info: {title: T, version: "1.0.0"}
paths:
  /a:
    get:
      responses:
        "200": {description: OK}
`), "'jsonSchemaDialect' names \"https://example.com/schema/dialect/house-style\", which vertrag does not implement")

	if got := dialectOf(t, "3.1.0", "https://example.com/schema/dialect/house-style"); got != dialect2020 {
		t.Errorf("$schema = %q, want the 3.1 default %q", got, dialect2020)
	}
}

func TestADialectDeclaredBefore31IsSaidRatherThanActedOn(t *testing.T) {
	// The field is 3.1's, and 3.0's Schema Object is not JSON Schema but a
	// subset with its own spellings — `nullable`, `exclusiveMinimum` as a flag
	// beside `minimum`. Stamping 2020-12 on the conversion of one produces a
	// schema the validator cannot compile, which means the body it described
	// goes unchecked: acting on the field here would be worse than refusing to.
	assertSomeDiagnosticMentions(t, diagnosticsOf(t, `
openapi: "3.0.3"
jsonSchemaDialect: https://json-schema.org/draft/2020-12/schema
info: {title: T, version: "1.0.0"}
paths:
  /a:
    get:
      responses:
        "200": {description: OK}
`), "'jsonSchemaDialect' is OpenAPI 3.1's")

	if got := dialectOf(t, "3.0.3", "https://json-schema.org/draft/2020-12/schema"); got != dialectDraft4 {
		t.Errorf("$schema = %q, want 3.0's %q", got, dialectDraft4)
	}
}

// TestEveryStampedDialectIsOneTheValidatorImplements is the guard that keeps
// the two ends in step.
//
// The parser chooses a `$schema` and the validator chooses its rules from that
// string alone, and it cannot refuse one: an unfamiliar URI is read as draft-04
// and the run carries on. So a dialect this parser is willing to stamp but the
// validator does not implement would silently downgrade every schema in the
// document — which is precisely the failure `jsonSchemaDialect` support was
// added to avoid.
func TestEveryStampedDialectIsOneTheValidatorImplements(t *testing.T) {
	declarations := []string{
		"",
		"https://json-schema.org/draft/2020-12/schema",
		"https://json-schema.org/draft/2019-09/schema",
		"http://json-schema.org/draft-07/schema#",
		"http://json-schema.org/draft-06/schema#",
		"http://json-schema.org/draft-04/schema#",
		"https://spec.openapis.org/oas/3.1/dialect/base",
		"https://spec.openapis.org/oas/3.2/dialect/base",
		"https://example.com/schema/dialect/house-style",
		"nonsense",
	}

	for _, version := range []string{"3.0.3", "3.1.0", "3.2.0"} {
		for _, declared := range declarations {
			stamped := dialectOf(t, version, declared)
			if !validate.KnownDialect(stamped) {
				t.Errorf("openapi %s declaring %q stamped %q, which the validator reads as draft-04",
					version, declared, stamped)
			}
		}
	}
}
