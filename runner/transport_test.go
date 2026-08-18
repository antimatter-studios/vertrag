package runner

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

// TestAClientCertificateIsPresentedWhenTheServerAsksForOne is the whole point
// of mutual TLS: the server decides who may speak to it at the handshake, so a
// client with no certificate never reaches a single handler. Without the
// certificate the run reports every request as a network failure — which is
// true and useless — and with it the API is testable at all.
//
// The server here really does require one: RequireAndVerifyClientCert against
// a CA that signed exactly one certificate, so the passing half of this test
// cannot pass by accident.
func TestAClientCertificateIsPresentedWhenTheServerAsksForOne(t *testing.T) {
	ca := newAuthority(t)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	server.TLS = &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  ca.pool(),
		MinVersion: tls.VersionTLS12,
	}
	server.StartTLS()
	defer server.Close()

	// The server's own certificate is self-signed, so it is its own CA; trusting
	// it here keeps the test about the CLIENT certificate rather than about
	// --insecure.
	serverCA := filepath.Join(t.TempDir(), "server-ca.pem")
	if err := writePEM(serverCA, server.Certificate().Raw); err != nil {
		t.Fatal(err)
	}

	anonymous, err := NewWithTransport(server.URL, Transport{CACert: serverCA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := anonymous.Send(context.Background(), transactionTo(server.URL)); err == nil {
		t.Fatal("the server requires a client certificate, so a client without one must not get through")
	}

	certPath, keyPath, _ := ca.issue(t, t.TempDir())
	engine, err := NewWithTransport(server.URL, Transport{
		CACert:        serverCA,
		ClientCert:    certPath,
		ClientCertKey: keyPath,
	})
	if err != nil {
		t.Fatalf("building the transport: %v", err)
	}
	reply, err := engine.Send(context.Background(), transactionTo(server.URL))
	if err != nil {
		t.Fatalf("the client certificate was not presented: %v", err)
	}
	if reply.StatusCode != "200" {
		t.Errorf("status = %s, want 200", reply.StatusCode)
	}
}

// TestOnePEMFileMayHoldTheCertificateAndItsKey: that is how openssl and every
// tool around it hand a pair over, so --cert-key is optional rather than
// something a user has to split a working file to satisfy.
func TestOnePEMFileMayHoldTheCertificateAndItsKey(t *testing.T) {
	ca := newAuthority(t)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	server.TLS = &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  ca.pool(),
		MinVersion: tls.VersionTLS12,
	}
	server.StartTLS()
	defer server.Close()

	serverCA := filepath.Join(t.TempDir(), "server-ca.pem")
	if err := writePEM(serverCA, server.Certificate().Raw); err != nil {
		t.Fatal(err)
	}

	_, _, bothPath := ca.issue(t, t.TempDir())
	engine, err := NewWithTransport(server.URL, Transport{CACert: serverCA, ClientCert: bothPath})
	if err != nil {
		t.Fatalf("building the transport: %v", err)
	}
	reply, err := engine.Send(context.Background(), transactionTo(server.URL))
	if err != nil {
		t.Fatalf("a combined PEM file was not accepted: %v", err)
	}
	if reply.StatusCode != "200" {
		t.Errorf("status = %s, want 200", reply.StatusCode)
	}
}

// TestABadClientCertificateFailsBeforeAnyRequest: a certificate that cannot be
// loaded is a mistake in the invocation, and it must be reported as one. Left
// to the handshake it arrives as a connection error on every transaction, which
// reads as an API that is down.
func TestABadClientCertificateFailsBeforeAnyRequest(t *testing.T) {
	ca := newAuthority(t)
	certPath, keyPath, _ := ca.issue(t, t.TempDir())
	_, otherKey, _ := ca.issue(t, t.TempDir())

	if _, err := NewWithTransport("http://x", Transport{ClientCert: "/nonexistent.pem"}); err == nil {
		t.Error("an unreadable client certificate should fail construction")
	}
	if _, err := NewWithTransport("http://x", Transport{ClientCert: certPath, ClientCertKey: "/nonexistent.key"}); err == nil {
		t.Error("an unreadable client key should fail construction")
	}
	if _, err := NewWithTransport("http://x", Transport{ClientCert: certPath, ClientCertKey: otherKey}); err == nil {
		t.Error("a key belonging to another certificate should fail construction")
	}
	garbage := filepath.Join(t.TempDir(), "garbage.pem")
	writeFile(garbage, "not a certificate\n")
	if _, err := NewWithTransport("http://x", Transport{ClientCert: garbage, ClientCertKey: keyPath}); err == nil {
		t.Error("a certificate file holding no PEM should fail construction")
	}

	// A key on its own authenticates nobody: there is nothing to present it
	// with, so the run would go out anonymous while its operator believed
	// otherwise.
	_, err := NewWithTransport("http://x", Transport{ClientCertKey: keyPath})
	if err == nil {
		t.Fatal("a client key with no certificate should fail construction")
	}
	if !strings.Contains(err.Error(), keyPath) {
		t.Errorf("the error does not name the key it cannot use: %v", err)
	}
}

