package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/config"
)

func TestTransportFromVertragFile(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
transport:
  timeout: 10s
  retries: 2
  delay: 250ms
  insecure: true
  ca-cert: /etc/ssl/private-ca.pem
  proxy: http://proxy.local:3128
`)

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tr := settings.Transport
	if tr.Timeout != 10*time.Second || tr.Retries != 2 || tr.Delay != 250*time.Millisecond ||
		!tr.Insecure || tr.CACert != "/etc/ssl/private-ca.pem" || tr.Proxy != "http://proxy.local:3128" {
		t.Errorf("Transport = %+v", tr)
	}
}

// `transport` is read from whatever file it was found in, like every other key
// — see TestTagIsHonouredWhateverTheFileIsCalled for what changed.
func TestTransportIsHonouredWhateverTheFileIsCalled(t *testing.T) {
	path := writeConfig(t, "dredd.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
transport:
  timeout: 10s
`)

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings.Transport.Timeout != 10*time.Second {
		t.Errorf("Transport.Timeout = %v, want 10s", settings.Transport.Timeout)
	}
}

// A duration vertrag cannot read is an error, not a silent default: "10" is
// not ten seconds and running with 30 s while the file says 10 is worse than
// refusing to start.
func TestTransportRejectsAnUnreadableDuration(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
transport:
  timeout: "10"
`)
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("Load err = %v, want a timeout parse error", err)
	}

	path = writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
transport:
  retries: -1
`)
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "retries") {
		t.Errorf("Load err = %v, want a retries error", err)
	}
}
