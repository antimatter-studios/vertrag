package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/validate"
)

// The expectation for `query viewer { viewer { id name pets } }`, where the
// schema says `viewer: User!`, `id: ID!`, `name: String` and `pets: [Pet!]`,
// and where `barks` was asked for inside `... on Dog`.
func viewerExpectation() *compile.GraphQL {
	return &compile.GraphQL{
		Operation: "query",
		Field:     "viewer",
		Data: []compile.GraphQLSelection{{
			Key: "viewer", Type: "User!", NonNull: true,
			Fields: []compile.GraphQLSelection{
				{Key: "id", Type: "ID!", NonNull: true},
				{Key: "name", Type: "String"},
				{Key: "pets", Type: "[Pet!]", ListDepth: 1, ElementNonNull: true, Fields: []compile.GraphQLSelection{
					{Key: "__typename", Type: "String!", NonNull: true},
					{Key: "barks", Type: "Boolean!", NonNull: true, Conditional: true},
				}},
			},
		}},
	}
}

func answered(body string) validate.Message {
	return validate.Message{StatusCode: "200", Headers: map[string]string{"content-type": "application/json"}, Body: body}
}

func TestAGraphQLResponseIsJudgedOnItsBodyRatherThanItsStatus(t *testing.T) {
	expected := validate.Message{StatusCode: "200"}

	for _, test := range []struct {
		name string
		body string
		want []string
	}{
		{
			// The case the whole check exists for: 200, a content type that
			// matches, a body — and the server answered nothing at all.
			name: "an errors array is a failure however good the status looks",
			body: `{"errors":[{"message":"Cannot query field \"viewer\"","path":["viewer"],` +
				`"extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}],"data":null}`,
			want: []string{`Cannot query field "viewer"`, "GRAPHQL_VALIDATION_FAILED", "at viewer"},
		},
		{
			name: "a nested error keeps its path, which is the diagnostic",
			body: `{"data":{"viewer":null},"errors":[{"message":"boom","path":["viewer","pets",0,"barks"]}]}`,
			want: []string{"boom", "at viewer.pets[0].barks"},
		},
		{
			name: "neither data nor errors is not a GraphQL response",
			body: `{"extensions":{}}`,
			want: []string{"neither `data` nor `errors`"},
		},
		{
			name: "an empty errors array is a violation of its own",
			body: `{"data":{"viewer":{"id":"1","name":null,"pets":null}},"errors":[]}`,
			want: []string{"empty `errors` array"},
		},
		{
			name: "a null data with nothing to explain it",
			body: `{"data":null}`,
			want: []string{"`data` is null"},
		},
		{
			name: "a field the query asked for and the response does not carry",
			body: `{"data":{"viewer":{"name":"Ada","pets":null}}}`,
			want: []string{"asked for `viewer.id`"},
		},
		{
			name: "null where the schema promised non-null",
			body: `{"data":{"viewer":{"id":null,"name":null,"pets":null}}}`,
			want: []string{"`viewer.id` is null, but the schema declares it ID!"},
		},
		{
			name: "a null element of a list of non-null elements",
			body: `{"data":{"viewer":{"id":"1","name":null,"pets":[{"__typename":"Cat"},null]}}}`,
			want: []string{"`viewer.pets[1]` is null, but the schema declares it [Pet!]"},
		},
		{
			name: "an object where the schema promised a list",
			body: `{"data":{"viewer":{"id":"1","name":null,"pets":{"__typename":"Cat"}}}}`,
			want: []string{"`viewer.pets` is not a list"},
		},
		{
			name: "a body that is not JSON at all",
			body: `<html>502 Bad Gateway</html>`,
			want: []string{"not a JSON object"},
		},
		{
			// Everything the query asked for, nothing the schema forbids: a
			// nullable field that is null, and a fragment field that is absent
			// because this pet is not a Dog.
			name: "a conforming response says nothing",
			body: `{"data":{"viewer":{"id":"1","name":null,"pets":[{"__typename":"Cat"}]}}}`,
			want: nil,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			findings := graphqlResponseFindings(viewerExpectation(), expected, answered(test.body))
			if len(test.want) == 0 {
				if len(findings) > 0 {
					t.Fatalf("a conforming response was reported: %v", findings)
				}
				return
			}
			joined := strings.Join(findings, "\n")
			for _, want := range test.want {
				if !strings.Contains(joined, want) {
					t.Errorf("findings do not say %q:\n%s", want, joined)
				}
			}
		})
	}
}