// TestDelayThrottlesTheStreamNotEachWorker pins the semantics --delay has to
// have once requests overlap.
//
// A pause each worker takes on its own throttles nothing: four workers
// pausing in parallel still send four requests per interval. What someone
// throttling a shared server is asking for is a bound on the whole run's
// request stream, so the wait is computed against the last send by ANY
// worker. (The naive per-worker form was also a data race, which is how this
// came to light.)
func TestDelayThrottlesTheStreamNotEachWorker(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	const delay = 25 * time.Millisecond
	engine, err := NewWithTransport(server.URL, Transport{Delay: delay})
	if err != nil {
		t.Fatal(err)
	}
	engine.Workers = 4

	var transactions []compile.Transaction
	for i := 0; i < 6; i++ {
		transactions = append(transactions, compile.Transaction{
			Name:     fmt.Sprintf("t%d", i),
			Request:  compile.Request{Method: "GET", URI: "/x"},
			Response: compile.Response{Status: "200"},
		})
	}
	if _, err := engine.Run(context.Background(), transactions); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	seen := append([]time.Time{}, times...)
	mu.Unlock()

	if len(seen) != 6 {
		t.Fatalf("server saw %d requests, want 6", len(seen))
	}
	sort.Slice(seen, func(i, j int) bool { return seen[i].Before(seen[j]) })

	// Six requests, five gaps of at least the delay: the whole run cannot be
	// shorter than 5×delay however many workers asked to send at once.
	span := seen[len(seen)-1].Sub(seen[0])
	if floor := 4 * delay; span < floor {
		t.Errorf("six requests spanned %s; a %s stream delay makes that at least %s — workers paced independently",
			span, delay, floor)
	}
}

// failsOnce answers the second attempt and drops the first, standing in for a
// connection that is reset once. A real server cannot be made to do this
// reliably — net/http retries some connection errors itself, and which ones
// depends on whether the connection was reused — so the failure is injected
// here instead of raced for.
type failsOnce struct{ calls int }

func (f *failsOnce) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls++
	if f.calls == 1 {
		return nil, errors.New("read: connection reset by peer")
	}
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
	}, nil
}

// TestARetriedRequestIsNotTimedForItsBackoff is the other half of the pacing
// case in checks_test.go, and it fails the same way: the backoff between
// attempts is a pause vertrag decided to take, so charging it to the server
// would report a slow API where there was a flaky link and a tool being
// patient about it.
func TestARetriedRequestIsNotTimedForItsBackoff(t *testing.T) {
	engine, err := NewWithTransport("http://example.invalid", Transport{Retries: 1})
	if err != nil {
		t.Fatalf("NewWithTransport: %v", err)
	}
	stub := &failsOnce{}
	engine.Client = &http.Client{Transport: stub}

	request, err := http.NewRequestWithContext(context.Background(),
		"GET", "http://example.invalid/", nil)
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	response, exchange, err := engine.do(request)
	if err != nil {
		t.Fatalf("do after one retry: %v", err)
	}
	defer response.Body.Close()

	// The backoff really happened, or the measurement below is being taken
	// over a run that never waited and proves nothing.
	if waited := time.Since(started); waited < 250*time.Millisecond {
		t.Fatalf("the whole call took %s, so the 250ms backoff was not waited", waited)
	}
	if stub.calls != 2 {
		t.Fatalf("the stub saw %d attempts, want 2", stub.calls)
	}
	if exchange > 50*time.Millisecond {
		t.Errorf("the answering attempt was timed at %s; the backoff is in the measurement", exchange)
	}
}
