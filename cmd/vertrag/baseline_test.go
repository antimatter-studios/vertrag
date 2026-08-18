package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/runner"
)

// TestBaselineDecidesWhetherAParameterCanBeBlamed pins the rule that keeps a
// broken operation from blaming every parameter it has.
//
// Found against a live API: a download endpoint answering 404 because nothing
// had been read reported three findings saying the server disagreed with its
// description about a `format` value — while returning exactly the same 404 for
// the documented value. The parameter had nothing to do with it.
func TestBaselineDecidesWhetherAParameterCanBeBlamed(t *testing.T) {
	for _, test := range []struct {
		name        string
		documented  string
		answers     int
		want        bool
		wantRefused bool
	}{
		{"the operation works as documented", "200", 200, true, false},
		{"documented as an error, and errors", "404", 404, true, false},
		{"documented as 200, answers 404", "200", 404, false, false},
		{"documented as 200, answers 500", "200", 500, false, false},
		{"documented as 404, answers 200", "404", 200, false, false},
		// A description with no usable status falls back to "did it not error",
		// which is the most that can be said without one.
		{"no documented status, answers 200", "", 200, true, false},
		{"no documented status, answers 500", "", 500, false, false},

		// A locked door is told apart from a disagreement: probing an
		// authenticated API without a credential fails every baseline, and
		// calling that "the operation fails as documented" sends the reader to
		// their handler when what they needed was a credential.
		{"documented as 200, answers 401", "200", 401, false, true},
		{"documented as 200, answers 403", "200", 403, false, true},
		{"no documented status, answers 401", "", 401, false, true},
		// Except where being turned away IS the documented answer.
		{"documented as 401, answers 401", "401", 401, true, false},
		{"documented as 403, answers 403", "403", 403, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.answers)
			}))
			defer server.Close()

			transaction := compile.Transaction{
				Request:  compile.Request{Method: "GET", URI: "/thing"},
				Response: compile.Response{Status: test.documented},
			}

			got := baselineWorks(context.Background(), runner.New(server.URL), transaction, nil)
			if got.ok != test.want {
				t.Errorf("baselineWorks ok = %v, want %v (documented %q, answered %d)",
					got.ok, test.want, test.documented, test.answers)
			}
			if got.refused != test.wantRefused {
				t.Errorf("baselineWorks refused = %v, want %v (documented %q, answered %d)",
					got.refused, test.wantRefused, test.documented, test.answers)
			}
		})
	}
}

// TestAnUnreachableServerIsNotABaseline pins that a transport failure counts as
// the operation not working, rather than as permission to blame parameters.
func TestAnUnreachableServerIsNotABaseline(t *testing.T) {
	transaction := compile.Transaction{
		Request:  compile.Request{Method: "GET", URI: "/thing"},
		Response: compile.Response{Status: "200"},
	}
	// A port nothing is listening on.
	got := baselineWorks(context.Background(), runner.New("http://127.0.0.1:1"), transaction, nil)
	if got.ok {
		t.Error("an unreachable server should not count as a working baseline")
	}
	// Nor as a credential problem: there was no answer to read a status from.
	if got.refused {
		t.Error("an unreachable server was reported as refusing the request")
	}
}
