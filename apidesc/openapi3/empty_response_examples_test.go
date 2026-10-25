package openapi3_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/antimatter-studios/vertrag/runner"
)

// Exercise the parser, compiler and real HTTP runner together: empty bytes
// explicitly promised by the author differ from an unspecified response body.
func TestResponseBodyPresenceSurvivesCompilationAndHTTP(t *testing.T) {
	for _, media := range []string{"text/plain", "application/xml", "application/mail+xml"} {
		for _, form := range []string{"example", "value", "dataValue", "absent"} {
			for _, body := range []string{"", "héllo\r\n"} {
				if form == "absent" && body != "" {
					continue
				}
				for _, wrong := range []bool{false, true} {
					name := media + "/" + form + "/" + body
					if wrong {
						name += "/unexpected bytes"
					}
					t.Run(name, func(t *testing.T) {
						response := map[string]interface{}{}
						if form == "example" {
							response[form] = body
						} else if form != "absent" {
							response["examples"] = map[string]interface{}{"literal": map[string]interface{}{form: body}}
						}
						txs := compileXMLExamples(t, media, nil, response)
						if len(txs) != 1 {
							t.Fatalf("got %d transactions, want one", len(txs))
						}
						if txs[0].Response.BodyPresent != (form != "absent") {
							t.Fatalf("body presence %v for %s", txs[0].Response.BodyPresent, form)
						}
						actual := body
						if wrong {
							actual = "<unexpected/>"
						}
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							w.Header().Set("Content-Type", media)
							_, _ = io.WriteString(w, actual)
						}))
						defer server.Close()
						results, err := runner.New(server.URL).Run(t.Context(), txs)
						if err != nil || len(results) != 1 {
							t.Fatalf("runner results %v, error %v", results, err)
						}
						want := runner.StatusPass
						if wrong && form != "absent" {
							want = runner.StatusFail
						}
						if results[0].Status != want {
							t.Fatalf("status %s, want %s: %v", results[0].Status, want, results[0].Errors)
						}
					})
				}
			}
		}
	}
}

func TestJSONResponseSchemasStillJudgeStructureInsteadOfExampleBytes(t *testing.T) {
	for _, example := range []bool{false, true} {
		for _, valid := range []bool{false, true} {
			response := map[string]interface{}{"schema": map[string]interface{}{
				"type": "object", "required": []string{"id"},
				"properties": map[string]interface{}{"id": map[string]string{"type": "integer"}},
			}}
			if example {
				response["example"] = map[string]int{"id": 1}
			}
			txs := compileXMLExamples(t, "application/json", nil, response)
			if len(txs) != 1 || txs[0].Response.Schema == "" {
				t.Fatal("JSON schema lost")
			}
			actual := `{"id":2}`
			if !valid {
				actual = `{"id":"wrong"}`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, actual)
			}))
			results, err := runner.New(server.URL).Run(t.Context(), txs)
			server.Close()
			if err != nil || len(results) != 1 {
				t.Fatalf("results %v, error %v", results, err)
			}
			want := runner.StatusPass
			if !valid {
				want = runner.StatusFail
			}
			if results[0].Status != want {
				t.Fatalf("example %v, valid %v: status %s, want %s: %v", example, valid, results[0].Status, want, results[0].Errors)
			}
		}
	}
}

// A text response with a string schema and no example has a body generated
// from the schema to fill the transaction, and that generated empty string is
// not an expectation: an HTML page, a metrics exposition or an MTA-STS policy
// answered there passes. Only a body the description writes out is enforced
// when empty (#90).
func TestAGeneratedEmptyBodyIsNotAnExpectation(t *testing.T) {
	for _, media := range []string{"text/plain", "text/html"} {
		t.Run(media, func(t *testing.T) {
			txs := compileXMLExamples(t, media, nil, map[string]interface{}{
				"schema": map[string]string{"type": "string"},
			})
			if len(txs) != 1 {
				t.Fatalf("got %d transactions, want one", len(txs))
			}
			if txs[0].Response.BodyPresent {
				t.Fatal("a body generated from the schema was taken as an explicit empty expectation")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", media)
				_, _ = io.WriteString(w, "<!DOCTYPE html><title>docs</title>\nversion: STSv1\n")
			}))
			defer server.Close()
			results, err := runner.New(server.URL).Run(t.Context(), txs)
			if err != nil || len(results) != 1 {
				t.Fatalf("runner results %v, error %v", results, err)
			}
			if results[0].Status != runner.StatusPass {
				t.Fatalf("status %s, want pass: %v", results[0].Status, results[0].Errors)
			}
		})
	}
}
