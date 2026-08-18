package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/config"
)

// The bound is the one `checks` entry that is not a boolean, so it is the one
// that can be written in a way vertrag cannot read — and the one that decides
// whether a run passes on timing rather than on conformance.
func TestTheResponseTimeBoundIsReadFromAVertragFile(t *testing.T) {
	if d := config.Default(); d.Checks.MaxResponseTime != 0 {
		t.Errorf("Default bound = %s, want none: a bound vertrag chose would be one nobody agreed to", d.Checks.MaxResponseTime)
	}

	path := writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
checks:
  max-response-time: 750ms
`)

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings.Checks.MaxResponseTime != 750*time.Millisecond {
		t.Errorf("bound = %s, want 750ms", settings.Checks.MaxResponseTime)
	}
	// The booleans beside it must still be whatever they were: a section read
	// for one key and left half-applied is worse than one not read at all.
	if !settings.Checks.ServerError || !settings.Checks.ContentType {
		t.Errorf("the default checks were disturbed: %+v", settings.Checks)
	}
}

// The bound is read from whatever file it was found in, like every other key —
// see TestTagIsHonouredWhateverTheFileIsCalled for what changed. It used to be
// refused from a `dredd.yml`, because a bound honoured out of a shared file
// would have had vertrag failing a suite Dredd passed; vertrag no longer
// discovers that file, so the only way one reaches Load is that somebody named
// it, and reading it in full is then the unsurprising thing to do.
func TestTheResponseTimeBoundIsHonouredWhateverTheFileIsCalled(t *testing.T) {
	path := writeConfig(t, "dredd.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
checks:
  max-response-time: 750ms
`)

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings.Checks.MaxResponseTime != 750*time.Millisecond {
		t.Errorf("bound = %s, want 750ms", settings.Checks.MaxResponseTime)
	}
}

// An unreadable bound is refused rather than run without. `750` is not 750ms,
// and a run that quietly timed nothing would report a clean suite to somebody
// who had asked to be told about slow responses — the one outcome worse than
// refusing to start.
func TestTheResponseTimeBoundRejectsADurationItCannotRead(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
checks:
  max-response-time: "750"
`)
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "max-response-time") {
		t.Errorf("Load err = %v, want a max-response-time parse error", err)
	}

	// A negative parses perfectly well and would switch the check off, which is
	// the same silence by another route.
	path = writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
checks:
  max-response-time: -1s
`)
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "max-response-time") {
		t.Errorf("Load err = %v, want a negative bound refused", err)
	}
}
