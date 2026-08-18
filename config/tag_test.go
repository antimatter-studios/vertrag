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

// `tag` narrows what is run, so it crosses the same boundary as `auth` and
// `skip`: honoured from a vertrag file, refused with a note from a dredd.yml,
// where Dredd would silently run everything the file looks like it excludes.
func TestTagIsIgnoredInADreddFile(t *testing.T) {
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

	if len(settings.Tag) != 0 {
		t.Errorf("tag was honoured from a dredd.yml: %v", settings.Tag)
	}
	if note := strings.Join(settings.Notes, "\n"); !strings.Contains(note, "`tag`") {
		t.Errorf("no note names `tag` as ignored:\n%s", note)
	}
}
