// Package oracle differential-tests vertrag against Dredd.
//
// Dredd is treated as the oracle: for every fixture, both implementations are
// run over the same input and their output must agree. This is what keeps the
// port honest — a Go implementation that merely looks reasonable but disagrees
// with Dredd would break the hook files and CI pipelines that already depend on
// Dredd's exact behaviour.
//
// The reference runs as a subprocess (Node), so these tests skip rather than
// fail when its dependencies are not installed. Run `make oracle-deps` first.
package oracle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/internal/compile"
	"github.com/antimatter-studios/vertrag/internal/refract"
)

// mediaTypes maps a corpus directory to the media type its documents were
// parsed from. The compiler branches on it, so it is part of the input.
var mediaTypes = map[string]string{
	"apib":     "text/vnd.apiblueprint",
	"openapi2": "application/swagger+yaml",
	"openapi3": "application/vnd.oai.openapi",
}

func TestCompileMatchesReference(t *testing.T) {
	root := repoRoot(t)
	requireReference(t, root)

	for _, dir := range sortedKeys(mediaTypes) {
		t.Run(dir, func(t *testing.T) {
			for _, fixture := range fixturesIn(t, filepath.Join(root, "oracle", "corpus", dir)) {
				t.Run(strings.TrimSuffix(filepath.Base(fixture), ".json"), func(t *testing.T) {
					t.Parallel()
					compareFixture(t, root, mediaTypes[dir], fixture)
				})
			}
		})
	}
}

// compareFixture compiles one API Elements document with both implementations
// and reports every field on which they disagree.
func compareFixture(t *testing.T, root, mediaType, fixture string) {
	t.Helper()

	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	element, err := refract.Load(data)
	if err != nil {
		t.Fatalf("loading API Elements: %v", err)
	}

	// Round-tripping through JSON puts vertrag's result in the same shape the
	// reference's output arrives in, so the comparison is between the data both
	// produce rather than between Go types and JavaScript objects.
	got := roundTrip(t, compile.Compile(mediaType, element, ""))
	want := runReference(t, root, mediaType, fixture)

	for _, diff := range diffValues("", want, got) {
		t.Errorf("%s", diff)
	}
}

func roundTrip(t *testing.T, result compile.Result) any {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encoding result: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	return decoded
}

// runReference shells out to the real Dredd compiler.
func runReference(t *testing.T, root, mediaType, fixture string) any {
	t.Helper()
	return runReferenceWith(t, root, "--media-type", mediaType, fixture)
}

// runReferenceWith invokes the reference driver and decodes what it prints.
func runReferenceWith(t *testing.T, root string, args ...string) any {
	t.Helper()

	cmd := exec.Command("node",
		append([]string{filepath.Join(root, "oracle", "reference", "compile.js")}, args...)...,
	)
	cmd.Dir = filepath.Join(root, "oracle", "reference")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("reference implementation failed: %v\n%s", err, stderr.String())
	}

	var decoded any
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decoding reference output: %v\n%s", err, stdout.String())
	}
	return decoded
}

// diffValues walks two decoded JSON values and describes where they differ.
//
// It reports paths rather than dumping both documents, because a whole-document
// diff of a large API description buries the one field that actually moved.
func diffValues(path string, want, got any) []string {
	switch expected := want.(type) {
	case map[string]any:
		actual, ok := got.(map[string]any)
		if !ok {
			return []string{mismatch(path, want, got)}
		}
		var diffs []string
		for _, key := range unionKeys(expected, actual) {
			expectedValue, inExpected := expected[key]
			actualValue, inActual := actual[key]
			switch {
			case !inActual:
				diffs = append(diffs, fmt.Sprintf("%s: missing, reference has %s",
					join(path, key), render(expectedValue)))
			case !inExpected:
				diffs = append(diffs, fmt.Sprintf("%s: unexpected %s, reference omits it",
					join(path, key), render(actualValue)))
			default:
				diffs = append(diffs, diffValues(join(path, key), expectedValue, actualValue)...)
			}
		}
		return diffs

	case []any:
		actual, ok := got.([]any)
		if !ok {
			return []string{mismatch(path, want, got)}
		}
		if len(expected) != len(actual) {
			return []string{fmt.Sprintf("%s: reference has %d item(s), vertrag has %d",
				pathOrRoot(path), len(expected), len(actual))}
		}
		var diffs []string
		for i := range expected {
			diffs = append(diffs, diffValues(fmt.Sprintf("%s[%d]", path, i), expected[i], actual[i])...)
		}
		return diffs

	default:
		if !reflect.DeepEqual(want, got) {
			return []string{mismatch(path, want, got)}
		}
		return nil
	}
}

func mismatch(path string, want, got any) string {
	return fmt.Sprintf("%s: reference has %s, vertrag has %s", pathOrRoot(path), render(want), render(got))
}

// render shows a value compactly for a failure message.
//
// HTML escaping is off because transaction names are joined with " > ", and
// seeing them as ">" makes the one message a developer reads when the
// oracle fails harder to read than it needs to be.
func render(v any) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return fmt.Sprintf("%v", v)
	}
	encoded := strings.TrimSuffix(buf.String(), "\n")

	const limit = 200
	if len(encoded) > limit {
		return encoded[:limit] + "…"
	}
	return encoded
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func pathOrRoot(path string) string {
	if path == "" {
		return "(root)"
	}
	return path
}

func unionKeys(a, b map[string]any) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	return sortedKeysOf(seen)
}

func fixturesIn(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("listing fixtures: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("no fixtures in %s", dir)
	}
	sort.Strings(matches)
	return matches
}

// requireReference skips the suite when the reference's dependencies are
// absent, so a fresh checkout does not report a wall of failures that are
// really just a missing install step.
func requireReference(t *testing.T, root string) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed; the oracle needs it to run the reference implementation")
	}
	modules := filepath.Join(root, "oracle", "reference", "node_modules")
	if _, err := os.Stat(modules); err != nil {
		t.Skip("reference dependencies are not installed; run `make oracle-deps`")
	}
}

// repoRoot walks up from the test's working directory looking for go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("locating working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the repository root")
		}
		dir = parent
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
