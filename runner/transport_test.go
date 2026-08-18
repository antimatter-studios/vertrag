package runner

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/compile"
)

func transactionTo(url string) compile.Transaction {
	return compile.Transaction{
		Name:     "t",
		Request:  compile.Request{Method: "GET", URI: "/"},
		Response: compile.Response{Status: "200"},
	}
}

// TestRetriesRecoverFromANetworkFailure: a connection that drops before a
// response is a network failure, and a retry is what turns a flaky link into a
// verdict about the API rather than about the network.
func TestRetriesRecoverFromANetworkFailure(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// Drop the connection without answering.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("server cannot hijack")
			}
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		w.WriteHeader(200)
	}))
	defer server.Close()

	engine, err := NewWithTransport(server.URL, Transport{Retries: 2})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := engine.Send(context.Background(), transactionTo(server.URL))
	if err != nil {
		t.Fatalf("Send after retries: %v", err)
	}
	if reply.StatusCode != "200" {
		t.Errorf("status = %s, want 200", reply.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("server saw %d calls, want 2 (one failure, one retry)", got)
	}
}

// TestResponsesAreNeverRetried is the rule that keeps retries honest: a 5xx is
// the finding the run exists to report, and retrying it until the server
// happens to answer 200 would hide exactly that.
func TestResponsesAreNeverRetried(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(500)
	}))
	defer server.Close()

	engine, _ := NewWithTransport(server.URL, Transport{Retries: 3})
	reply, err := engine.Send(context.Background(), transactionTo(server.URL))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if reply.StatusCode != "500" {
		t.Errorf("status = %s, want the 500 reported as-is", reply.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server saw %d calls, want exactly 1 — a response is never retried", got)
	}
}

// TestNoRetriesByDefault pins the default: zero retries, so a run without the
// flag behaves as every run before the flag existed.
func TestNoRetriesByDefault(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		hj := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	defer server.Close()

	engine := New(server.URL)
	if _, err := engine.Send(context.Background(), transactionTo(server.URL)); err == nil {
		t.Fatal("a dropped connection with no retries should error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server saw %d calls, want 1", got)
	}
}

// TestTimeoutBoundsARequest: a hung server must not hang the run.
func TestTimeoutBoundsARequest(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	engine, _ := NewWithTransport(server.URL, Transport{Timeout: 150 * time.Millisecond})
	start := time.Now()
	_, err := engine.Send(context.Background(), transactionTo(server.URL))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed > 2*time.Second {
		t.Errorf("timeout took %s; the 150ms bound was not honoured", elapsed)
	}
	var netErr net.Error
	if !strings.Contains(err.Error(), "Timeout") && !strings.Contains(err.Error(), "deadline") && !errorsAs(err, &netErr) {
		t.Errorf("error does not read as a timeout: %v", err)
	}
}

func errorsAs(err error, target *net.Error) bool {
	for err != nil {
		if ne, ok := err.(net.Error); ok {
			*target = ne
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// TestDelayPacesRequests: the pause applies between requests, not before the
// first, so a single request costs nothing and N requests cost N-1 delays.
func TestDelayPacesRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer server.Close()

	engine, _ := NewWithTransport(server.URL, Transport{Delay: 120 * time.Millisecond})
	tx := transactionTo(server.URL)

	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := engine.Send(context.Background(), tx); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)

	// Two gaps for three requests: at least 240ms, well under three gaps.
	if elapsed < 240*time.Millisecond {
		t.Errorf("three requests took %s; two 120ms delays were expected", elapsed)
	}
	if elapsed > 360*time.Millisecond+500*time.Millisecond {
		t.Errorf("three requests took %s; the delay applied too often or too long", elapsed)
	}
}

// TestInsecureTrustsASelfSignedServer: the whole reason the flag exists.
func TestInsecureTrustsASelfSignedServer(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer server.Close()

	strict := New(server.URL)
	if _, err := strict.Send(context.Background(), transactionTo(server.URL)); err == nil {
		t.Fatal("a self-signed certificate should be refused by default")
	}

	lax, _ := NewWithTransport(server.URL, Transport{Insecure: true})
	reply, err := lax.Send(context.Background(), transactionTo(server.URL))
	if err != nil {
		t.Fatalf("--insecure did not trust the server: %v", err)
	}
	if reply.StatusCode != "200" {
		t.Errorf("status = %s", reply.StatusCode)
	}
}

// TestCACertTrustsAPrivateAuthority: the httptest TLS server's certificate,
// handed over as a CA bundle, must be enough on its own — no --insecure.
func TestCACertTrustsAPrivateAuthority(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer server.Close()

	// httptest's cert is self-signed, so it is its own CA.
	pemPath := t.TempDir() + "/ca.pem"
	if err := writePEM(pemPath, server.Certificate().Raw); err != nil {
		t.Fatal(err)
	}

	engine, err := NewWithTransport(server.URL, Transport{CACert: pemPath})
	if err != nil {
		t.Fatalf("building the transport: %v", err)
	}
	if _, err := engine.Send(context.Background(), transactionTo(server.URL)); err != nil {
		t.Fatalf("the CA bundle did not establish trust: %v", err)
	}
}

// TestBadCACertFailsBeforeAnyRequest: a transport that cannot be built stops
// the run at construction, with the reason, rather than 401-ing every request.
func TestBadCACertFailsBeforeAnyRequest(t *testing.T) {
	if _, err := NewWithTransport("http://x", Transport{CACert: "/nonexistent.pem"}); err == nil {
		t.Error("an unreadable CA bundle should fail construction")
	}
	empty := t.TempDir() + "/empty.pem"
	writeFile(empty, "not a certificate\n")
	if _, err := NewWithTransport("http://x", Transport{CACert: empty}); err == nil {
		t.Error("a bundle with no certificates should fail construction")
	}
	if _, err := NewWithTransport("http://x", Transport{Proxy: "://bad"}); err == nil {
		t.Error("an unparsable proxy URL should fail construction")
	}
}
