package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/config"
)

func TestAnEndpointsEntryLaysItsTransportOverTheRuns(t *testing.T) {
	settings, err := config.Load(writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
transport:
  timeout: 10s
  retries: 2
endpoints:
  - endpoint: https://internal:8443
    operation-id: [deliver, check]
    transport:
      retries: 0
      cert: ./client.pem
      cert-key: ./client.key
  - endpoint: http://throwaway:8080
    operation-id: [wipe]
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := settings.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(settings.Endpoints) != 2 {
		t.Fatalf("endpoints = %+v", settings.Endpoints)
	}
	first := settings.Endpoints[0]
	if first.Endpoint != "https://internal:8443" || strings.Join(first.OperationIDs, ",") != "deliver,check" {
		t.Fatalf("first = %+v", first)
	}
	got, err := first.Transport(settings.Transport)
	if err != nil {
		t.Fatal(err)
	}
	if got.Timeout != 10*time.Second || got.Retries != 0 || got.ClientCert != "./client.pem" || got.ClientCertKey != "./client.key" {
		t.Fatalf("merged transport = %+v", got)
	}
	if settings.Transport.ClientCert != "" {
		t.Fatal("the entry's certificate reached the run's transport")
	}
	plain, err := settings.Endpoints[1].Transport(settings.Transport)
	if err != nil || plain.Retries != 2 {
		t.Fatalf("no transport of its own: %+v, %v", plain, err)
	}
}

func TestAnEndpointsEntryThatCannotWorkIsRefused(t *testing.T) {
	for name, entries := range map[string]string{
		"needs a scheme": "- {endpoint: internal:8443, operation-id: [a]}",
		"names no":       "- {endpoint: https://internal}",
		"is named twice": "- {endpoint: https://one, operation-id: [a]}\n  - {endpoint: https://two, operation-id: [a]}",
	} {
		settings, err := config.Load(writeConfig(t, "vertrag.yml", "spec: ./api.yml\nendpoint: http://localhost\nendpoints:\n  "+entries+"\n"))
		if err != nil {
			t.Fatalf("%s: Load: %v", name, err)
		}
		if err := settings.Validate(); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: Validate = %v", name, err)
		}
	}
}

func TestAnEndpointsEntryWithABadTransportStopsTheLoad(t *testing.T) {
	_, err := config.Load(writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost
endpoints:
  - endpoint: https://internal
    operation-id: [a]
    transport: {timeout: soon}
`))
	if err == nil || !strings.Contains(err.Error(), "endpoints entry 1") {
		t.Fatalf("Load = %v", err)
	}
}
