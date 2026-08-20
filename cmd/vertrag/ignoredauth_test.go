package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `--check-ignored-auth` re-sends every authenticated request without the
// credential and reports each endpoint that answers anyway. At one project it
// found 56 of 117 endpoints answering unauthenticated — both rejection
// branches in their middleware had been commented out for months — and it was
// found only because somebody happened to mention that the flag existed.
//
// It stays off by default, because it doubles those requests. What these tests
// pin is that a run which could have used it says so, once, and that the
// silence everywhere else is worth something.

// authenticatedProject points a run at the corpus server with a credential
// configured, which is the only condition the notice turns on.
func authenticatedProject(t *testing.T, endpoint, description, extra string) string {
	t.Helper()
	dir := t.TempDir()
	settings := fmt.Sprintf("spec: %s\nendpoint: %s\n%s", description, endpoint, extra)
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

const ignoredAuthNotice = "nothing checked whether it mattered"

// TestAnAuthenticatedRunSaysWhatItDidNotCheck. A check nobody knows about is a
// check nobody runs, which is the failure this tool argues against everywhere
// else — so a run that carried a credential and never tested whether the
// credential mattered says so at the end, naming the flag and its cost.
func TestAnAuthenticatedRunSaysWhatItDidNotCheck(t *testing.T) {
	binary := build(t)
	endpoint, description := serve(t, "widgets")
	dir := authenticatedProject(t, endpoint, description, "auth:\n  header: 'X-API-Key: abc123'\n")

	output, _ := runIn(t, dir, binary, "run", "--no-color")

	if !strings.Contains(output, ignoredAuthNotice) {
		t.Errorf("an authenticated run was not told the check exists:\n%s", output)
	}
	for _, want := range []string{"--check-ignored-auth", "doubling"} {
		if !strings.Contains(output, want) {
			t.Errorf("the notice does not say %q, so it names neither the switch nor its cost:\n%s", want, output)
		}
	}
	// Once. A per-transaction version of this would bury the report it is
	// printed beside.
	if strings.Count(output, ignoredAuthNotice) != 1 {
		t.Errorf("the notice appeared %d times, want 1:\n%s", strings.Count(output, ignoredAuthNotice), output)
	}
}

// TestARunWithNoCredentialIsNotToldAboutTheCheck. With nothing configured to
// withhold there is nothing for the check to find, so mentioning it would be
// advice a reader cannot act on — and advice that arrives when it does not
// apply is what teaches people to stop reading the last line of a run.
func TestARunWithNoCredentialIsNotToldAboutTheCheck(t *testing.T) {
	binary := build(t)
	endpoint, description := serve(t, "widgets")

	output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color", description)
	if code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, output)
	}
	if strings.Contains(output, ignoredAuthNotice) {
		t.Errorf("a run with no credential was nagged about a check with nothing to check:\n%s", output)
	}
}

// TestTheNoticeIsSilentWhenTheCheckIsOn. The line reports an absence; a run
// that did the thing must not be told to do it.
func TestTheNoticeIsSilentWhenTheCheckIsOn(t *testing.T) {
	binary := build(t)
	endpoint, description := serve(t, "widgets")
	dir := authenticatedProject(t, endpoint, description, "auth:\n  header: 'X-API-Key: abc123'\n")

	output, _ := runIn(t, dir, binary, "run", "--no-color", "--check-ignored-auth")

	if strings.Contains(output, ignoredAuthNotice) {
		t.Errorf("a run that made the check was told to make it:\n%s", output)
	}
}
