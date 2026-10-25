package openapi3_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc/openapi3"
	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/runner"
)

func emptyQueryTransactions(t *testing.T, parameter map[string]interface{}, named bool) compile.Result {
	t.Helper()
	responses := map[string]interface{}{}
	singleState := "blank"
	if parameter["schema"].(map[string]interface{})["default"] == "café+box@example.com" {
		singleState = "known"
	}
	for _, row := range []struct{ name, status, state string }{{"blank", "400", "blank"}, {"known", "200", "known"}} {
		if !named && row.name != singleState {
			continue
		}
		media := map[string]interface{}{
			"schema":   map[string]interface{}{"type": "object", "required": []string{"state"}, "properties": map[string]interface{}{"state": map[string]interface{}{"type": "string", "enum": []string{row.state}}}},
			"examples": map[string]interface{}{row.name: map[string]interface{}{"value": map[string]string{"state": row.state}}},
		}
		responses[row.status] = map[string]interface{}{"description": row.state, "content": map[string]interface{}{"application/json": media}}
	}
	doc := map[string]interface{}{"openapi": "3.1.0", "info": map[string]string{"title": "query presence", "version": "1"}, "paths": map[string]interface{}{"/lookup": map[string]interface{}{"get": map[string]interface{}{"operationId": "lookup", "parameters": []interface{}{parameter}, "responses": responses}}}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := openapi3.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return compile.Compile("application/vnd.oai.openapi", parsed, "query.json")
}

func TestExplicitEmptyQueryExamplesReachTheHTTPServer(t *testing.T) {
	for _, form := range []string{"named value", "named dataValue", "single", "schema examples", "default", "nonempty default", "optional"} {
		t.Run(form, func(t *testing.T) {
			parameter := map[string]interface{}{"name": "address", "in": "query", "required": form != "optional", "schema": map[string]interface{}{"type": "string"}}
			named := strings.HasPrefix(form, "named")
			if named || form == "single" || form == "schema examples" || form == "optional" {
				parameter["schema"].(map[string]interface{})["default"] = "fallback@example.net"
			}
			if named {
				key := "value"
				if form == "named dataValue" {
					key = "dataValue"
				}
				parameter["examples"] = map[string]interface{}{"blank": map[string]interface{}{key: ""}, "known": map[string]interface{}{key: "café+box@example.com"}}
			} else if form == "schema examples" {
				parameter["schema"].(map[string]interface{})["examples"] = []string{""}
			} else if form == "default" {
				parameter["schema"].(map[string]interface{})["default"] = ""
			} else if form == "nonempty default" {
				parameter["schema"].(map[string]interface{})["default"] = "café+box@example.com"
			} else {
				parameter["example"] = ""
			}
			compiled := emptyQueryTransactions(t, parameter, named)
			for _, annotation := range compiled.Annotations {
				if annotation.Type == "error" {
					t.Fatalf("explicit empty example was rejected: %+v", annotation)
				}
			}
			want := 1
			if named {
				want = 2
			}
			if len(compiled.Transactions) != want {
				t.Fatalf("got %d transactions, want %d", len(compiled.Transactions), want)
			}
			for _, tx := range compiled.Transactions {
				parsed, err := url.Parse(tx.Request.URI)
				if err != nil {
					t.Fatal(err)
				}
				values, ok := parsed.Query()["address"]
				if !ok || len(values) != 1 {
					t.Fatalf("query omitted: %q", tx.Request.URI)
				}
				if tx.Response.Status == "400" && (values[0] != "" || !strings.Contains(tx.Request.URI, "address=")) {
					t.Fatalf("blank query changed: %q", tx.Request.URI)
				}
				if len(tx.Request.Parameters) != 1 || !tx.Request.Parameters[0].HasValue || tx.Request.Parameters[0].Named != named {
					t.Fatalf("query presence/name lost: %+v", tx.Request.Parameters)
				}
			}
			for _, wrong := range []bool{false, true} {
				var mu sync.Mutex
				seen := map[string]int{}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					values, ok := r.URL.Query()["address"]
					if !ok || len(values) != 1 {
						t.Errorf("HTTP query missing: %q", r.URL.RawQuery)
						w.WriteHeader(500)
						return
					}
					address := values[0]
					mu.Lock()
					seen[address]++
					mu.Unlock()
					state, status := "blank", 400
					if address != "" {
						state, status = "known", 200
						if address != "café+box@example.com" {
							t.Errorf("nonempty query changed: %q", address)
						}
					}
					if wrong {
						state = "wrong"
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"state":"`+state+`"}`)
				}))
				results, err := runner.New(server.URL).Run(t.Context(), compiled.Transactions)
				server.Close()
				if err != nil || len(results) != want {
					t.Fatalf("results %v, error %v", results, err)
				}
				for _, result := range results {
					status := runner.StatusPass
					if wrong {
						status = runner.StatusFail
					}
					if result.Status != status {
						t.Fatalf("result %s, want %s: %v", result.Status, status, result.Errors)
					}
				}
				mu.Lock()
				wantBlank := 1
				if form == "nonempty default" {
					wantBlank = 0
				}
				if seen[""] != wantBlank || ((named || form == "nonempty default") && seen["café+box@example.com"] != 1) {
					t.Errorf("requests lost/duplicated: %v", seen)
				}
				mu.Unlock()
			}
		})
	}
}
