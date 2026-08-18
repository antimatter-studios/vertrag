package reporter

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/runner"
)

func TestCurlRepeatsTheRequest(t *testing.T) {
	line := Curl(runner.Request{
		Method: "POST",
		URL:    "http://localhost:4000/api/v1/things?limit=10",
		Headers: map[string]string{
			"Content-Type": "application/json",
			"Accept":       "application/json",
		},
		Body: `{"name":"it's"}`,
	})

	want := `curl -X POST 'http://localhost:4000/api/v1/things?limit=10'` +
		` -H 'Accept: application/json' -H 'Content-Type: application/json'` +
		` --data '{"name":"it'\''s"}'`
	if line != want {
		t.Errorf("Curl =\n%s\nwant\n%s", line, want)
	}
}

// Headers render in name order so the same request always renders the same
// line — map iteration order must not reach the report.
func TestCurlHeadersAreSorted(t *testing.T) {
	request := runner.Request{
		Method: "GET",
		URL:    "http://localhost:4000/a",
		Headers: map[string]string{
			"Zebra": "1", "Alpha": "2", "Mango": "3",
		},
	}

	first := Curl(request)
	for i := 0; i < 20; i++ {
		if again := Curl(request); again != first {
			t.Fatalf("two renderings differ:\n%s\n%s", first, again)
		}
	}
	if !strings.Contains(first, `'Alpha: 2' -H 'Mango: 3' -H 'Zebra: 1'`) {
		t.Errorf("headers are not in name order: %s", first)
	}
}

// A curl line is written to be pasted, and the paste travels further than the
// terminal it came from — credentials must not ride along.
func TestCurlRedactsCredentials(t *testing.T) {
	line := Curl(runner.Request{
		Method: "GET",
		URL:    "http://localhost:4000/a",
		Headers: map[string]string{
			"authorization": "Bearer topsecret",
			"Cookie":        "jwt_token=topsecret",
			"X-Api-Key":     "topsecret",
			"X-Tenant":      "acme",
		},
	})

	if strings.Contains(line, "topsecret") {
		t.Errorf("a credential survived into the repro line: %s", line)
	}
	if !strings.Contains(line, "'authorization: <redacted>'") {
		t.Errorf("the redaction is not shown where the header was: %s", line)
	}
	if !strings.Contains(line, "'X-Tenant: acme'") {
		t.Errorf("a harmless header was lost: %s", line)
	}
}

// A request that never carried its absolute address cannot be repeated, and a
// half-address would be worse than none.
func TestCurlWithoutAnAddressSaysNothing(t *testing.T) {
	if line := Curl(runner.Request{Method: "GET", URI: "/a"}); line != "" {
		t.Errorf("Curl = %q, want empty without a URL", line)
	}
}
