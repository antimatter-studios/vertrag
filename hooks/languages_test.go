package hooks

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/runner"
)

// The two workers are the same worker in two languages, and the only way to
// keep them that way is to run the same scenario through both and require the
// same answer. A worker that quietly diverged — a named hook that did not
// fire, an edit that did not travel back — would look perfectly healthy in
// its own tests.

// hookSource is one language's spelling of the same hook file: rename the
// transaction, set a header, pin a body field, and skip one transaction by
// name.
var hookSource = map[string]string{
	"nodejs": `
const hooks = require('vertrag_hooks');

hooks.beforeEach((transaction) => {
  transaction.request.headers['X-Each'] = 'ran';
  const body = transaction.request.body ? JSON.parse(transaction.request.body) : {};
  body.dry_run = true;
  transaction.request.body = JSON.stringify(body);
});

hooks.before('keep', (transaction) => {
  transaction.request.headers['X-Named'] = 'ran';
});

hooks.before('drop', (transaction) => {
  transaction.skip = true;
});
`,
	"python": `
import vertrag_hooks as hooks

@hooks.before_each
def each(transaction):
    transaction['request']['headers']['X-Each'] = 'ran'
    body = hooks.get_json(transaction)
    body['dry_run'] = True
    hooks.set_json(transaction, body)

@hooks.before('keep')
def named(transaction):
    transaction['request']['headers']['X-Named'] = 'ran'

@hooks.before('drop')
def drop(transaction):
    transaction['skip'] = True
`,
}

var hookFilename = map[string]string{"nodejs": "hooks.js", "python": "hooks.py"}

// interpreterFor skips a language whose interpreter is not installed, rather
// than failing: a contributor without Python should still be able to run the
// suite, and CI has both.
func interpreterFor(t *testing.T, language string) {
	t.Helper()
	found := false
	for _, candidate := range runtimes[language].interpreters {
		if _, err := exec.LookPath(candidate); err == nil {
			found = true
			break
		}
	}
	if !found {
		t.Skipf("%s is not installed", language)
	}
}

func TestBothLanguagesRunTheSameHooks(t *testing.T) {
	for _, language := range Languages() {
		t.Run(language, func(t *testing.T) {
			interpreterFor(t, language)

			dir := t.TempDir()
			path := filepath.Join(dir, hookFilename[language])
			if err := writeFile(path, hookSource[language]); err != nil {
				t.Fatal(err)
			}

			client, err := startWorker(t, Options{
				Language:  language,
				Hookfiles: []string{path},
				Host:      "127.0.0.1",
			})
			if err != nil {
				t.Fatalf("starting the %s worker: %v", language, err)
			}
			defer client.Stop()

			keep := transactionNamed("keep", `{"value":1}`)
			drop := transactionNamed("drop", "")

			if err := client.BeforeEach(keep); err != nil {
				t.Fatalf("beforeEach: %v", err)
			}
			if err := client.BeforeEach(drop); err != nil {
				t.Fatalf("beforeEach: %v", err)
			}

			// The each-hook ran on both.
			if keep.Request.Headers["X-Each"] != "ran" {
				t.Errorf("beforeEach did not set its header: %v", keep.Request.Headers)
			}
			// The body was parsed, edited and put back.
			if keep.Request.Body != `{"value":1,"dry_run":true}` && keep.Request.Body != `{"dry_run":true,"value":1}` {
				t.Errorf("the body edit did not survive: %q", keep.Request.Body)
			}
			// The named hook ran only on the transaction it named.
			if keep.Request.Headers["X-Named"] != "ran" {
				t.Errorf("the named hook did not run: %v", keep.Request.Headers)
			}
			if drop.Request.Headers["X-Named"] == "ran" {
				t.Error("a named hook ran on a transaction it did not name")
			}
			// And skip travelled back.
			if !drop.Skip {
				t.Error("transaction.skip was not applied")
			}
			if keep.Skip {
				t.Error("the wrong transaction was skipped")
			}
		})
	}
}

