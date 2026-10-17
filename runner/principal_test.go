package runner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
)

func TestSendAsRemovesCredentialsFromEveryHeaderSource(t *testing.T) {
	headers := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	engine := New(server.URL)
	engine.Auth = Credential{Header: "Authorization: Bearer run-admin"}
	engine.ExtraHeaders = []string{"authorization: Bearer extra-admin", "X-Key: extra-key", "Cookie: session=run-admin; preference=dark"}
	engine.ConditionalHeaders = []ConditionalHeader{{Name: "X-Mock", Value: "success", Status: "200"}}
	source := compile.Transaction{
		Name: "protected operation",
		Request: compile.Request{Method: "GET", URI: "/protected", Headers: []compile.Header{
			{Name: "Authorization", Value: "Bearer example-admin"},
			{Name: "Cookie", Value: "session=example-admin; locale=en"},
		}},
		Response: compile.Response{Status: "200"},
	}
	carriers := Carriers{Headers: []string{"Authorization", "X-Key"}, Cookies: []string{"session"}}
	for _, credential := range []Credential{{}, {Header: "Authorization: Bearer reader"}, {Header: "Cookie: session=reader"}} {
		result, err := engine.SendAs(context.Background(), "reader", source, credential, carriers)
		if err != nil {
			t.Fatal(err)
		}
		received := <-headers
		if received.Get("X-Key") != "" || received.Get("X-Mock") != "" {
			t.Errorf("run credentials or a status-conditional mock header leaked: %v", received)
		}
		if !strings.Contains(received.Get("Cookie"), "preference=dark") || !strings.Contains(received.Get("Cookie"), "locale=en") {
			t.Errorf("non-credential cookies were lost: %v", received)
		}
		if strings.Contains(received.Get("Cookie"), "admin") || strings.Contains(received.Get("Authorization"), "admin") {
			t.Errorf("admin credential leaked: %v", received)
		}
		if credential.Header == "Authorization: Bearer reader" && received.Get("Authorization") != "Bearer reader" {
			t.Errorf("reader token lost: %v", received)
		}
		if credential.Header == "Cookie: session=reader" && !strings.Contains(received.Get("Cookie"), "session=reader") {
			t.Errorf("reader cookie lost: %v", received)
		}
		if result.Status != "" || result.Name != "reader" || result.Actual.StatusCode != "200" {
			t.Errorf("SendAs judged the response instead of returning it: %+v", result)
		}
	}
}

func TestSendAsRefusesAHookThatChangesThePrincipal(t *testing.T) {
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called.Store(true) }))
	defer server.Close()
	engine := New(server.URL)
	engine.Hooks = &stubHooks{beforeFn: func(transaction *Transaction) {
		transaction.Request.Headers["Authorization"] = "Bearer hook-admin"
	}}
	_, err := engine.SendAs(context.Background(), "anonymous", compile.Transaction{
		Name: "protected", Request: compile.Request{Method: "GET", URI: "/protected"},
	}, Credential{}, Carriers{Headers: []string{"Authorization"}})
	if !errors.Is(err, ErrCredentialChangedByHook) || called.Load() {
		t.Fatalf("SendAs error = %v, sent = %v; credential changes must stop before sending", err, called.Load())
	}
}

func TestSendAsHonorsThePrincipalsAuthExclusions(t *testing.T) {
	headers := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	engine := New(server.URL)
	source := compile.Transaction{Name: "login", Request: compile.Request{Method: "POST", URI: "/login"}}
	for _, credential := range []Credential{
		{Header: "Authorization: Bearer reader", Except: map[string]bool{"login": true}},
		{Header: "Authorization: Bearer reader", LoginMethod: "POST", LoginPath: "/login"},
	} {
		if _, err := engine.SendAs(context.Background(), "reader", source, credential,
			Carriers{Headers: []string{"Authorization"}}); err != nil {
			t.Fatal(err)
		}
		if received := <-headers; received != "" {
			t.Errorf("excluded transaction received %q", received)
		}
	}
}

func TestIgnoredAuthDoesNotProbeDeclaredPublicOperations(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	engine := New(server.URL)
	engine.Auth = Credential{Header: "Authorization: Bearer reader"}
	engine.Checks.IgnoredAuth = true
	results, err := engine.Run(context.Background(), []compile.Transaction{{
		Name: "public", Security: compile.Security{Declared: true, Public: true},
		Request: compile.Request{Method: "GET", URI: "/public"}, Response: compile.Response{Status: "200"},
	}})
	if err != nil || len(results) != 1 || results[0].Status != StatusPass || requests.Load() != 1 {
		t.Fatalf("public operation: results = %+v, requests = %d, error = %v", results, requests.Load(), err)
	}
}
