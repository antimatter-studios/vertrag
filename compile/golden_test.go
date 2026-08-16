package compile_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
)

// update rewrites the recorded output instead of comparing against it.
//
//	go test ./compile/ -run TestGolden -update
//
// Regenerating is a deliberate act with a reviewable diff: the diff IS the
// behaviour change, and it is the only place a change to what vertrag derives
// from a document has to be looked at directly.
var update = flag.Bool("update", false, "rewrite the recorded transactions")

// mediaTypes maps a fixture directory to the media type its documents are read
// as. API Blueprint is absent: vertrag does not parse it, so there is nothing to
// record.
var mediaTypes = map[string]string{
	"openapi2": "application/swagger+json",
	"openapi3": "application/vnd.oai.openapi",
}

// TestGoldenTransactions pins what vertrag derives from a corpus of real
// description documents.
//
// This is the regression net for parsing and compiling, and it exists in this
// form because the corpus tests cannot provide one. The corpus server is built
// from vertrag's own reading of a document, so a misparse produces a server and
// a tester that are wrong in exactly the same way and the test passes. Only
// something holding the output still can catch that.
//
// The recorded files were captured while the differential against the reference
// implementation held over every one of these fixtures, so they carry that
// verification forward without needing it to be re-run — or a Node runtime
// installed — on every commit.
func TestGoldenTransactions(t *testing.T) {
	root := repoRoot(t)

	for _, dir := range sortedKeys(mediaTypes) {
		t.Run(dir, func(t *testing.T) {
			documents := documentsIn(t, filepath.Join(root, "oracle", "corpus", dir))
			if len(documents) == 0 {
				t.Skipf("no source documents in %s", dir)
			}

			for _, document := range documents {
				name := strings.TrimSuffix(filepath.Base(document), filepath.Ext(document))
				t.Run(name, func(t *testing.T) {
					source, err := os.ReadFile(document)
					if err != nil {
						t.Fatalf("reading %s: %v", document, err)
					}

					parsed, err := apidesc.Parse(source, document)
					if err != nil {
						t.Fatalf("parsing %s: %v", document, err)
					}
					result := compile.Compile(parsed.MediaType, parsed.Elements, filepath.Base(document))

					got, err := json.MarshalIndent(recorded{
						Transactions: result.Transactions,
						Annotations:  annotationText(result),
					}, "", "  ")
					if err != nil {
						t.Fatalf("encoding: %v", err)
					}
					got = append(got, '\n')

					golden := filepath.Join(root, "compile", "testdata", "golden", dir, name+".json")
					if *update {
						writeGolden(t, golden, got)
						return
					}

					want, err := os.ReadFile(golden)
					if err != nil {
						t.Fatalf("no recorded output for %s (run with -update to create it): %v", name, err)
					}
					if string(got) != string(want) {
						t.Errorf("what vertrag derives from %s has changed.\n"+
							"If the change is intended, re-record with:\n"+
							"    go test ./compile/ -run TestGolden -update\n"+
							"and review the diff.\n\n%s",
							filepath.Base(document), firstDifference(string(want), string(got)))
					}
				})
			}
		})
	}
}

// recorded is what gets pinned: the transactions a document yields, and the
// diagnostics raised reading it.
//
// Annotations are reduced to their text. Their source-map offsets move whenever
// a fixture is reformatted, which would make the recording fail for a reason
// having nothing to do with behaviour.
type recorded struct {
	Transactions []compile.Transaction `json:"transactions"`
	Annotations  []string              `json:"annotations,omitempty"`
}

func annotationText(result compile.Result) []string {
	var text []string
	for _, annotation := range result.Annotations {
		text = append(text, annotation.Type+": "+annotation.Message)
	}
	sort.Strings(text)
	return text
}

// firstDifference reports the first line that differs, with a little context.
// A whole-file dump of two hundred lines of JSON buries the one line that
// actually moved.
func firstDifference(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")

	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		var wantLine, gotLine string
		if i < len(wantLines) {
			wantLine = wantLines[i]
		}
		if i < len(gotLines) {
			gotLine = gotLines[i]
		}
		if wantLine == gotLine {
			continue
		}

		var context strings.Builder
		for back := max(0, i-3); back < i; back++ {
			context.WriteString("  " + wantLines[back] + "\n")
		}
		context.WriteString("- " + wantLine + "\n")
		context.WriteString("+ " + gotLine + "\n")
		return "first difference at line " + strconv.Itoa(i+1) + ":\n" + context.String()
	}
	return "the files differ in trailing whitespace only"
}

func writeGolden(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func documentsIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading %s: %v", dir, err)
	}

	// Only the source documents. Each is paired with a `.json` holding the API
	// Elements it should parse to, and feeding one of those back in as a
	// description reads it as an unrecognised format — which is correct, and
	// nothing to record.
	var documents []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		switch filepath.Ext(entry.Name()) {
		case ".apib", ".yml", ".yaml":
			documents = append(documents, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(documents)
	return documents
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

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
