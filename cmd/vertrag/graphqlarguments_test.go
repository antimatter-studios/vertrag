package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// The end-to-end tests for generated GraphQL arguments: the built binary, a
// schema on disk, and a server that reads the `variables` it is sent.
//
// They are here rather than in compile or fuzz because what they catch is
// wiring — a schema built and then handed to no probe, a pin checked against
// bodies a GraphQL run does not have — which every unit test in the tree stays
// green through.

// argumentSDL is written around its arguments: an id that must name something,
// a required string, an optional Int, a boolean flag that decides whether the
// operation does something irreversible, and a mutation that must never be
// sent.
const argumentSDL = `
type Query {
  version: String!
  user(id: ID!): User
  search(term: String!, limit: Int): [User!]!
  report(dryRun: Boolean!): String!
}

type Mutation {
  deleteUser(id: ID!): Boolean!
}

type User {
  id: ID!
  name: String
}
`

// argumentServer is a GraphQL server that actually reads its variables: it
// coerces each one the way the schema says and refuses what does not fit, which
// is what makes an invalid-mode probe mean anything. A server that accepted
// everything would report a validation bypass on every case, and one that
// refused everything would report a disagreement on every case.
type argumentServer struct {
	mu        sync.Mutex
	sent      []string
	variables []map[string]any
	live      int

	// unknownIDs answers a lookup by an id it does not hold with a GraphQL
	// error rather than a null, which is what most servers do.
	unknownIDs bool
}

var gqlOperation = regexp.MustCompile(`^\s*(query|mutation)\s+([_A-Za-z][_0-9A-Za-z]*)`)

func (s *argumentServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(payload, &request)

		field := ""
		if match := gqlOperation.FindStringSubmatch(request.Query); match != nil {
			field = match[1] + " " + match[2]
		}

		s.mu.Lock()
		s.sent = append(s.sent, field)
		s.variables = append(s.variables, request.Variables)
		if field == "query report" && request.Variables["dryRun"] != true {
			s.live++
		}
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		// 200 whatever happens, exactly as a GraphQL server does.
		_, _ = w.Write([]byte(s.answer(field, request.Variables)))
	})
}

// answer coerces the variables the way the schema declares them, then serves
// the field.
func (s *argumentServer) answer(field string, variables map[string]any) string {
	refuse := func(message string) string {
		return fmt.Sprintf(`{"errors":[{"message":%q}],"data":null}`, message)
	}

	switch field {
	case "query version":
		return `{"data":{"version":"1.0.0"}}`

	case "query user":
		id, ok := variables["id"]
		if !ok {
			return refuse("id is required")
		}
		switch id.(type) {
		case string, float64:
		default:
			return refuse("ID cannot represent a non-string, non-integer value")
		}
		if s.unknownIDs {
			// A server that holds none of the ids vertrag can invent, which is
			// the ordinary case: the generated one names nothing.
			return refuse("no user with the id " + fmt.Sprint(id))
		}
		if fmt.Sprint(id) == "1" {
			return `{"data":{"user":{"id":"1","name":"Ada"}}}`
		}
		return `{"data":{"user":null}}`

	case "query search":
		term, ok := variables["term"].(string)
		if !ok || term == "" {
			return refuse("String cannot represent a non-string value")
		}
		if limit, given := variables["limit"]; given && limit != nil {
			number, isNumber := limit.(float64)
			if !isNumber || number != float64(int32(number)) {
				return refuse("Int cannot represent non 32-bit signed integer value")
			}
		}
		return `{"data":{"search":[{"id":"1","name":"Ada"}]}}`

	case "query report":
		if _, ok := variables["dryRun"].(bool); !ok {
			return refuse("Boolean cannot represent a non-boolean value")
		}
		return `{"data":{"report":"ok"}}`
	}
	return refuse("unknown operation " + field)
}

func (s *argumentServer) received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

func (s *argumentServer) sentVariables() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.variables...)
}

func (s *argumentServer) liveReports() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live
}

