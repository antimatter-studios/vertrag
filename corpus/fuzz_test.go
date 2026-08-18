package corpus_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/corpus"
	"github.com/antimatter-studios/vertrag/fuzz"
	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// probe generates against a corpus server and returns what it found.
//
// Every body and every parameter carrying a schema is probed in both modes,
// which is what `vertrag fuzz` does — reproduced here rather than shelling out
// so a failure points at a package rather than at a subprocess.
func probe(t *testing.T, name string, faults ...corpus.Fault) []fuzz.Finding {
	t.Helper()

	server, err := corpus.NewNamed(name, faults...)
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}
	http := httptest.NewServer(server.Handler())
	t.Cleanup(http.Close)

	source, err := corpus.Load(name)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	parsed, err := apidesc.Parse(source, name)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	compiled := compile.Compile(parsed.MediaType, parsed.Elements, name)

	engine := runner.New(http.URL)
	options := fuzz.Options{Cases: 25, Seed: 9}

	var findings []fuzz.Finding
	for _, transaction := range compiled.Transactions {
		for _, mode := range []generate.Mode{generate.Valid, generate.Invalid} {
			findings = append(findings,
				probeBody(t, engine, transaction, mode, options)...)
			findings = append(findings,
				probeParameters(t, engine, transaction, mode, options)...)
		}
	}
	return findings
}

func probeBody(t *testing.T, engine *runner.Runner, transaction compile.Transaction,
	mode generate.Mode, options fuzz.Options) []fuzz.Finding {
	t.Helper()

	if strings.TrimSpace(transaction.Request.Schema) == "" || !acceptsJSON(transaction.Request) {
		return nil
	}
	var schema generate.Schema
	if json.Unmarshal([]byte(transaction.Request.Schema), &schema) != nil {
		return nil
	}

	send := func(ctx context.Context, value any) (validate.Message, error) {
		body, _ := value.(string)
		attempt := transaction
		attempt.Request.Body = body
		return engine.Send(ctx, attempt)
	}

	finding, found := fuzz.Probe(context.Background(), schema, mode, send, options)
	if !found || finding.Unprobeable {
		return nil
	}
	return []fuzz.Finding{finding}
}

func probeParameters(t *testing.T, engine *runner.Runner, transaction compile.Transaction,
	mode generate.Mode, options fuzz.Options) []fuzz.Finding {
	t.Helper()

	var findings []fuzz.Finding
	for _, parameter := range transaction.Request.Parameters {
		if strings.TrimSpace(parameter.Schema) == "" {
			continue
		}
		var schema generate.Schema
		if json.Unmarshal([]byte(parameter.Schema), &schema) != nil {
			continue
		}
		if !fuzz.Probeable(schema, parameter.Style) {
			continue
		}

		send := func(ctx context.Context, value any) (validate.Message, error) {
			request, err := transaction.Request.SetParameter(parameter, value)
			if err != nil {
				return validate.Message{}, err
			}
			attempt := transaction
			attempt.Request = request
			return engine.Send(ctx, attempt)
		}

		subject := fuzz.Subject{In: parameter.In, Name: parameter.Name}
		finding, found := fuzz.ProbeParameter(context.Background(), subject, schema, mode, send, options)
		if found && !finding.Unprobeable {
			findings = append(findings, finding)
		}
	}
	return findings
}

func describe(findings []fuzz.Finding) string {
	var out []string
	for _, finding := range findings {
		out = append(out, finding.Subject.Describe()+": "+finding.Message)
	}
	return strings.Join(out, "\n  ")
}

// TestGenerationFindsNothingAgainstAConformingServer is the soundness baseline,
// and the one that matters most for a tool that generates its own inputs.
//
// The server enforces exactly what its description states, so every finding
// here is vertrag inventing one. That is the failure mode which destroys trust
// fastest: a generated report full of violations that are not violations
// teaches people to stop reading it, and unlike a missed bug it is invisible
// until someone checks by hand.
//
// It exercises the guard in fuzz.Probe across every shape the corpus carries —
// numeric extremes, patterns, formats, enums, arrays, references, unicode.
func TestGenerationFindsNothingAgainstAConformingServer(t *testing.T) {
	for _, name := range corpus.Names() {
		t.Run(name, func(t *testing.T) {
			if findings := probe(t, name); len(findings) > 0 {
				t.Errorf("%d false finding(s) against a conforming server:\n  %s",
					len(findings), describe(findings))
			}
		})
	}
}

// TestGenerationFindsTheFaultsARunCannot closes the catalogue.
//
// Three faults concern what a server does with input it should refuse, and a
// deterministic run never sends any: it sends the documented example, which is
// valid. They are the reason `vertrag fuzz` exists, and until now nothing
// asserted it found them.
func TestGenerationFindsTheFaultsARunCannot(t *testing.T) {
	for _, fault := range []corpus.Fault{
		corpus.FaultAcceptsAnyParameter,
		corpus.FaultAcceptsAnyBody,
		corpus.FaultCrashesOnBadInput,
	} {
		t.Run(string(fault), func(t *testing.T) {
			var found []string
			for _, name := range corpus.Names() {
				if len(probe(t, name, fault)) > 0 {
					found = append(found, name)
				}
			}

			if len(found) == 0 {
				t.Errorf("%s was committed against every description and generation found nothing", fault)
			}
			t.Logf("found in %d of %d description(s): %s",
				len(found), len(corpus.Names()), strings.Join(found, ", "))
		})
	}
}

// acceptsJSON reports whether a generated JSON body can be sent to this
// operation at all. A multipart schema describes the parts of a body rather
// than a document, so posting JSON at it gets a 400 that says nothing.
func acceptsJSON(request compile.Request) bool {
	for _, header := range request.Headers {
		if !strings.EqualFold(header.Name, "Content-Type") {
			continue
		}
		media := strings.ToLower(strings.TrimSpace(strings.SplitN(header.Value, ";", 2)[0]))
		return media == "application/json" || strings.HasSuffix(media, "+json")
	}
	return true
}
