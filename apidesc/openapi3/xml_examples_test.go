package openapi3_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc/openapi3"
	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/runner"
)

var xmlExampleBodies = map[string]string{
	"valid":     "<?xml version=\"1.0\"?><request>héllo &amp; goodbye</request>\r\n",
	"malformed": "<request>",
	"empty":     "",
	"oversized": strings.Repeat("x", 65537),
}

func compileXMLExamples(t *testing.T, media string, requestMedia map[string]interface{}, responseMedia map[string]interface{}) []compile.Transaction {
	t.Helper()
	doc := map[string]interface{}{
		"openapi": "3.1.0", "info": map[string]string{"title": "XML examples", "version": "1"},
		"paths": map[string]interface{}{"/xml": map[string]interface{}{"post": map[string]interface{}{
			"operationId": "xmlExamples",
			"requestBody": map[string]interface{}{"content": map[string]interface{}{media: requestMedia}},
			"responses":   map[string]interface{}{"200": map[string]interface{}{"description": "literal response", "content": map[string]interface{}{media: responseMedia}}},
		}}},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := openapi3.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return compile.Compile("application/vnd.oai.openapi", parsed, "xml.json").Transactions
}

func namedXMLMedia() map[string]interface{} {
	examples := map[string]interface{}{}
	for name, body := range xmlExampleBodies {
		examples[name] = map[string]interface{}{"value": body}
	}
	return map[string]interface{}{"schema": map[string]string{"type": "string"}, "examples": examples}
}

func TestExplicitXMLExamplesReachTheRunnerAsLiteralBytes(t *testing.T) {
	for _, media := range []string{"application/xml", "application/mail+xml", "text/xml; charset=utf-8", "IMAGE/SVG+XML"} {
		t.Run(media, func(t *testing.T) {
			transactions := compileXMLExamples(t, media, namedXMLMedia(), namedXMLMedia())
			if len(transactions) != len(xmlExampleBodies) {
				t.Fatalf("got %d transactions, want %d", len(transactions), len(xmlExampleBodies))
			}
			seen := map[string]bool{}
			for _, tx := range transactions {
				if tx.Request.Body != tx.Response.Body {
					t.Fatal("named XML request/response pairing was lost")
				}
				seen[tx.Request.Body] = true
				if tx.Response.Schema != "" {
					t.Fatal("XML bytes must not be validated as a JSON document")
				}
			}
			for name, body := range xmlExampleBodies {
				if !seen[body] {
					t.Errorf("%s example bytes were discarded", name)
				}
			}
			if t.Failed() {
				return
			}
			for _, wrong := range []bool{false, true} {
				t.Run(map[bool]string{false: "exact response", true: "wrong response fails"}[wrong], func(t *testing.T) {
					received := map[string]int{}
					var mu sync.Mutex
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
						}
						mu.Lock()
						received[string(body)]++
						mu.Unlock()
						if r.Method != http.MethodPost || r.Header.Get("Content-Type") != media {
							t.Errorf("wrong request method or media type: %s %q", r.Method, r.Header.Get("Content-Type"))
						}
						w.Header().Set("Content-Type", media)
						if wrong {
							_, _ = io.WriteString(w, "<wrong/>")
						} else {
							_, _ = w.Write(body)
						}
					}))
					defer server.Close()
					results, err := runner.New(server.URL).Run(context.Background(), transactions)
					if err != nil {
						t.Fatal(err)
					}
					if len(results) != len(xmlExampleBodies) {
						t.Fatal("not every XML transaction ran")
					}
					for i, result := range results {
						want := runner.StatusPass
						if wrong {
							want = runner.StatusFail
						}
						if result.Status != want {
							t.Errorf("transaction %d: status %s, want %s: %v", i, result.Status, want, result.Errors)
						}
					}
					mu.Lock()
					defer mu.Unlock()
					for name, body := range xmlExampleBodies {
						if received[body] != 1 {
							t.Errorf("%s reached server %d times, want one", name, received[body])
						}
					}
				})
			}
		})
	}
}

func TestSingleXMLExampleIsLiteral(t *testing.T) {
	for _, key := range []string{"example", "examples"} {
		t.Run(key, func(t *testing.T) {
			media := map[string]interface{}{key: xmlExampleBodies["valid"]}
			if key == "examples" {
				media[key] = map[string]interface{}{"valid": map[string]interface{}{"dataValue": xmlExampleBodies["valid"]}}
			}
			tx := compileXMLExamples(t, "application/xml; charset=utf-8", media, media)
			if len(tx) != 1 || tx[0].Request.Body != xmlExampleBodies["valid"] || tx[0].Response.Body != xmlExampleBodies["valid"] {
				t.Fatal("single XML example bytes changed")
			}
		})
	}
}

func TestXMLSchemasAndObjectsDoNotInventXML(t *testing.T) {
	for _, media := range []map[string]interface{}{
		{"schema": map[string]string{"type": "string"}},
		{"schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"name": map[string]string{"type": "string"}}}},
		{"example": map[string]string{"name": "not serialized XML"}},
	} {
		tx := compileXMLExamples(t, "application/xml", media, media)
		if len(tx) != 1 || tx[0].Request.Body != "" || tx[0].Response.Body != "" {
			t.Fatal("XML was invented from a schema or structured value")
		}
	}
}
