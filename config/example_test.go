package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/config"
)

// The example file is the first thing anyone copies. A key renamed here without
// it being updated leaves the documentation quietly wrong, so it is loaded for
// real rather than trusted.
func TestExampleConfigLoads(t *testing.T) {
	// Copied under a vertrag name, which no longer changes how it is read but
	// still matches how a project would hold it.
	source, err := os.ReadFile(filepath.Join("..", "vertrag.example.yml"))
	if err != nil {
		t.Fatalf("reading the example: %v", err)
	}
	path := filepath.Join(t.TempDir(), "vertrag.yml")
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatalf("writing the copy: %v", err)
	}

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("the example config does not load: %v", err)
	}

	if len(settings.Unsupported) != 0 {
		t.Errorf("the example uses keys vertrag does not support: %v", settings.Unsupported)
	}
	if len(settings.Notes) != 0 {
		t.Errorf("the example produced notes: %v", settings.Notes)
	}

	// Each documented feature is checked, so an example demonstrating a key that
	// silently stopped being read cannot pass.
	if !settings.Auth.Configured() {
		t.Error("the example's auth block was not read")
	}
	if settings.Auth.Cookie == "" || settings.Auth.Carry != "cookie" {
		t.Errorf("auth carry = %q, cookie = %q", settings.Auth.Carry, settings.Auth.Cookie)
	}
	if len(settings.Auth.Except) == 0 {
		t.Error("the example's auth.except was not read")
	}
	if len(settings.ConditionalHeaders) == 0 {
		t.Error("the example's conditional headers were not read")
	}
	if len(settings.Header) == 0 {
		t.Error("the example's plain header entry was not read")
	}
	// The response-time bound is the only `checks` entry with a value rather
	// than a switch, so it is the only one the example can demonstrate wrongly:
	// a renamed key or a unit misread leaves the file looking right.
	if settings.Checks.MaxResponseTime != 750*time.Millisecond {
		t.Errorf("the example's max-response-time read as %s, want 750ms", settings.Checks.MaxResponseTime)
	}
	if len(settings.Skip) < 2 {
		t.Errorf("the example's skip list read as %d entries, want both forms", len(settings.Skip))
	}
	// The narrowing keys the example demonstrates with a value. An empty list
	// in the file proves nothing was read, so only the filled ones are checked
	// — and every one of them is a key that decides what reaches the server.
	for key, got := range map[string][]string{
		"only-matching":    settings.OnlyMatching,
		"exclude-matching": settings.ExcludeMatching,
		"exclude-method":   settings.ExcludeMethod,
		"exclude-tag":      settings.ExcludeTag,
	} {
		if len(got) == 0 {
			t.Errorf("the example's %s was not read", key)
		}
	}

	// One of each skip form, since the point of accepting both is that both work.
	var withReason, bare int
	for _, rule := range settings.Skip {
		if rule.Name == "" {
			t.Error("a skip rule parsed with no name")
		}
		if rule.Reason == "" {
			bare++
		} else {
			withReason++
		}
	}
	if withReason == 0 || bare == 0 {
		t.Errorf("skip forms read: %d with a reason, %d bare; want both demonstrated", withReason, bare)
	}

	// The GraphQL section, whose two live keys are read and whose third is
	// deliberately commented out. Copying the example must not turn mutations
	// on: the file people copy is the file that decides what a first run
	// sends, and a first run that mutates is the one nobody expected.
	if settings.GraphQL.Path == "" || settings.GraphQL.MaxDepth == 0 {
		t.Errorf("the example's graphql block was not read: %+v", settings.GraphQL)
	}
	if settings.GraphQL.Mutations {
		t.Error("the example enables mutations; copying it would send them")
	}
}
