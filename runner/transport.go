package runner

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Transport is how requests reach the server: the knobs a CI job turns when
// the server under test is slow, self-signed, behind a proxy, or shared.
//
// Zero values mean "vertrag's defaults", so a caller that has no opinion can
// pass Transport{} and get the client New always built.
type Transport struct {
	// Timeout bounds one request from first byte sent to last byte read. Zero
	// means the default of 30 s. A hung server otherwise hangs the run.
	Timeout time.Duration

	// Retries is how many more times a request is attempted after a NETWORK
	// failure — connection refused, reset, timeout. A response, whatever its
	// status, is never retried: a 5xx is a finding, and retrying it would hide
	// the very thing the run exists to report.
	Retries int

	// Delay is a pause before every request after the first, for servers that
	// throttle. Zero means no pause.
	Delay time.Duration

	// Insecure disables TLS certificate verification. For a test server with a
	// self-signed certificate; never for anything else.
	Insecure bool

	// CACert is a PEM bundle to trust in ADDITION to the system roots — for a
	// private CA the system does not know. Empty means system roots only.
	CACert string

	// ClientCert is a PEM certificate to present when the server asks the
	// client to prove who it is — mutual TLS, which an internal API often
	// requires of everything that reaches it. Without one such a server refuses
	// the handshake, so every transaction is a network failure and the run says
	// nothing about the contract.
	ClientCert string

	// ClientCertKey is the private key for ClientCert. Empty means the key is
	// in the certificate file itself, which is how openssl and the tools around
	// it hand a pair over — so a working file does not have to be split in two
	// to satisfy this.
	ClientCertKey string

	// Proxy is an HTTP(S) proxy URL. Empty means the environment's
	// HTTP_PROXY/HTTPS_PROXY/NO_PROXY, which Go's default transport honours.
	Proxy string
}

// client builds the http.Client the runner sends through.
func (t Transport) client() (*http.Client, error) {
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	base := http.DefaultTransport.(*http.Transport).Clone()

	// A key with no certificate to present it with authenticates nobody, and the
	// handshake would go out anonymous while whoever passed it believed
	// otherwise. Said here rather than left to the server's refusal.
	if t.ClientCert == "" && t.ClientCertKey != "" {
		return nil, fmt.Errorf("%s is a client key with no certificate to go with it", t.ClientCertKey)
	}

	if t.Insecure || t.CACert != "" || t.ClientCert != "" {
		tlsConfig := &tls.Config{InsecureSkipVerify: t.Insecure} //nolint:gosec // opt-in by flag
		if t.CACert != "" {
			pem, err := os.ReadFile(t.CACert)
			if err != nil {
				return nil, fmt.Errorf("reading the CA bundle: %w", err)
			}
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("%s holds no PEM certificates", t.CACert)
			}
			tlsConfig.RootCAs = pool
		}
		if t.ClientCert != "" {
			key := t.ClientCertKey
			if key == "" {
				key = t.ClientCert
			}
			// A certificate the client cannot load is a mistake in the
			// invocation, and this is the last moment it can be reported as one.
			// Left to the handshake it arrives as a connection error on every
			// transaction, which reads as an API that is down.
			pair, err := tls.LoadX509KeyPair(t.ClientCert, key)
			if err != nil {
				return nil, fmt.Errorf("reading the client certificate: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{pair}
		}
		base.TLSClientConfig = tlsConfig
	}

	if t.Proxy != "" {
		proxyURL, err := url.Parse(t.Proxy)
		if err != nil {
			return nil, fmt.Errorf("proxy: %w", err)
		}
		base.Proxy = http.ProxyURL(proxyURL)
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: base,
		// Redirects are not followed. A description promising a 301 is
		// describing the redirect itself; following it would test the
		// destination instead and report the wrong status.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// do sends one request, retrying network failures the number of times the
// transport allows, with a short backoff. It never retries a response.
func (r *Runner) do(request *http.Request) (*http.Response, error) {
	attempts := r.Transport.Retries + 1
	backoff := 250 * time.Millisecond

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(backoff):
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
			backoff *= 2
			// A body reader is spent after the first attempt; rewind it the
			// way net/http itself does when it can.
			if request.GetBody != nil {
				body, err := request.GetBody()
				if err != nil {
					return nil, err
				}
				request.Body = body
			}
		}

		response, err := r.Client.Do(request)
		if err == nil {
			return response, nil
		}
		// A cancelled run stops now; it is not a network failure to retry.
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// pace throttles the run's requests to one every Delay, and is the reason
// --delay means something under --workers.
//
// The naive form — sleep before each request unless it is the first — is both
// a data race and the wrong semantics once several requests are in flight: a
// pause each worker takes independently throttles nothing, because the
// workers simply pause in parallel. What someone throttling a shared server
// wants is a bound on the STREAM, so the wait is computed against the last
// send by anyone, under a lock, and the send time is claimed before the wait
// rather than after — otherwise every waiting worker would read the same last
// send and all wake together.
func (r *Runner) pace(ctx context.Context) error {
	if r.Transport.Delay <= 0 {
		return nil
	}

	r.paceMu.Lock()
	now := time.Now()
	// The first request goes at once; each later one no sooner than Delay
	// after the slot already claimed.
	slot := now
	if !r.lastSend.IsZero() {
		if earliest := r.lastSend.Add(r.Transport.Delay); earliest.After(slot) {
			slot = earliest
		}
	}
	r.lastSend = slot
	r.paceMu.Unlock()

	wait := time.Until(slot)
	if wait <= 0 {
		return nil
	}
	select {
	case <-time.After(wait):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
