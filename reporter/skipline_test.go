package reporter

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/runner"
)

// Every transaction line names its method. A skip is a transaction line.
//
// It did not, for the transactions a hook removes: those results were built
// without the request, so the report printed `skip:  /api/v1/thing > …` with two
// spaces and a gap where every pass and fail line carries `GET`. The method is
// also the only thing distinguishing a transaction line from an indented detail
// line, so a suite counting its own report — inpace's `bin/test` does exactly
// this — counted 98 skips as zero and reported a total of 74 instead of 172.
func TestEveryTransactionLineNamesItsMethod(t *testing.T) {
	results := []runner.Result{
		{
			Name:    "/api/v1/a > Get a > 200 > application/json",
			Status:  runner.StatusPass,
			Request: runner.Request{Method: "GET", URI: "/api/v1/a"},
		},
		{
			Name:    "/api/v1/b > Make b > 201 > application/json",
			Status:  runner.StatusFail,
			Request: runner.Request{Method: "POST", URI: "/api/v1/b"},
			Errors:  []string{"statusCode: expected 201, got 500"},
		},
		{
			Name:    "/api/v1/c > Remove c > 204 > application/json",
			Status:  runner.StatusSkip,
			Request: runner.Request{Method: "DELETE", URI: "/api/v1/c"},
		},
		{
			Name:    "/api/v1/d > Read d > 200 > application/json",
			Status:  runner.StatusError,
			Request: runner.Request{Method: "PUT", URI: "/api/v1/d"},
			Errors:  []string{"connection refused"},
		},
	}

	var out bytes.Buffer
	CLI{Out: &out}.Report(results)

	// The anchor a consumer would use: a status word, then a method in capitals.
	line := regexp.MustCompile(`(?im)^(pass|fail|skip|error):\s+([A-Z]+)\s`)
	for _, want := range []string{"GET", "POST", "DELETE", "PUT"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no line names the %s transaction", want)
		}
	}

	matched := line.FindAllStringSubmatch(out.String(), -1)
	if len(matched) != len(results) {
		t.Errorf("%d of %d transaction lines carry a method:\n%s",
			len(matched), len(results), out.String())
	}

	// And specifically no `skip:` followed by whitespace where a method belongs.
	if regexp.MustCompile(`(?im)^skip:\s{2,}/`).MatchString(out.String()) {
		t.Errorf("a skip line has a gap where its method should be:\n%s", out.String())
	}
}
