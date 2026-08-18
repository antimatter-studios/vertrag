package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The end-to-end tests for GraphQL: the built binary, a schema on disk, and a
// server on a port. They are here rather than in the packages because the
// failure they catch is wiring — a flag parsed into the wrong variable, a
// media type detected and then routed nowhere — which every unit test in the
// tree stays green through.
//
// The server is an httptest handler answering canned JSON. Nothing here needs
// a GraphQL implementation: what is under test is the query vertrag sends and
// the judgement it makes of what comes back.

// gqlServer records the operations it was asked for, so a test can assert
// what was NOT sent — which is the whole of the mutation question.
type gqlServer struct {
	mu   sync.Mutex
	sent []string

	// answer overrides the canned reply, for the tests about failure.
	answer func(operation string) string
}

func (s *gqlServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		var request struct {
			Query string `json:"query"`
		}
		json.Unmarshal(payload, &request)

		// `query viewer {` — the operation kind and the field, which is all
		// this server needs to answer.
		words := strings.Fields(request.Query)
		operation := ""
		if len(words) > 1 {
			operation = words[0] + " " + words[1]
		}

		s.mu.Lock()
		s.sent = append(s.sent, operation)
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		// 200 whatever happens, exactly as a GraphQL server does.
		w.Write([]byte(s.answer(operation)))
	})
}

func (s *gqlServer) received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

func conformingAnswers(operation string) string {
	switch operation {
	case "query version":
		return `{"data":{"version":"1.0.0"}}`
	case "query viewer":
		return `{"data":{"viewer":{"id":"1","name":"Ada"}}}`
	case "mutation ping":
		return `{"data":{"ping":"pong"}}`
	default:
		return `{"errors":[{"message":"unknown operation ` + operation + `"}]}`
	}
}

// The schema under test, as somebody would check it in: a query root with a
// scalar and an object field, a mutation root with one field, and a type with
// a nullable field and a non-nullable one.
const testSDL = `
type Query {
  version: String!
  viewer: User!
}

type Mutation {
  ping: String!
}

type User {
  id: ID!
  name: String
}
`

// serveGraphQL stands a server up and writes the schema it answers for.
func serveGraphQL(t *testing.T, answer func(string) string) (endpoint, schemaPath string, server *gqlServer) {
	t.Helper()

	server = &gqlServer{answer: answer}
	http := httptest.NewServer(server.handler())
	t.Cleanup(http.Close)

	schemaPath = filepath.Join(t.TempDir(), "schema.graphql")
	if err := os.WriteFile(schemaPath, []byte(testSDL), 0o600); err != nil {
		t.Fatalf("writing the schema: %v", err)
	}
	return http.URL, schemaPath, server
}

func TestAGraphQLSchemaIsRunEndToEnd(t *testing.T) {
	binary := build(t)
	endpoint, schema, server := serveGraphQL(t, conformingAnswers)

	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color", schema)
	if code != 0 {
		t.Fatalf("exit = %d against a conforming server\n%s", code, output)
	}

	// One transaction per field of the query root, each named for what it
	// asked: the names hooks and `--only` address.
	for _, want := range []string{"Query > version", "Query > viewer"} {
		if !strings.Contains(output, want) {
			t.Errorf("the report does not name %q:\n%s", want, output)
		}
	}
	sent := strings.Join(server.received(), ", ")
	if sent != "query version, query viewer" {
		t.Errorf("the server was asked for %q", sent)
	}
}

// The safety interlock, through the command: a mutation is not sent, and the
// run says so. A run that quietly tested less than the reader believes is the
// failure this repository cares most about — and here the untested operation
// is the one that would have changed something.
func TestAMutationIsNotSentUnlessItWasAskedFor(t *testing.T) {
	binary := build(t)
	endpoint, schema, server := serveGraphQL(t, conformingAnswers)

	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color", schema)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, output)
	}
	for _, operation := range server.received() {
		if strings.HasPrefix(operation, "mutation") {
			t.Errorf("a mutation was sent to a server nobody asked to mutate: %q", operation)
		}
	}
	// Withheld, and said out loud, with the switch that changes it.
	for _, want := range []string{"Mutation > ping", "mutations: true"} {
		if !strings.Contains(output, want) {
			t.Errorf("the run did not say what it withheld (%q missing):\n%s", want, output)
		}
	}

	// Asked for by flag, it goes.
	output, code = runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color",
		"--graphql-mutations", schema)
	if code != 0 {
		t.Fatalf("exit = %d with mutations enabled\n%s", code, output)
	}
	var mutated bool
	for _, operation := range server.received() {
		mutated = mutated || operation == "mutation ping"
	}
	if !mutated {
		t.Errorf("--graphql-mutations did not send the mutation; the server saw %v", server.received())
	}
}

