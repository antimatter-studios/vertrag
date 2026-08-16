package oracle

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/internal/apidesc"
	"github.com/antimatter-studios/vertrag/internal/compile"
)

// TestParseMatchesReference is the end-to-end contract for the format parsers:
// a description document in its original format must yield the same
// transactions as Dredd derives from it.
//
// The compile-stage oracle already pins everything downstream of API Elements,
// so a failure here is a parser failure. The two stages are kept separate for
// exactly that reason — a single end-to-end comparison would report a parser
// bug and a compiler bug identically.
//
// Transactions and annotations are reported separately. Transactions are what
// gets executed against a server and what hooks address by name, so they are
// the part that has to be right first; annotation wording is a diagnostic
// detail that can converge afterwards without anything being silently wrong.
func TestParseMatchesReference(t *testing.T) {
	root := repoRoot(t)
	requireReference(t, root)

	for _, dir := range sortedKeys(mediaTypes) {
		t.Run(dir, func(t *testing.T) {
			documents := documentsIn(t, filepath.Join(root, "oracle", "corpus", dir))
			if len(documents) == 0 {
				t.Skipf("no source documents in %s", dir)
			}
			// A format with no parser is reported once, as a skip naming what
			// is missing. Letting every fixture fail instead would bury the
			// formats that ARE covered under noise that says nothing new.
			if !apidesc.Implemented(mediaTypes[dir]) {
				t.Skipf("no %s parser yet: %d document(s) in the corpus are not covered",
					mediaTypes[dir], len(documents))
			}

			for _, document := range documents {
				t.Run(baseName(document), func(t *testing.T) {
					t.Parallel()
					compareDocument(t, root, document)
				})
			}
		})
	}
}

func compareDocument(t *testing.T, root, document string) {
	t.Helper()

	source, err := os.ReadFile(document)
	if err != nil {
		t.Fatalf("reading document: %v", err)
	}

	// The reference records the file's base name as the transaction origin, so
	// vertrag is given the same one; otherwise every name would differ by a
	// path and hide the differences that matter.
	filename := filepath.Base(document)

	result, err := apidesc.Parse(source, filename)
	if err != nil {
		t.Fatalf("vertrag failed to parse the document: %v", err)
	}
	got := roundTrip(t, compile.Compile(result.MediaType, result.Elements, filename))

	want := runReferenceParse(t, root, document, filename)

	gotMap, _ := got.(map[string]any)
	wantMap, _ := want.(map[string]any)
	if gotMap == nil || wantMap == nil {
		t.Fatalf("unexpected result shape")
	}

	if gotMap["mediaType"] != wantMap["mediaType"] {
		t.Errorf("mediaType: reference has %v, vertrag has %v", wantMap["mediaType"], gotMap["mediaType"])
	}

	for _, diff := range diffValues("transactions", wantMap["transactions"], gotMap["transactions"]) {
		t.Errorf("%s", diff)
	}
	for _, diff := range diffValues("annotations", wantMap["annotations"], gotMap["annotations"]) {
		t.Errorf("%s", diff)
	}
}

// runReferenceParse drives the reference through parse and compile in one pass.
func runReferenceParse(t *testing.T, root, document, filename string) any {
	t.Helper()
	return runReferenceWith(t, root, "--parse", "--filename", filename, document)
}

// documentsIn lists the description documents in a corpus directory, which are
// everything except the API Elements JSON they are paired with.
func documentsIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("listing %s: %v", dir, err)
	}
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

func baseName(path string) string {
	name := filepath.Base(path)
	return strings.TrimSuffix(name, filepath.Ext(name))
}
