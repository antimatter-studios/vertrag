package runner

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
)

func operation(id string) compile.Transaction {
	return compile.Transaction{
		Name:        id,
		OperationID: id,
		Request:     compile.Request{Method: "GET", URI: "/" + id},
		Response:    compile.Response{Status: "204"},
	}
}

func counting(hits *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
}

// A routed operation reaches its own server and nothing else does, and it is a
// result of the run like any other.
func TestARoutedOperationGoesToItsOwnServerOnly(t *testing.T) {
	var main, own atomic.Int32
	mainServer := httptest.NewServer(counting(&main))
	defer mainServer.Close()
	ownServer := httptest.NewServer(counting(&own))
	defer ownServer.Close()

	engine := New(mainServer.URL)
	if err := engine.AddRoute(ownServer.URL+"/", Transport{}, []string{"wipe"}); err != nil {
		t.Fatal(err)
	}
	results, err := engine.Run(context.Background(), []compile.Transaction{operation("read"), operation("wipe")})
	if err != nil {
		t.Fatal(err)
	}
	if main.Load() != 1 || own.Load() != 1 {
		t.Fatalf("main saw %d and own saw %d, want one each", main.Load(), own.Load())
	}
	for _, result := range results {
		if result.Status != StatusPass {
			t.Errorf("%s: %v: %v", result.Name, result.Status, result.Errors)
		}
		if result.Name == "wipe" && !strings.HasPrefix(result.Request.URL, ownServer.URL+"/wipe") {
			t.Errorf("reported as sent to %q", result.Request.URL)
		}
	}
}

// The certificate is the route's: the server that demands one gets it, and the
// run's other requests go out with none.
func TestARouteUsesItsOwnClientCertificate(t *testing.T) {
	dir := t.TempDir()
	ca := newAuthority(t)
	cert, key, _ := ca.issue(t, dir)

	var internal atomic.Int32
	server := httptest.NewUnstartedServer(counting(&internal))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: ca.pool()}
	server.StartTLS()
	defer server.Close()
	var public atomic.Int32
	publicServer := httptest.NewServer(counting(&public))
	defer publicServer.Close()

	// Without the certificate the handshake is refused: the test proves the
	// certificate, not merely the address.
	refused := New(publicServer.URL)
	if err := refused.AddRoute(server.URL, Transport{Insecure: true}, []string{"deliver"}); err != nil {
		t.Fatal(err)
	}
	if _, err := refused.Send(context.Background(), operation("deliver")); err == nil {
		t.Fatal("a server requiring a certificate answered a client with none")
	}

	engine := New(publicServer.URL)
	transport := Transport{Insecure: true, ClientCert: cert, ClientCertKey: key}
	if err := engine.AddRoute(server.URL, transport, []string{"deliver"}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Send(context.Background(), operation("deliver")); err != nil {
		t.Fatalf("with the certificate: %v", err)
	}
	if _, err := engine.Send(context.Background(), operation("read")); err != nil {
		t.Fatal(err)
	}
	if internal.Load() != 1 || public.Load() != 1 {
		t.Fatalf("internal saw %d and public saw %d, want one each", internal.Load(), public.Load())
	}
}

func TestABadRouteCertificateFailsBeforeAnyRequest(t *testing.T) {
	engine := New("http://localhost:1")
	err := engine.AddRoute("https://localhost:2", Transport{ClientCert: "/nonexistent.pem"}, []string{"x"})
	if err == nil {
		t.Fatal("an unreadable certificate was accepted")
	}
}