// argumentProject stands the server up and writes a schema and a vertrag.yml
// into a directory the commands can be run from.
func argumentProject(t *testing.T, server *argumentServer, fuzzSection string) string {
	t.Helper()

	endpoint := httptest.NewServer(server.handler())
	t.Cleanup(endpoint.Close)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "schema.graphql"), []byte(argumentSDL), 0o600); err != nil {
		t.Fatal(err)
	}
	config := "spec: ./schema.graphql\nendpoint: " + endpoint.URL + "\n" + fuzzSection
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The examples phase now sends the fields it used to withhold, with the value
// vertrag built from each argument's own type — declared as a variable and
// passed in `variables`, never written into the query.
func TestARequiredArgumentIsSentWithAGeneratedValueEndToEnd(t *testing.T) {
	binary := build(t)
	server := &argumentServer{}
	dir := argumentProject(t, server, "")

	output, code := runIn(t, dir, binary, "run")
	if code != 0 {
		t.Fatalf("exit = %d against a server that reads its variables\n%s", code, output)
	}

	sent := server.received()
	for _, want := range []string{"query user", "query search", "query report"} {
		if !contains(sent, want) {
			t.Errorf("%q was never sent; the run saw %v", want, sent)
		}
	}

	found := false
	for _, values := range server.sentVariables() {
		if values["id"] == "1" {
			found = true
		}
	}
	if !found {
		t.Errorf("no request carried a generated id in its variables: %v", server.sentVariables())
	}

	// And the run says the values are its own invention, because a reader who
	// does not know that reads a finding about `user` as a broken resolver.
	if !strings.Contains(output, "argument values vertrag generated") {
		t.Errorf("the run does not say the arguments were generated:\n%s", output)
	}
}

// The probing phases had nothing to work on in a schema and said so. They have
// now: one generated value per argument, drawn from the argument's own type.
func TestTheProbingPhasesGenerateGraphQLArgumentValues(t *testing.T) {
	binary := build(t)

	for _, phase := range []string{"fuzz", "coverage"} {
		t.Run(phase, func(t *testing.T) {
			server := &argumentServer{}
			dir := argumentProject(t, server, "fuzz:\n  cases: 12\n  seed: 5\n")

			output, code := runIn(t, dir, binary, phase)
			if code > 2 {
				t.Fatalf("the %s run did not complete, exit %d:\n%s", phase, code, output)
			}
			if strings.Contains(output, "nothing to work on in a GraphQL schema") {
				t.Errorf("the phase still claims it can do nothing:\n%s", output)
			}
			if strings.Contains(output, "0 request(s) sent") || strings.Contains(output, "0 probe(s) sent") {
				t.Errorf("the phase sent nothing:\n%s", output)
			}

			// Something other than the compiled example must have gone out, or
			// nothing was generated at all.
			varied := false
			for _, values := range server.sentVariables() {
				if term, ok := values["term"].(string); ok && term != "vertrag" {
					varied = true
				}
				if _, ok := values["limit"]; ok {
					varied = true
				}
			}
			if !varied {
				t.Errorf("every request carried the compiled example: %v", server.sentVariables())
			}
		})
	}
}

// The mutation gate is the OUTER interlock, and generating argument values must
// never become a way round it. `deleteUser(id: ID!)` is exactly the shape that
// would have been unreachable before this round for want of an id and reachable
// after it — so every phase is run, and the server is asked what it saw.
func TestAWithheldMutationIsNotReachableByGeneration(t *testing.T) {
	binary := build(t)

	for _, command := range [][]string{
		{"run", "--phases", "examples,coverage,fuzz"},
		{"fuzz", "--cases", "10"},
		{"coverage"},
	} {
		t.Run(command[0], func(t *testing.T) {
			server := &argumentServer{}
			dir := argumentProject(t, server, "fuzz:\n  seed: 11\n")

			output, code := runIn(t, dir, binary, command...)
			if code > 2 {
				t.Fatalf("the run did not complete, exit %d:\n%s", code, output)
			}

			for _, field := range server.received() {
				if strings.HasPrefix(field, "mutation") {
					t.Fatalf("a withheld mutation reached the server: %q", field)
				}
			}
			if len(server.received()) == 0 {
				t.Fatal("nothing was sent at all, so the gate proves nothing")
			}
			if !strings.Contains(output, "Mutation > deleteUser") {
				t.Errorf("the withheld mutation was not named:\n%s", output)
			}
		})
	}
}

