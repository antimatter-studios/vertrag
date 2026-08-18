package config_test

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/config"
)

func TestTagFromVertragFile(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
tag:
  - network
  - admin
`)

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(settings.Tag) != 2 || settings.Tag[0] != "network" || settings.Tag[1] != "admin" {
		t.Errorf("Tag = %v, want [network admin]", settings.Tag)
	}
}

// The file's name no longer decides which of its keys are read.
//
// `tag` used to be refused from a `dredd.yml` — along with `auth`, `skip`,
// `transport`, `operation-id`, `max-failures`, `workers`, `phases`, `fuzz` and
// the conditional `header` entries — because vertrag discovered a dredd.yml
// automatically, and honouring keys Dredd ignores without a word would have had
// the two testers silently testing different things from one file that looked
// shared. Nothing is discovered by that name any more, so the only way a file
// called dredd.yml reaches Load is that someone named it, and refusing half of
// what they pointed at would be the surprise. This test and the ones below it
// pin the inversion: same file, same name, now honoured.
func TestTagIsHonouredWhateverTheFileIsCalled(t *testing.T) {
	path := writeConfig(t, "dredd.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
tag:
  - network
`)

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(settings.Tag) != 1 || settings.Tag[0] != "network" {
		t.Errorf("Tag = %v, want [network] honoured from a file named dredd.yml", settings.Tag)
	}
	if note := strings.Join(settings.Notes, "\n"); strings.Contains(note, "ignored") {
		t.Errorf("nothing was ignored, so nothing should say so:\n%s", note)
	}
}

func TestOperationIDAndMaxFailuresFromVertragFile(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
operation-id: [listThings, createThing]
max-failures: 3
`)
	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(settings.OperationID) != 2 || settings.OperationID[1] != "createThing" {
		t.Errorf("OperationID = %v", settings.OperationID)
	}
	if settings.MaxFailures != 3 {
		t.Errorf("MaxFailures = %d, want 3", settings.MaxFailures)
	}
}

func TestOperationIDAndMaxFailuresHonouredWhateverTheFileIsCalled(t *testing.T) {
	path := writeConfig(t, "dredd.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
operation-id: [listThings]
max-failures: 3
`)
	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(settings.OperationID) != 1 || settings.MaxFailures != 3 {
		t.Errorf("not honoured: %v / %d", settings.OperationID, settings.MaxFailures)
	}
}

func TestPhasesFromVertragFile(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
phases: [fuzz, coverage]
fuzz:
  seed: 7
  cases: 10
  whole-request: true
`)
	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Order is fixed and examples is always first, whatever was written.
	if strings.Join(settings.Phases, ",") != "examples,coverage,fuzz" {
		t.Errorf("Phases = %v, want examples,coverage,fuzz", settings.Phases)
	}
	if settings.Fuzz.Seed != 7 || settings.Fuzz.Cases != 10 || !settings.Fuzz.WholeRequest {
		t.Errorf("Fuzz = %+v", settings.Fuzz)
	}
}

func TestPhasesHonouredWhateverTheFileIsCalled(t *testing.T) {
	path := writeConfig(t, "dredd.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
phases: [coverage]
`)
	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if strings.Join(settings.Phases, ",") != "examples,coverage" {
		t.Errorf("Phases = %v, want examples,coverage", settings.Phases)
	}
}

func TestUnknownPhaseIsAConfigError(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
phases: [examples, fuzzing]
`)
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "fuzzing") {
		t.Errorf("Load err = %v, want the unknown phase named", err)
	}
}

func TestOAuth2FromVertragFile(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
auth:
  oauth2:
    token-url: https://id.example.com/oauth/token
    client-id: suite
    client-secret-env: SUITE_SECRET
    scopes: [read, write]
`)
	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !settings.Auth.Configured() {
		t.Error("an oauth2 block is authentication being configured")
	}
	o := settings.Auth.OAuth2
	if o.TokenURL != "https://id.example.com/oauth/token" || o.ClientID != "suite" ||
		o.ClientSecretEnv != "SUITE_SECRET" || len(o.Scopes) != 2 {
		t.Errorf("OAuth2 = %+v", o)
	}
}
