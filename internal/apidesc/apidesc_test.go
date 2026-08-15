package apidesc

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/internal/compile"
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
		{"API Blueprint is the fallback", "# My API\n", MediaTypeAPIBlueprint, false},

		// A two-part version is not matched, because the reference requires all
		// three. Accepting it here would make vertrag parse documents Dredd
		// rejects.
		{"two-part version is not OpenAPI 3", `openapi: "3.0"`, MediaTypeAPIBlueprint, false},
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
	if !Implemented(MediaTypeOpenAPI3) {
		t.Error("OpenAPI 3 has a parser")
	}
	for _, mediaType := range []string{MediaTypeOpenAPI2, MediaTypeAPIBlueprint} {
		if Implemented(mediaType) {
			t.Errorf("%s has no parser yet and must not claim otherwise", mediaType)
		}
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
		{"OpenAPI 2", `swagger: "2.0"`, "OpenAPI 2 documents are not supported yet"},
		{"unrecognised", "# Just a heading\n", "Could not recognize API description format"},
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
