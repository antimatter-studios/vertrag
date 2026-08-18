package config_test

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/config"
)

func TestTheGraphQLSectionIsRead(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
spec: ./schema.graphql
endpoint: http://localhost:4000
graphql:
  path: /api/graphql
  max-depth: 6
  mutations: true
`)

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings.GraphQL.Path != "/api/graphql" {
		t.Errorf("path = %q", settings.GraphQL.Path)
	}
	if settings.GraphQL.MaxDepth != 6 {
		t.Errorf("max-depth = %d", settings.GraphQL.MaxDepth)
	}
	if !settings.GraphQL.Mutations {
		t.Error("mutations: true was not read, so a run that asked for them would send none")
	}
	if len(settings.Unsupported) != 0 {
		t.Errorf("the graphql keys were reported as unsupported: %v", settings.Unsupported)
	}
}

// The default has to be the safe one, and it has to stay the default when the
// section is present for other reasons: a file setting only `graphql.path`
// must not acquire mutations along with it.
func TestMutationsAreOffUnlessTheFileSaysSo(t *testing.T) {
	for _, body := range []string{
		"spec: ./schema.graphql\nendpoint: http://localhost:4000\n",
		"spec: ./schema.graphql\nendpoint: http://localhost:4000\ngraphql:\n  path: /api/graphql\n",
		"spec: ./schema.graphql\nendpoint: http://localhost:4000\ngraphql:\n  mutations: false\n",
	} {
		settings, err := config.Load(writeConfig(t, "vertrag.yml", body))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if settings.GraphQL.Mutations {
			t.Errorf("mutations came on by themselves for:\n%s", body)
		}
	}
}

// A depth of zero produces a run with no transactions in it, which reads
// exactly like a schema with nothing in it. The number is the reader's choice,
// so one they cannot have meant is worth stopping for.
func TestADepthNobodyCouldHaveMeantIsRefused(t *testing.T) {
	_, err := config.Load(writeConfig(t, "vertrag.yml", `
spec: ./schema.graphql
endpoint: http://localhost:4000
graphql:
  max-depth: 0
`))
	if err == nil || !strings.Contains(err.Error(), "max-depth") {
		t.Errorf("err = %v, want a refusal naming max-depth", err)
	}
}
