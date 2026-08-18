package config_test

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/config"
)

// TestAPinAndAnAcceptListAreReadFromTheFile pins the two settings that decide
// whether a probing phase can be pointed at an API where some generated value
// would do something irreversible.
func TestAPinAndAnAcceptListAreReadFromTheFile(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
fuzz:
  pin:
    dry_run: true
  accept: [409, 422]
`)

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings.Fuzz.Pin["dry_run"] != true {
		t.Errorf("Pin = %v, want dry_run held true", settings.Fuzz.Pin)
	}
	if len(settings.Fuzz.Accept) != 2 || settings.Fuzz.Accept[0] != 409 {
		t.Errorf("Accept = %v, want [409 422]", settings.Fuzz.Accept)
	}
}

// TestAnAcceptListThatWouldHideAServerFailureIsRefusedAtLoad pins that the
// refusal happens before the run starts rather than at the point of use — the
// point of use is after the first request has already gone out.
func TestAnAcceptListThatWouldHideAServerFailureIsRefusedAtLoad(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
fuzz:
  accept: [500]
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("accepting a 500 should stop the run at load")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("the error does not name the status: %v", err)
	}
}

// TestNoFuzzSafetySettingsIsStillFine guards the two refusals against
// overreach: neither key is required, and a file without them loads.
func TestNoFuzzSafetySettingsIsStillFine(t *testing.T) {
	settings, err := config.Load(writeConfig(t, "vertrag.yml",
		"spec: ./api.yml\nendpoint: http://localhost:4210\nfuzz:\n  cases: 5\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(settings.Fuzz.Pin) != 0 || len(settings.Fuzz.Accept) != 0 {
		t.Errorf("something was invented: pin=%v accept=%v", settings.Fuzz.Pin, settings.Fuzz.Accept)
	}
}
