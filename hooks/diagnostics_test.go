package hooks

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestAWorkerThatDiesSaysWhy pins a diagnostic, which is the whole of the fix.
//
// A hook file with a syntax error, a port already held, a missing TypeScript
// loader — every one of them ends the worker before it announces itself, and
// every one of them used to report the same bare line: "the hooks worker exited
// before it was ready". The cause was on a stream nobody read: Options.Stderr
// is nil unless a caller sets one, and a nil Stderr on exec.Cmd goes to
// /dev/null. A CI run failed exactly this way and told us nothing, which is
// what prompted this.
func TestAWorkerThatDiesSaysWhy(t *testing.T) {
	interpreterFor(t, "nodejs")

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.js")
	// Deliberately unparseable.
	if err := writeFile(path, "this is (not javascript at all;\n"); err != nil {
		t.Fatal(err)
	}

	client, err := startWorker(t, Options{
		Language:  "nodejs",
		Hookfiles: []string{path},
		Host:      "127.0.0.1",
	})
	if client != nil {
		client.Stop()
	}
	if err == nil {
		t.Fatal("a hook file that cannot be parsed should not start")
	}

	// The bare line on its own is what this exists to stop.
	if !strings.Contains(err.Error(), "the hooks worker exited before it was ready") {
		t.Errorf("the error lost its summary: %v", err)
	}
	// And the worker's own account must travel with it. Node names the file and
	// the kind of failure; either alone makes the error actionable, where
	// neither did before.
	report := err.Error()
	if !strings.Contains(report, "hooks.js") && !strings.Contains(report, "SyntaxError") {
		t.Errorf("the worker's own account did not reach the error, so it still says nothing:\n%s", report)
	}
}
