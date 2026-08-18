package apidesc

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
)

func TestDetect(t *testing.T) {
	for _, test := range []struct {
		name       string
		source     string
		mediaType  string
		recognised bool
	}{
		{"unquoted OpenAPI 3", "openapi: 3.0.0\n", MediaTypeOpenAPI3, true},
		{"double-quoted OpenAPI 3", `openapi: "3.0.0"`, MediaTypeOpenAPI3, true},
		{"single-quoted OpenAPI 3", `openapi: '3.1.0'`, MediaTypeOpenAPI3, true},
		{"JSON OpenAPI 3", `{"openapi": "3.0.2"}`, MediaTypeOpenAPI3, true},
		{"Swagger 2", `swagger: "2.0"`, MediaTypeOpenAPI2, true},
		{"JSON Swagger 2", `{"swagger": "2.0"}`, MediaTypeOpenAPI2, true},
		// Unrecognised is unrecognised. It used to be reported as API Blueprint,
		// because that is what the reference assumes an unlabelled document is —
		// but vertrag does not support Blueprint, so the guess bought nothing
		// and labelled every unreadable file, of any kind, as a format it was
		// about to refuse.
		{"an unrecognised document is not guessed at", "# My API\n", MediaTypeUnknown, false},
		// A document that really is Blueprint is recognised, so it can be told
		// why it cannot be read rather than given the generic answer.
		{"API Blueprint is recognised and unsupported", "FORMAT: 1A\n\n# My API\n", MediaTypeAPIBlueprint, true},

		// A two-part version IS matched, though the specification requires all
		// three. This once deferred to the reference, which rejects it — but
		// the consequence was telling the author of an OpenAPI 3 file that API
		// Blueprint is unsupported, over a missing `.0`. The document is read
		// and the version is reported instead; see
		// TestAMalformedVersionIsReportedNotFatal.
		{"two-part version is still OpenAPI 3", `openapi: "3.0"`, MediaTypeOpenAPI3, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			mediaType, recognised := Detect([]byte(test.source))
			if mediaType != test.mediaType {
				t.Errorf("mediaType = %q, want %q", mediaType, test.mediaType)
			}
			if recognised != test.recognised {
				t.Errorf("recognised = %v, want %v", recognised, test.recognised)
			}
		})
	}
}

func TestImplemented(t *testing.T) {
	for _, mediaType := range []string{MediaTypeOpenAPI3, MediaTypeOpenAPI2} {
		if !Implemented(mediaType) {
			t.Errorf("%s has a parser", mediaType)
		}
	}
	if Implemented(MediaTypeAPIBlueprint) {
		t.Error("API Blueprint has no parser yet and must not claim otherwise")
	}
}

func TestParseOpenAPI3(t *testing.T) {
	result, err := Parse([]byte(`
openapi: "3.0.0"
info:
  title: Example
  version: "1.0.0"
paths:
  /things:
    get:
      summary: List things
      responses:
        "200":
          description: OK
`), "api.yml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result.MediaType != MediaTypeOpenAPI3 {
		t.Errorf("mediaType = %q", result.MediaType)
	}

	compiled := compile.Compile(result.MediaType, result.Elements, "api.yml")
	if len(compiled.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1", len(compiled.Transactions))
	}
	if want := "Example > /things > List things > 200"; compiled.Transactions[0].Name != want {
		t.Errorf("name = %q, want %q", compiled.Transactions[0].Name, want)
	}
}

// TestUnsupportedFormatsReportRatherThanCrash pins that a format with no parser
// yields a diagnostic, not an error the caller has to special-case.
func TestUnsupportedFormatsReportRatherThanCrash(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{"unrecognised", "# Just a heading\n", "could not be recognised"},
		{"API Blueprint says why, and what to do instead", "FORMAT: 1A\n\n# API\n", "does not support"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := Parse([]byte(test.source), "api")
			if err != nil {
				t.Fatalf("Parse should report through an annotation, not an error: %v", err)
			}

			compiled := compile.Compile(result.MediaType, result.Elements, "api")
			if len(compiled.Transactions) != 0 {
				t.Errorf("transactions = %d, want 0", len(compiled.Transactions))
			}
			if len(compiled.Annotations) == 0 {
				t.Fatal("expected an annotation explaining why nothing was produced")
			}
			if got := compiled.Annotations[0]; !strings.Contains(got.Message, test.want) {
				t.Errorf("annotation = %q, want it to mention %q", got.Message, test.want)
			}
			if compiled.Annotations[0].Type != "error" {
				t.Errorf("type = %q, want error", compiled.Annotations[0].Type)
			}
		})
	}
}

func TestParseRejectsMalformedYAML(t *testing.T) {
	if _, err := Parse([]byte("openapi: \"3.0.0\"\n\tbad: indentation"), "api.yml"); err == nil {
		t.Error("unparseable YAML should be reported as an error")
	}
}

// TestATwoPartVersionIsStillOpenAPI3 pins the fix for the worst first-contact
// failure this tool had.
//
// The specification requires `openapi: major.minor.patch`, so `openapi: 3.0`
// is malformed — but it exists in the wild, and the reference's pattern
// answers it by not recognising the document at all and reporting that API
// Blueprint is unsupported. Being told your OpenAPI file is an unsupported
// format, because of a missing `.0`, is the least useful thing a tool can say.
func TestATwoPartVersionIsStillOpenAPI3(t *testing.T) {
	for _, source := range []string{
		"openapi: 3.0\ninfo: {}\n",
		`{"openapi": "3.1"}`,
		"openapi: '3.0'\n",
		// The well-formed spellings must keep working.
		"openapi: 3.0.3\n",
		`{"openapi": "3.1.0"}`,
	} {
		mediaType, recognised := Detect([]byte(source))
		if !recognised || mediaType != MediaTypeOpenAPI3 {
			t.Errorf("Detect(%q) = %q, %v; want OpenAPI 3 recognised", source, mediaType, recognised)
		}
	}

	// And a document that really is neither is still not OpenAPI 3.
	if _, recognised := Detect([]byte("# Some API\n\n## GET /things\n")); recognised {
		t.Error("API Blueprint should not be detected as OpenAPI")
	}
	// A version that is not 3.x is not ours either.
	if mediaType, _ := Detect([]byte("openapi: 4.0.0\n")); mediaType == MediaTypeOpenAPI3 {
		t.Error("OpenAPI 4 should not be read as OpenAPI 3")
	}
}
