// Package corpus holds vertrag's own API descriptions and a server that hosts
// them.
//
// Testing a contract tester needs an API to point it at, and borrowing a real
// project's is a poor way to get one. It has to be running, it only exercises
// the features that project happens to use, and a change to it silently changes
// what the tests cover — a suite that passes because an endpoint was removed
// looks exactly like a suite that passes because the code is right. vertrag was
// developed against one such project and inherited all three problems: features
// it does not use, chiefly OpenAPI links and response header schemas, had
// nowhere to be tested at all.
//
// The descriptions here are the corpus, and the server answers exactly what
// they promise. That inverts the usual difficulty: a run against a conforming
// server should report NOTHING, so every finding is a false positive and the
// baseline is checkable. Faults are then switched on one at a time, and each
// should produce its own finding and no others — which measures the two ways a
// contract tester can be wrong, missing a real violation and inventing one,
// rather than only the first.
//
// A description added here is hosted with no new code, because the server
// serves what the description says rather than a handler written to match it.
package corpus

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed descriptions
var descriptions embed.FS

// Names lists the corpus descriptions, without their extensions.
func Names() []string {
	entries, err := fs.ReadDir(descriptions, "descriptions")
	if err != nil {
		return nil
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		names = append(names, strings.TrimSuffix(name, path.Ext(name)))
	}
	sort.Strings(names)
	return names
}

// Load reads one description by name.
//
// The documents are embedded rather than read from disk so that a test needs no
// working directory and a consumer of this package needs no copy of the
// repository.
func Load(name string) ([]byte, error) {
	for _, extension := range []string{".yml", ".yaml", ".json"} {
		if source, err := descriptions.ReadFile("descriptions/" + name + extension); err == nil {
			return source, nil
		}
	}
	return nil, fmt.Errorf("no description named %q; have %s", name, strings.Join(Names(), ", "))
}