// One root cause, one finding. A server that answered errors has already
// explained itself, and walking the selection as well would bury that
// explanation under a dozen findings that all restate it.
func TestAnErrorResponseIsNotAlsoReportedAsAMissingField(t *testing.T) {
	findings := graphqlResponseFindings(viewerExpectation(), validate.Message{StatusCode: "200"},
		answered(`{"data":{"viewer":null},"errors":[{"message":"the resolver threw"}]}`))

	if len(findings) != 1 {
		t.Fatalf("one error produced %d findings:\n%s", len(findings), strings.Join(findings, "\n"))
	}
}

// The shape of a list's elements is checked once. Every element comes from one
// resolver and one type, so a missing field is one fault — reporting it per
// row would bury a hundred-row response's real findings under a hundred copies
// of the same one. Nullability is still checked per element, because that is
// the fault that genuinely varies down a list.
func TestAListReportsItsShapeOnceAndItsNullsIndividually(t *testing.T) {
	body := `{"data":{"viewer":{"id":"1","name":null,"pets":[` +
		`{},{},null,{},null` +
		`]}}}`

	findings := graphqlResponseFindings(viewerExpectation(), validate.Message{StatusCode: "200"}, answered(body))

	var shape, nulls int
	for _, finding := range findings {
		switch {
		case strings.Contains(finding, "asked for"):
			shape++
		case strings.Contains(finding, "is null"):
			nulls++
		}
	}
	if shape != 1 {
		t.Errorf("the missing `__typename` was reported %d times:\n%s", shape, strings.Join(findings, "\n"))
	}
	if nulls != 2 {
		t.Errorf("%d null elements reported, want 2:\n%s", nulls, strings.Join(findings, "\n"))
	}
}

// A response that was not the expected one at all is one fault. Reporting it
// as a status mismatch AND as a malformed GraphQL body is the cascade that
// teaches a reader to skim a report.
func TestAnUnexpectedStatusIsNotAlsoReportedAsABadGraphQLBody(t *testing.T) {
	findings := graphqlResponseFindings(viewerExpectation(),
		validate.Message{StatusCode: "200"},
		validate.Message{StatusCode: "502", Body: "<html>Bad Gateway</html>"})

	if len(findings) > 0 {
		t.Errorf("a 502 was reported twice: %v", findings)
	}
}

// The whole point, through the runner: a server answering 200 to everything,
// as GraphQL servers do, must not produce a passing run.
func TestARunAgainstAGraphQLServerFailsOnTheBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 200, always, exactly as a GraphQL server does.
		if strings.Contains(requestBody(r), "broken") {
			w.Write([]byte(`{"errors":[{"message":"no such resolver","path":["broken"]}]}`))
			return
		}
		w.Write([]byte(`{"data":{"viewer":{"id":"1","name":"Ada","pets":[]}}}`))
	}))
	defer server.Close()

	good := compile.Transaction{
		Name:     "Query > viewer",
		Request:  compile.Request{Method: "POST", URI: "/graphql", Body: `{"query":"query viewer { viewer { id } }"}`},
		Response: compile.Response{Status: "200"},
		GraphQL:  viewerExpectation(),
	}
	bad := good
	bad.Name = "Query > broken"
	bad.Request.Body = `{"query":"query broken { broken { id } }"}`

	results, err := New(server.URL).Run(context.Background(), []compile.Transaction{good, bad})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if results[0].Status != StatusPass {
		t.Errorf("a conforming answer failed: %v", results[0].Errors)
	}
	if results[1].Status != StatusFail {
		t.Fatalf("a 200 carrying nothing but errors passed; a run of a broken server would be green")
	}
	if !strings.Contains(strings.Join(results[1].Errors, "\n"), "no such resolver") {
		t.Errorf("the server's own message is not in the report: %v", results[1].Errors)
	}
}

func requestBody(r *http.Request) string {
	var out strings.Builder
	if r.Body != nil {
		buffer := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buffer)
			out.Write(buffer[:n])
			if err != nil {
				break
			}
		}
	}
	return out.String()
}