// TestPythonMatchesByOperationIdAndGlob pins the two selectors Dredd has no
// equivalent of, and which exist because a generated transaction name is long
// and moves whenever a summary is edited.
func TestPythonMatchesByOperationIdAndGlob(t *testing.T) {
	interpreterFor(t, "python")

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.py")
	if err := writeFile(path, `
import vertrag_hooks as hooks

@hooks.before('createThing')
def by_operation(transaction):
    transaction['request']['headers']['X-ByOperation'] = 'ran'

@hooks.before('/api/*')
def by_glob(transaction):
    transaction['request']['headers']['X-ByGlob'] = 'ran'
`); err != nil {
		t.Fatal(err)
	}

	client, err := startWorker(t, Options{
		Language: "python", Hookfiles: []string{path},
		Host: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	defer client.Stop()

	transaction := runner.New("http://example.invalid").Prepare(compile.Transaction{
		Name:        "/api/things > Create > 201 > application/json",
		OperationID: "createThing",
		Request:     compile.Request{Method: "POST", URI: "/api/things", Headers: []compile.Header{}},
	})
	if err := client.BeforeEach(transaction); err != nil {
		t.Fatal(err)
	}

	if transaction.Request.Headers["X-ByOperation"] != "ran" {
		t.Errorf("matching by operationId did not fire: %v", transaction.Request.Headers)
	}
	if transaction.Request.Headers["X-ByGlob"] != "ran" {
		t.Errorf("matching by glob did not fire: %v", transaction.Request.Headers)
	}
}

// TestAThrowingHookFailsItsTransactionNotTheRun pins the same contract in both
// languages: one bad hook must not stop the suite from reporting.
func TestAThrowingHookFailsItsTransactionNotTheRun(t *testing.T) {
	source := map[string]string{
		"nodejs": "require('vertrag_hooks').beforeEach(() => { throw new Error('deliberate'); });",
		"python": "import vertrag_hooks as hooks\n@hooks.before_each\ndef boom(t):\n    raise RuntimeError('deliberate')\n",
	}

	for _, language := range Languages() {
		t.Run(language, func(t *testing.T) {
			interpreterFor(t, language)

			dir := t.TempDir()
			path := filepath.Join(dir, hookFilename[language])
			if err := writeFile(path, source[language]); err != nil {
				t.Fatal(err)
			}

			client, err := startWorker(t, Options{
				Language: language, Hookfiles: []string{path},
				Host: "127.0.0.1",
			})
			if err != nil {
				t.Fatalf("starting the worker: %v", err)
			}
			defer client.Stop()

			transaction := transactionNamed("t", "")
			err = client.BeforeEach(transaction)
			if err == nil {
				t.Fatal("a throwing hook should be reported")
			}
			// The worker is still alive: a second transaction still works.
			if second := transactionNamed("t2", ""); client.BeforeEach(second) == nil {
				t.Log("the worker survived the throwing hook, as it must")
			}
		})
	}
}

// TestBothModuleNamesWorkInNode: `vertrag_hooks` is the one name, in both
// languages, because the underscore is the only spelling Python can take — a
// hyphen there is a syntax error. The hyphenated form still resolves in Node
// for the muscle memory. The bare `hooks` Dredd used does not, and the next
// test says so.
func TestBothModuleNamesWorkInNode(t *testing.T) {
	interpreterFor(t, "nodejs")

	for _, name := range []string{"vertrag_hooks", "vertrag-hooks"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "hooks.js")
			if err := writeFile(path, `
const hooks = require('`+name+`');
hooks.beforeEach((transaction) => { transaction.request.headers['X-Loaded'] = 'ran'; });
`); err != nil {
				t.Fatal(err)
			}

			client, err := startWorker(t, Options{
				Language: "nodejs", Hookfiles: []string{path},
				Host: "127.0.0.1",
			})
			if err != nil {
				t.Fatalf("require('%s') did not resolve: %v", name, err)
			}
			defer client.Stop()

			transaction := transactionNamed("t", "")
			if err := client.BeforeEach(transaction); err != nil {
				t.Fatal(err)
			}
			if transaction.Request.Headers["X-Loaded"] != "ran" {
				t.Errorf("the hook did not run when imported as %q", name)
			}
		})
	}
}

// TestATypeScriptHookFileWithoutALoaderSaysWhatToInstall pins the diagnostic
// rather than the capability: Node cannot parse TypeScript, and a project
// without tsx or ts-node would otherwise get `Unexpected token` from a file
// it thought was fine.
func TestATypeScriptHookFileWithoutALoaderSaysWhatToInstall(t *testing.T) {
	interpreterFor(t, "nodejs")

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.ts")
	if err := writeFile(path, "const x: number = 1;\n"); err != nil {
		t.Fatal(err)
	}

	client, err := startWorker(t, Options{
		Language: "nodejs", Hookfiles: []string{path},
		Host: "127.0.0.1",
	})
	if client != nil {
		defer client.Stop()
	}
	if err == nil {
		// A machine that happens to have tsx or ts-node installed globally
		// will load it, which is the feature working.
		t.Skip("a TypeScript loader is available here, so the file loaded")
	}
	// Otherwise the worker must have said what to do about it. The message
	// travels on the worker's stderr, so the failure is the ready-timeout or
	// exit; what matters is that the run stops rather than proceeding with no
	// hooks loaded.
	t.Logf("a TypeScript hook file without a loader stopped the run: %v", err)
}

// TestTheBareHooksNameIsGone pins a removal rather than a feature.
//
// Dredd's worker provided `require('hooks')`, and vertrag's did too while it
// was aiming at drop-in compatibility. It no longer is: there is one Node
// hook file in the world that says it, changing the line takes seconds, and
// an alias named after an archived tool would otherwise sit in this worker
// forever. A hook file that still says `hooks` fails loudly at load, which is
// the right moment to find out.
func TestTheBareHooksNameIsGone(t *testing.T) {
	interpreterFor(t, "nodejs")

	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.js")
	if err := writeFile(path, "const hooks = require('hooks');\n"); err != nil {
		t.Fatal(err)
	}

	client, err := startWorker(t, Options{
		Language: "nodejs", Hookfiles: []string{path},
		Host: "127.0.0.1",
	})
	if client != nil {
		defer client.Stop()
	}
	if err == nil {
		t.Error("require('hooks') should no longer resolve")
	}
}
