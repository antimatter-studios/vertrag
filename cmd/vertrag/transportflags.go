package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/runner"
)

// transportFlags are the network knobs `run` and `fuzz` share. One definition
// so the two commands cannot drift: a flag that means one thing to run and
// another to fuzz is a flag someone will pass to the wrong one.
type transportFlags struct {
	timeout  time.Duration
	retries  int
	delay    time.Duration
	insecure bool
	caCert   string
	cert     string
	certKey  string
	proxy    string

	// set records which flags were actually given, so a zero value written on
	// purpose ("--retries 0") can override the config file and one that was
	// simply not mentioned cannot.
	set map[string]bool
}

func addTransportFlags(fs *flag.FlagSet, f *transportFlags) {
	fs.DurationVar(&f.timeout, "timeout", 0, "per-request timeout, e.g. 10s (default 30s)")
	fs.IntVar(&f.retries, "retries", 0, "retry a request this many times on a network failure — never on a response")
	fs.DurationVar(&f.delay, "delay", 0, "pause between requests, e.g. 200ms, for a server that throttles")
	fs.BoolVar(&f.insecure, "insecure", false, "skip TLS certificate verification (self-signed test servers only)")
	fs.StringVar(&f.caCert, "ca-cert", "", "PEM bundle to trust in addition to the system roots")
	fs.StringVar(&f.cert, "cert", "", "PEM client certificate to present to a server that requires mutual TLS")
	fs.StringVar(&f.certKey, "cert-key", "", "private key for --cert, when it is not in that file already")
	fs.StringVar(&f.proxy, "proxy", "", "HTTP(S) proxy URL (default: the HTTP_PROXY/HTTPS_PROXY environment)")
}

// noteGiven must be called after parsing; it records which flags were set.
func (f *transportFlags) noteGiven(fs *flag.FlagSet) {
	f.set = map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { f.set[fl.Name] = true })
}

// apply lays the flags over the config file's transport section: a flag that
// was given wins, one that was not leaves the file's value alone.
func (f transportFlags) apply(t *config.Transport) {
	if f.set["timeout"] {
		t.Timeout = f.timeout
	}
	if f.set["retries"] {
		t.Retries = f.retries
	}
	if f.set["delay"] {
		t.Delay = f.delay
	}
	if f.set["insecure"] {
		t.Insecure = f.insecure
	}
	if f.set["ca-cert"] {
		t.CACert = f.caCert
	}
	if f.set["cert"] {
		t.ClientCert = f.cert
	}
	if f.set["cert-key"] {
		t.ClientCertKey = f.certKey
	}
	if f.set["proxy"] {
		t.Proxy = f.proxy
	}
}

// newEngine builds the runner from the resolved settings, failing before any
// request when the transport itself cannot be built.
func newEngine(settings config.Config) (*runner.Runner, error) {
	engine, err := runner.NewWithTransport(settings.Endpoint, runner.Transport{
		Timeout:       settings.Transport.Timeout,
		Retries:       settings.Transport.Retries,
		Delay:         settings.Transport.Delay,
		Insecure:      settings.Transport.Insecure,
		CACert:        settings.Transport.CACert,
		ClientCert:    settings.Transport.ClientCert,
		ClientCertKey: settings.Transport.ClientCertKey,
		Proxy:         settings.Transport.Proxy,
	})
	if err != nil {
		return nil, fmt.Errorf("transport: %w", err)
	}
	return engine, nil
}