// The reason the body check exists at all: this server answers 200, with a
// JSON content type, to every request — and reports nothing but errors. A run
// that judged the status alone would be green.
func TestAServerThatAnswers200WithNothingButErrorsFailsTheRun(t *testing.T) {
	binary := build(t)
	endpoint, schema, _ := serveGraphQL(t, func(string) string {
		return `{"errors":[{"message":"the resolver exploded","path":["viewer"],` +
			`"extensions":{"code":"INTERNAL_SERVER_ERROR"}}],"data":null}`
	})

	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color", schema)
	if code == 0 {
		t.Fatalf("exit = 0 against a server answering every query with an error; CI would call this a pass\n%s", output)
	}
	// The server's own message is the diagnostic, so it has to reach the
	// report rather than being reduced to "the body did not match".
	for _, want := range []string{"the resolver exploded", "INTERNAL_SERVER_ERROR", "at viewer"} {
		if !strings.Contains(output, want) {
			t.Errorf("the report does not carry %q:\n%s", want, output)
		}
	}
}

// A response missing a field the query asked for is a finding, even though
// every other signal — status, content type, valid JSON — says the exchange
// went perfectly.
func TestAResponseMissingAFieldTheQueryAskedForFailsTheRun(t *testing.T) {
	binary := build(t)
	endpoint, schema, _ := serveGraphQL(t, func(operation string) string {
		if operation == "query viewer" {
			return `{"data":{"viewer":{"name":"Ada"}}}`
		}
		return conformingAnswers(operation)
	})

	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color", schema)
	if code == 0 {
		t.Fatalf("exit = 0 for a response missing a selected field\n%s", output)
	}
	if !strings.Contains(output, "viewer.id") {
		t.Errorf("the report does not name the missing field:\n%s", output)
	}
}

// `vertrag compile` shows what a run would send, which means it withholds the
// same operations — a listing that showed mutations the run then declined to
// send would read as a bug in the run.
func TestCompileShowsTheQueriesAndWithholdsTheMutations(t *testing.T) {
	binary := build(t)
	_, schema, _ := serveGraphQL(t, conformingAnswers)

	output, code := runCommand(t, binary, "compile", schema)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, output)
	}
	if !strings.Contains(output, `"Query > viewer"`) {
		t.Errorf("the compiled transactions do not name the query:\n%s", output)
	}
	if strings.Contains(output, `"name": "Mutation > ping"`) {
		t.Errorf("compile listed a mutation a run would not send:\n%s", output)
	}
	if !strings.Contains(output, "Mutation > ping") {
		t.Errorf("compile said nothing about what it withheld:\n%s", output)
	}

	// The request is what the runner will send: one POST per root field.
	if !strings.Contains(output, `"method": "POST"`) || !strings.Contains(output, `"uri": "/graphql"`) {
		t.Errorf("the compiled request is not a POST to /graphql:\n%s", output)
	}
}

// A probing phase asked for against a GraphQL schema can do nothing yet, and
// has to say so: a phase that ran over nothing and found nothing reads exactly
// like a server that handled everything correctly.
func TestAProbingPhaseSaysItHasNothingToProbeInASchema(t *testing.T) {
	binary := build(t)
	endpoint, schema, _ := serveGraphQL(t, conformingAnswers)

	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color",
		"--phases", "examples,fuzz", schema)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, output)
	}
	if !strings.Contains(output, "nothing to work on in a GraphQL schema") {
		t.Errorf("the fuzz phase ran over nothing without saying so:\n%s", output)
	}
}

// The path is configuration, so it must be settable — and the queries have to
// follow it, which is the half that is easy to leave unwired.
func TestTheGraphQLPathCanBeMoved(t *testing.T) {
	binary := build(t)

	var paths []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"version":"1.0.0","viewer":{"id":"1","name":"Ada"}}}`))
	}))
	defer server.Close()

	_, schema, _ := serveGraphQL(t, conformingAnswers)
	dir := t.TempDir()
	config := filepath.Join(dir, "vertrag.yml")
	os.WriteFile(config, []byte("spec: "+schema+"\nendpoint: "+server.URL+
		"\ngraphql:\n  path: /api/graphql\n"), 0o600)

	output, code := runCommand(t, binary, "run", "--config", config, "--no-color")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, output)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, path := range paths {
		if path != "/api/graphql" {
			t.Errorf("a query went to %q, not to the configured path", path)
		}
	}
	if len(paths) == 0 {
		t.Error("nothing was sent at all")
	}
}