// `fuzz.pin` pins request-BODY fields by name. A GraphQL schema has no request
// body to name one in — the flag that decides whether the operation does
// something irreversible is an ARGUMENT — so the pin has to hold there or it
// holds nothing for a whole format.
func TestAPinnedGraphQLArgumentIsHeldInEveryGeneratedQuery(t *testing.T) {
	binary := build(t)

	// First unpinned, to establish that the danger is real and the test below
	// is not passing for want of anything to catch.
	loose := &argumentServer{}
	dir := argumentProject(t, loose, "fuzz:\n  cases: 12\n  seed: 5\n")
	if _, code := runIn(t, dir, binary, "fuzz"); code > 2 {
		t.Fatalf("the unpinned run did not complete, exit %d", code)
	}
	if loose.liveReports() == 0 {
		t.Fatal("no request ever reached `report` with dryRun other than true; the pin below would prove nothing")
	}
	t.Logf("unpinned: %d requests reported live", loose.liveReports())

	// Now the same run with the pin, through both probing phases: a pin that
	// held on one and not the other would not be a pin.
	for _, phase := range []string{"fuzz", "coverage"} {
		t.Run(phase, func(t *testing.T) {
			held := &argumentServer{}
			dir := argumentProject(t, held, "fuzz:\n  cases: 12\n  seed: 5\n  pin:\n    dryRun: true\n")

			output, code := runIn(t, dir, binary, phase)
			if code > 2 {
				t.Fatalf("the pinned run did not complete, exit %d:\n%s", code, output)
			}
			if len(held.received()) == 0 {
				t.Fatal("the pinned run sent nothing, so the pin proved nothing")
			}
			if live := held.liveReports(); live != 0 {
				t.Errorf("%d requests reached the server with dryRun unpinned", live)
			}
			// Including the baseline request, which vertrag sends on its own
			// initiative once per operation and which no generation path could
			// have held.
			for _, values := range held.sentVariables() {
				if got, present := values["dryRun"]; present && got != true {
					t.Errorf("a request carried dryRun=%v", got)
				}
			}
			if !strings.Contains(output, "dryRun") {
				t.Errorf("the run does not say what it pinned:\n%s", output)
			}
		})
	}
}

// A pin that matches nothing is not a safety control, it only looks like one —
// and the run must stop before the first request rather than after it.
func TestAPinNamingAnArgumentNoFieldDeclaresStopsTheRun(t *testing.T) {
	binary := build(t)
	server := &argumentServer{}
	dir := argumentProject(t, server, "fuzz:\n  cases: 4\n  pin:\n    dryrun: true\n")

	output, code := runIn(t, dir, binary, "fuzz")
	if code == 0 {
		t.Fatalf("a pin naming nothing should stop the run:\n%s", output)
	}
	if !strings.Contains(output, "dryrun") {
		t.Errorf("the refusal does not name the argument:\n%s", output)
	}
	if len(server.received()) != 0 {
		t.Errorf("%d requests went out before the pin was checked", len(server.received()))
	}
}

// An `ID!` argument names a row that must already exist, and vertrag can shape
// an identifier but cannot possess one. A server answering "no such user" to a
// generated id is doing its job, so the run passes — and says which operations
// are in that position, because the exemption is a real cost and an unstated
// one would be worse.
func TestAGeneratedIdentifierThatNamesNothingDoesNotFailTheRun(t *testing.T) {
	binary := build(t)
	server := &argumentServer{unknownIDs: true}
	dir := argumentProject(t, server, "")

	output, code := runIn(t, dir, binary, "run")
	if code != 0 {
		t.Fatalf("a server refusing a made-up id failed the run, exit %d:\n%s", code, output)
	}
	if !strings.Contains(output, "cannot possess one") {
		t.Errorf("the run does not say which operations carry a generated id:\n%s", output)
	}
	if !strings.Contains(output, `argument "id" of user`) {
		t.Errorf("the note does not name the argument:\n%s", output)
	}
}

// The stateful phase is the one that still has nothing to follow in a schema,
// and a phase that was asked for and silently did nothing is the failure this
// repository keeps meeting.
func TestTheStatefulPhaseSaysItHasNothingToFollowInASchema(t *testing.T) {
	binary := build(t)
	server := &argumentServer{}
	dir := argumentProject(t, server, "")

	output, code := runIn(t, dir, binary, "run", "--phases", "examples,stateful")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, output)
	}
	if !strings.Contains(output, "nothing to work on in a GraphQL schema") {
		t.Errorf("the stateful phase ran over nothing without saying so:\n%s", output)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
