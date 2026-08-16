package config_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/config"
)

// Keys vertrag reads into its settings and then acts on nowhere must say so.
// Being silent about them is worse than not having them: `names: true` asks for
// a list of transaction names and gets a test run, and `user` asks for
// credentials on every request and gets none.
func TestUnsupportedKeysAreReported(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4000
server: ./start.sh
server-wait: 5
user: admin:secret
path: ['./extra/*.yml']
names: true
inline-errors: true
loglevel: debug
require: ./preload.js
custom: {apiaryApiKey: xyz}
`)

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := append([]string(nil), settings.Unsupported...)
	sort.Strings(got)
	want := []string{"custom", "inline-errors", "loglevel", "names", "path", "require", "server", "user"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("unsupported = %v\nwant           %v", got, want)
	}
}

// The mirror of the above, and the one that actually costs something to get
// wrong. Dredd's own config generator writes every option out explicitly, so a
// migrated file is full of `server: null` and `names: false` — warning about
// those would put eight lines of noise above every run and teach people to
// scroll past the warnings that matter.
func TestExplicitlyEmptyKeysAreSilent(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4000
server: null
server-wait: 0
user: null
path: []
names: false
inline-errors: false
require: null
custom: {}
loglevel: warning
`)

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(settings.Unsupported) != 0 {
		t.Errorf("a config setting everything to its default warned about %v", settings.Unsupported)
	}
}

// A nil *string carried in an `any` is not the untyped nil a type switch tests
// for: the interface is non-nil and holds a nil pointer. Checking these through
// a generic helper reported every one of them as set, which is how the case
// above came to be broken.
func TestTypedNilsAreNotMistakenForValues(t *testing.T) {
	for _, key := range []string{"server", "user", "names", "inline-errors"} {
		t.Run(key, func(t *testing.T) {
			path := writeConfig(t, "vertrag.yml",
				"spec: ./api.yml\nendpoint: http://x\n"+key+": null\n")

			settings, err := config.Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			for _, reported := range settings.Unsupported {
				if reported == key {
					t.Errorf("%s: null was reported as set", key)
				}
			}
		})
	}
}
