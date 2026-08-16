package fuzz_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestProbeWorksInACompiledBinary guards the one failure mode this package's
// own tests cannot see.
//
// rapid.Check consults testing.Short, which panics with "Short called before
// Init" outside a Go test, and then "before Parse" once Init is added. Every
// test in this package runs under `go test`, where both conditions are already
// met, so all of them would keep passing while `vertrag fuzz` panicked the
// first time anyone ran it. The only way to pin it is to compile a real command
// and run it.
func TestProbeWorksInACompiledBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}

	output, err := exec.Command("go", "run", "./testdata/clibinary").CombinedOutput()
	if err != nil {
		t.Fatalf("running the binary: %v\n%s", err, output)
	}
	if !strings.HasPrefix(string(output), "OK") {
		t.Errorf("binary did not report success:\n%s", output)
	}
}
