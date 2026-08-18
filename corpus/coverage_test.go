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
	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// cover sends every boundary probe against a corpus server and returns the
// findings — what `vertrag coverage` does, reproduced in-process.
func cover(t *testing.T, name string, faults ...corpus.Fault) []fuzz.CoverageFinding {
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

	var findings []fuzz.CoverageFinding
	for _, transaction := range compiled.Transactions {
		// Body.
		if strings.TrimSpace(transaction.Request.Schema) != "" {
			var schema map[string]any
			if json.Unmarshal([]byte(transaction.Request.Schema), &schema) == nil {
				send := func(ctx context.Context, value any) (validate.Message, error) {
					body, _ := value.(string)
					attempt := transaction
					attempt.Request.Body = body
					return engine.Send(ctx, attempt)
				}
				for _, outcome := range fuzz.Cover(context.Background(), fuzz.Subject{In: fuzz.InBody},
					mediaOf(transaction.Request), schema, send, fuzz.Options{}) {
					if outcome.Finding != nil {
						findings = append(findings, *outcome.Finding)
					}
				}
			}
		}
		// Parameters.
		for _, parameter := range transaction.Request.Parameters {
			if strings.TrimSpace(parameter.Schema) == "" {
				continue
			}
			var schema map[string]any
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
			subject := fuzz.Subject{In: parameter.In, Name: parameter.Name, Style: parameter.Style}
			for _, outcome := range fuzz.Cover(context.Background(), subject, "", schema, send, fuzz.Options{}) {
				if outcome.Finding != nil {
					findings = append(findings, *outcome.Finding)
				}
			}
		}
	}
	return findings
}

func mediaOf(request compile.Request) string {
	for _, header := range request.Headers {
		if strings.EqualFold(header.Name, "Content-Type") {
			return strings.ToLower(strings.TrimSpace(strings.SplitN(header.Value, ";", 2)[0]))
		}
	}
	return ""
}

func describeCoverage(findings []fuzz.CoverageFinding) string {
	var out []string
	for _, finding := range findings {
		out = append(out, finding.Subject.Describe()+" at "+finding.Probe.Why+": "+finding.Message)
	}
	return strings.Join(out, "\n  ")
}

// TestCoverageFindsNothingAgainstAConformingServer is coverage's soundness
// baseline, and it is stricter than fuzz's: every probe is sent, none is
// skipped as a repeat, and each one names the exact bound it sits on — so a
// false finding here points at the enumerator's arithmetic for that bound.
func TestCoverageFindsNothingAgainstAConformingServer(t *testing.T) {
	for _, name := range corpus.Names() {
		t.Run(name, func(t *testing.T) {
			if findings := cover(t, name); len(findings) > 0 {
				t.Errorf("%d false finding(s) against a conforming server:\n  %s",
					len(findings), describeCoverage(findings))
			}
		})
	}
}

// TestCoverageFindsTheFaultsARunCannot: the same three faults generation is
// for, found deterministically — the same probes every run, so this is a
// gate a pipeline can hold, not a lottery it might win.
func TestCoverageFindsTheFaultsARunCannot(t *testing.T) {
	for _, fault := range []corpus.Fault{
		corpus.FaultAcceptsAnyParameter,
		corpus.FaultAcceptsAnyBody,
		corpus.FaultCrashesOnBadInput,
	} {
		t.Run(string(fault), func(t *testing.T) {
			var found []string
			for _, name := range corpus.Names() {
				if len(cover(t, name, fault)) > 0 {
					found = append(found, name)
				}
			}
			if len(found) == 0 {
				t.Errorf("%s was committed against every description and coverage found nothing", fault)
			}
			t.Logf("found in %d of %d description(s): %s", len(found), len(corpus.Names()), strings.Join(found, ", "))
		})
	}
}
