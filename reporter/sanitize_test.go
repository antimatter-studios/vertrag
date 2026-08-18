package reporter

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

func secretResult() runner.Result {
	return runner.Result{
		Name:   "a",
		Status: runner.StatusFail,
		Request: runner.Request{
			Method: "GET", URI: "/x", URL: "http://h/x",
			Headers: map[string]string{
				"Authorization": "Bearer topsecret",
				"Cookie":        "jwt=topsecret",
				"X-Tenant":      "acme",
			},
		},
		Actual: validate.Message{
			StatusCode: "500",
			Headers:    map[string]string{"Set-Cookie": "session=topsecret; Path=/", "Content-Type": "text/plain"},
		},
		Errors: []string{"wrong"},
	}
}

// TestEveryReporterRedactsCredentials: one policy, every format. A junit file
// that redacted while the terminal leaked is not a configuration anyone means.
func TestEveryReporterRedactsCredentials(t *testing.T) {
	SetSanitize(true)
	t.Cleanup(func() { SetSanitize(true) })

	for name, build := range map[string]func(*strings.Builder) Reporter{
		"cli":      func(b *strings.Builder) Reporter { return CLI{Out: b} },
		"markdown": func(b *strings.Builder) Reporter { return Markdown{Out: b} },
		"html":     func(b *strings.Builder) Reporter { return HTML{Out: b} },
		"junit":    func(b *strings.Builder) Reporter { return JUnit{Out: b} },
		// The two recording formats belong here more than any of the others do.
		// A cassette is committed to a repository and replayed later, so a
		// credential that reaches one travels further and lives longer than one
		// in a terminal log ever could.
		"har": func(b *strings.Builder) Reporter { return HAR{Out: b} },
		"vcr": func(b *strings.Builder) Reporter { return VCR{Out: b} },
	} {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			build(&out).Report([]runner.Result{secretResult()})
			text := out.String()
			if strings.Contains(text, "topsecret") {
				t.Errorf("%s leaked a credential:\n%s", name, text)
			}
			// HTML and XML escape the angle brackets, which is right; the
			// marker must be there in one spelling or the other.
			if !strings.Contains(text, Redacted) && !strings.Contains(text, "&lt;redacted&gt;") {
				t.Errorf("%s shows no redaction marker, so the header vanished rather than being redacted:\n%s", name, text)
			}
			if !strings.Contains(text, "acme") {
				t.Errorf("%s lost a harmless header:\n%s", name, text)
			}
		})
	}
}

// TestNoSanitizeShowsEverything is the escape hatch for the person debugging
// their own credential — and proves the default was doing something.
func TestNoSanitizeShowsEverything(t *testing.T) {
	SetSanitize(false)
	t.Cleanup(func() { SetSanitize(true) })

	var out strings.Builder
	CLI{Out: &out}.Report([]runner.Result{secretResult()})
	if !strings.Contains(out.String(), "Bearer topsecret") {
		t.Errorf("--no-sanitize did not show the credential:\n%s", out.String())
	}
}

// TestSanitizeHeaderAddsAName: a project's own credential header joins the
// list for the run.
func TestSanitizeHeaderAddsAName(t *testing.T) {
	SetSanitize(true)
	AddRedactedHeader("X-Tenant-Secret")
	t.Cleanup(func() { delete(redactedHeaders, "x-tenant-secret") })

	if got := Redact("x-tenant-secret", "hush"); got != Redacted {
		t.Errorf("Redact(added header) = %q, want %q", got, Redacted)
	}
	if got := Redact("X-Tenant", "acme"); got != "acme" {
		t.Errorf("Redact(harmless) = %q, want acme", got)
	}
}
