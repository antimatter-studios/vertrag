package oracle

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
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

// divergence is a difference from Dredd that vertrag intends.
//
// Recording one is not a way to silence a failing test. Each entry has to say
// what differs and why, and the suite fails if a recorded divergence stops
// diverging — a ledger that is not checked becomes a list of things that used
// to be true.
type divergence struct {
	fixture string
	path    string
	reason  string
}

var divergences = []divergence{
	{
		fixture: "media-types",
		path:    "transactions[0].request.body",
		reason: "Dredd sends an empty body for application/x-www-form-urlencoded, " +
			"exactly as it does for multipart, so an endpoint taking a form is " +
			"sent nothing and any server requiring its fields answers 400 — the " +
			"endpoint cannot be tested at all. vertrag encodes the object.",
	},
	{
		fixture: "media-types",
		path:    "transactions[1].request.body",
		reason: "Dredd sends an empty body for multipart/form-data, so a project " +
			"testing file uploads has to skip those endpoints. vertrag assembles " +
			"the parts from the schema.",
	},
	{
		fixture: "media-types",
		path:    "transactions[1].request.headers[0].value",
		reason: "the generated multipart body needs its boundary in the " +
			"Content-Type header, or the server cannot parse the parts.",
	},
	{
		fixture: "request-bodies",
		path:    "transactions",
		reason: "Dredd sends the first named example and drops the rest, so a " +
			"document illustrating an accepted and a rejected request is only " +
			"ever tested on the accepted one. vertrag sends each named example " +
			"as its own exchange, which is the extra transaction here.",
	},
	{
		fixture: "request-bodies",
		path:    "annotations",
		reason: "Dredd warns that it ignored the examples after the first. " +
			"vertrag sends them, so it has nothing to warn about.",
	},
	{
		fixture: "media-types",
		path:    "annotations",
		reason:  constraintsAreSupported,
	},
	{
		fixture: "composition",
		path:    "transactions[0].response.body",
		reason: "Dredd acts on none of allOf, anyOf or oneOf when the schema " +
			"has no type of its own, so a body composed with allOf gets no " +
			"specimen at all — and an endpoint whose REQUEST body is an allOf " +
			"is therefore sent nothing, which any server requiring the body " +
			"rejects. vertrag merges the branches, since a value satisfying " +
			"only the first satisfies the allOf no better than one satisfying " +
			"none.",
	},
	{
		fixture: "composition",
		path:    "transactions[2].response.body",
		reason:  falsyValuesAreStillValues,
	},
	{
		fixture: "composition",
		path:    "transactions[4].response.body",
		reason:  falsyValuesAreStillValues,
	},
	{
		fixture: "proof-of-concept",
		path:    "transactions[1].response.body",
		reason:  falsyValuesAreStillValues,
	},
	{
		fixture: "proof-of-concept",
		path:    "transactions[3].response.body",
		reason:  falsyValuesAreStillValues,
	},
	{
		fixture: "composition",
		path:    "transactions[3].response.body",
		reason: "Dredd sends the zero value of the declared type whatever the " +
			"schema says, so a string with a minLength is demonstrated by \"\" " +
			"— a body the document itself calls invalid. Sent as a REQUEST body " +
			"that makes any server correctly enforcing its own contract answer " +
			"400, and the run blames the server for doing what it published. " +
			"vertrag sends the smallest value the schema permits instead.",
	},
	{
		fixture: "composition",
		path:    "annotations",
		reason:  constraintsAreSupported,
	},

	{
		fixture: "methods-and-refs",
		path:    "annotations",
		reason:  headerSchemasAreSupported,
	},
	{
		fixture: "parameters",
		path:    "annotations",
		reason:  headerSchemasAreSupported,
	},
	{
		fixture: "proof-of-concept",
		path:    "annotations",
		reason: headerSchemasAreSupported + " " + constraintsAreSupported +
			" Both apply to this fixture, so the difference shows as a count " +
			"rather than as any single message.",
	},
}

// headerSchemasAreSupported explains the Header Object annotations vertrag does
// not emit.
const headerSchemasAreSupported = "Dredd reports `schema` in a Header Object " +
	"as an unsupported key. vertrag decodes the header value to the type the " +
	"schema declares and validates it, which is a check neither Dredd nor " +
	"Gavel makes at all, so warning that the key does nothing would be false."

// falsyValuesAreStillValues explains the bodies vertrag emits where Dredd
// emits none.
const falsyValuesAreStillValues = "Dredd tests a generated value for " +
	"JavaScript truthiness before emitting a body, so a documented body of " +
	"false, null, 0 or the empty string produces no body at all. That is a " +
	"language's notion of emptiness leaking into a contract: false is a " +
	"perfectly good response, and as a REQUEST body the omission means sending " +
	"nothing to a server that requires one, which then answers 400 and is " +
	"reported as broken. Whether a specimen exists is already answered " +
	"separately, so there is nothing for truthiness to decide."

// constraintsAreSupported explains the annotations vertrag does not emit.
const constraintsAreSupported = "Dredd reports every JSON Schema constraint " +
	"beyond type and enum as an unsupported key, because its parser does not " +
	"act on them. vertrag passes them into the emitted schema, where they are " +
	"enforced against the response and drawn from during generation, so " +
	"warning that they do nothing would be false — and would invite deleting " +
	"the very constraint being checked."

// expectedDivergence finds a recorded entry for a difference.
func expectedDivergence(fixture, difference string) (divergence, bool) {
	for _, entry := range divergences {
		if entry.fixture == fixture && strings.HasPrefix(difference, entry.path+":") {
			return entry, true
		}
	}
	return divergence{}, false
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

	fixture := baseName(document)
	seen := map[string]bool{}

	for _, section := range []string{"transactions", "annotations"} {
		for _, diff := range diffValues(section, wantMap[section], gotMap[section]) {
			entry, intended := expectedDivergence(fixture, diff)
			if !intended {
				t.Errorf("%s", diff)
				continue
			}
			seen[entry.path] = true
			t.Logf("diverges from Dredd, as intended — %s\n  %s", entry.reason, diff)
		}
	}

	// A recorded divergence that no longer diverges means either Dredd changed
	// or vertrag did, and either way the ledger is now describing something
	// that is not happening.
	for _, entry := range divergences {
		if entry.fixture == fixture && !seen[entry.path] {
			t.Errorf("%s no longer differs from Dredd; remove it from the divergence ledger", entry.path)
		}
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
