package openapi3_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc/openapi3"
	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/runner"
)

func compileHeaderDocument(t *testing.T, operation map[string]any) compile.Result {
	t.Helper()
	source, err := json.Marshal(map[string]any{
		"openapi": "3.1.0", "info": map[string]string{"title": "Headers", "version": "1"},
		"paths": map[string]any{"/attachment": map[string]any{"get": operation}},
		"components": map[string]any{"headers": map[string]any{
			"Required": map[string]any{"required": true, "schema": map[string]string{"type": "integer"}},
			"Optional": map[string]any{"schema": map[string]string{"type": "integer"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := openapi3.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	compiled := compile.Compile("application/vnd.oai.openapi", parsed, "headers.json")
	for _, annotation := range compiled.Annotations {
		if annotation.Type == "error" {
			t.Fatalf("compile: %+v", annotation)
		}
	}
	if len(compiled.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1", len(compiled.Transactions))
	}
	return compiled
}

func TestOptionalResponseHeadersAreValidatedOnlyWhenPresent(t *testing.T) {
	compiled := compileHeaderDocument(t, map[string]any{"responses": map[string]any{
		"200": map[string]any{
			"description": "OK",
			"headers": map[string]any{
				"X-Required": map[string]string{"$ref": "#/components/headers/Required"},
				"X-Optional": map[string]string{"$ref": "#/components/headers/Optional"},
				"ETag":       map[string]any{"required": false, "schema": map[string]string{"type": "string"}},
				// A Header Object named Content-Type must be ignored even when required.
				"cOnTeNt-TyPe": map[string]any{"required": true, "schema": map[string]string{"type": "integer"}},
			},
		},
	}})
	tx := compiled.Transactions[0]
	if len(tx.Response.Headers) != 1 || tx.Response.Headers[0].Name != "X-Required" {
		t.Fatalf("presence expectations = %+v", tx.Response.Headers)
	}
	if len(tx.Response.HeaderSchemas) != 3 {
		t.Fatalf("schemas = %+v, want all three non-Content-Type headers", tx.Response.HeaderSchemas)
	}
	for _, test := range []struct {
		name, required, optional string
		want                     runner.Status
	}{
		{"optional absent", "42", "", runner.StatusPass},
		{"optional valid", "42", "7", runner.StatusPass},
		{"optional invalid", "42", "banana", runner.StatusFail},
		{"required absent", "", "7", runner.StatusFail},
		{"required invalid", "banana", "7", runner.StatusFail},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.required != "" {
					w.Header().Set("X-Required", test.required)
				}
				if test.optional != "" {
					w.Header().Set("X-Optional", test.optional)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			engine := runner.New(server.URL)
			engine.Checks = runner.Checks{HeaderSchema: true}
			results, err := engine.Run(t.Context(), compiled.Transactions)
			if err != nil || len(results) != 1 {
				t.Fatalf("results = %+v, error = %v", results, err)
			}
			if results[0].Status != test.want {
				t.Fatalf("result = %+v, want %s", results[0], test.want)
			}
		})
	}
}

func TestOptionalRequestHeadersDoNotInventEmptyValues(t *testing.T) {
	for _, test := range []struct {
		name      string
		parameter map[string]any
		present   bool
		value     string
	}{
		{"absent", map[string]any{}, false, ""},
		{"explicit empty", map[string]any{"example": ""}, true, ""},
		{"example", map[string]any{"example": "bytes=0-2"}, true, "bytes=0-2"},
		{"schema example", map[string]any{"schema": map[string]any{"type": "string", "example": "bytes=0-2"}}, true, "bytes=0-2"},
		{"default", map[string]any{"schema": map[string]any{"type": "string", "default": "bytes=0-2"}}, true, "bytes=0-2"},
		{"empty default", map[string]any{"schema": map[string]any{"type": "string", "default": ""}}, true, ""},
		{"example overrides default", map[string]any{"example": "bytes=3-5", "schema": map[string]any{"type": "string", "default": "bytes=0-2"}}, true, "bytes=3-5"},
		{"required", map[string]any{"required": true}, true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			param := test.parameter
			param["name"], param["in"] = "Range", "header"
			if param["schema"] == nil {
				param["schema"] = map[string]string{"type": "string"}
			}
			compiled := compileHeaderDocument(t, map[string]any{
				"parameters": []any{param},
				"responses":  map[string]any{"204": map[string]string{"description": "OK"}},
			})
			tx := compiled.Transactions[0]
			if len(tx.Request.Parameters) != 1 || tx.Request.Parameters[0].Name != "Range" || tx.Request.Parameters[0].Schema == "" {
				t.Fatalf("parameter schema lost: %+v", tx.Request.Parameters)
			}
			if !test.present && (len(tx.Request.Headers) != 0 || tx.Request.Parameters[0].HasValue) {
				t.Fatalf("optional absent header was sent: %+v", tx.Request)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, present := r.Header["Range"]
				if present != test.present || r.Header.Get("Range") != test.value {
					t.Errorf("Range = %q, present = %v, want %q / %v", r.Header.Get("Range"), present, test.value, test.present)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			results, err := runner.New(server.URL).Run(t.Context(), compiled.Transactions)
			if err != nil || len(results) != 1 || results[0].Status != runner.StatusPass {
				t.Fatalf("results = %+v, error = %v", results, err)
			}
			if !test.present {
				generated, err := tx.Request.SetParameter(tx.Request.Parameters[0], "bytes=0-2")
				if err != nil || len(generated.Headers) != 1 || generated.Headers[0].Value != "bytes=0-2" {
					t.Fatalf("generation lost omitted header: %+v / %v", generated, err)
				}
			}
		})
	}
}
