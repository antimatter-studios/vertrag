package main

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/corpus"
)

// build compiles vertrag once, so the tests below exercise the command a user
// actually runs rather than the functions behind it.
//
// The distinction has already mattered: a package can be entirely correct while
// the flag that reaches it is misspelled, unwired, or parsed into the wrong
// variable, and every unit test in the tree stays green.
func build(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a binary")
	}

	binary := filepath.Join(t.TempDir(), "vertrag")
	out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("building vertrag: %v\n%s", err, out)
	}
	return binary
}

// serve stands up a corpus server and writes its description to a file, which
// is what the command takes.
func serve(t *testing.T, name string, faults ...corpus.Fault) (endpoint, description string) {
	t.Helper()

	server, err := corpus.NewNamed(name, faults...)
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}
	http := httptest.NewServer(server.Handler())
	t.Cleanup(http.Close)

	source, err := corpus.Load(name)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	path := filepath.Join(t.TempDir(), name+".yml")
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatalf("writing the description: %v", err)
	}
	return http.URL, path
}

func runCommand(t *testing.T, binary string, args ...string) (output string, code int) {
	t.Helper()

	command := exec.Command(binary, args...)
	out, err := command.CombinedOutput()
	if exit, isExit := err.(*exec.ExitError); isExit {
		return string(out), exit.ExitCode()
	}
	if err != nil {
		t.Fatalf("running vertrag: %v\n%s", err, out)
	}
	return string(out), 0
}

// TestExitCodeReportsTheOutcome pins the only thing CI reads.
//
// A tester whose exit code does not follow its findings is worse than no
// tester: a green pipeline over a broken API is a claim nobody checks. It is
// also exactly the sort of thing that survives every unit test, because the
// packages are right and only the wiring is wrong.
func TestExitCodeReportsTheOutcome(t *testing.T) {
	binary := build(t)

	t.Run("a conforming server exits zero", func(t *testing.T) {
		endpoint, description := serve(t, "widgets")
		output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color", description)
		if code != 0 {
			t.Errorf("exit = %d, want 0\n%s", code, output)
		}
	})

	t.Run("a faulty server exits non-zero", func(t *testing.T) {
		endpoint, description := serve(t, "widgets", corpus.FaultWrongStatus)
		output, code := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color", description)
		if code == 0 {
			t.Errorf("exit = 0 for a server returning 500s; CI would call this a pass\n%s", output)
		}
	})

	t.Run("an unreachable endpoint exits non-zero", func(t *testing.T) {
		_, description := serve(t, "widgets")
		// A port nothing is listening on. Reported as an error rather than a
		// failure, but it must not look like success.
		output, code := runCommand(t, binary,
			"run", "--endpoint", "http://127.0.0.1:1", "--no-color", description)
		if code == 0 {
			t.Errorf("exit = 0 with nothing listening\n%s", output)
		}
	})

	t.Run("a description that cannot be read exits non-zero", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "broken.yml")
		os.WriteFile(path, []byte("{{{ not yaml"), 0o600)

		output, code := runCommand(t, binary, "run", "--endpoint", "http://127.0.0.1:1", "--no-color", path)
		if code == 0 {
			t.Errorf("exit = 0 for an unreadable description\n%s", output)
		}
	})
}

// TestEveryReporterWritesToAFile pins the flag pairing a CI job depends on:
// a report on disk for the pipeline to collect, and the terminal left alone.
func TestEveryReporterWritesToAFile(t *testing.T) {
	binary := build(t)
	endpoint, description := serve(t, "widgets", corpus.FaultWrongStatus)

	for _, format := range []string{"cli", "dot", "markdown", "html", "junit"} {
		t.Run(format, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "report."+format)

			_, code := runCommand(t, binary, "run",
				"--endpoint", endpoint, "--no-color",
				"--reporter", format, "--output", path, description)
			if code == 0 {
				t.Error("a faulty server should still exit non-zero when reporting to a file")
			}

			written, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("no report written: %v", err)
			}
			if len(written) == 0 {
				t.Error("the report is empty")
			}
		})
	}
}

// TestFiltersNarrowTheRun pins --only and --method, which are how a large suite
// is cut down to the operation someone is working on.
func TestFiltersNarrowTheRun(t *testing.T) {
	binary := build(t)
	endpoint, description := serve(t, "widgets")

	full, _ := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color", "--dry-run", description)
	filtered, _ := runCommand(t, binary, "run", "--endpoint", endpoint, "--no-color", "--dry-run",
		"--method", "POST", description)

	fullCount := strings.Count(full, "skip:")
	filteredCount := strings.Count(filtered, "skip:")

	if fullCount == 0 {
		t.Fatalf("the unfiltered dry run listed nothing:\n%s", full)
	}
	if filteredCount == 0 {
		t.Errorf("--method POST filtered everything away:\n%s", filtered)
	}
	if filteredCount >= fullCount {
		t.Errorf("--method POST listed %d of %d transactions, so it narrowed nothing",
			filteredCount, fullCount)
	}
}

// TestDryRunSendsNothing pins that --dry-run is safe to point at anything.
//
// Its whole purpose is answering "what would this do" without doing it, and a
// dry run that contacts the server would be a trap: the endpoint given is
// frequently the one someone is not sure about.
func TestDryRunSendsNothing(t *testing.T) {
	binary := build(t)
	_, description := serve(t, "widgets")

	// Nothing is listening. A dry run must not care.
	output, code := runCommand(t, binary, "run",
		"--endpoint", "http://127.0.0.1:1", "--no-color", "--dry-run", description)
	if code != 0 {
		t.Errorf("exit = %d against a dead endpoint; a dry run should not have tried\n%s", code, output)
	}
	if !strings.Contains(output, "dry run") {
		t.Errorf("output does not say it was a dry run:\n%s", output)
	}
}

// TestAConfigFileDrivesAWholeRun pins the path a project actually uses.
//
// A configured project runs `vertrag run` with no arguments, so everything —
// which description, which endpoint, which reporters, where each writes — comes
// from the file. Every flag being correct says nothing about that: the file is
// read by different code, and a key that silently fails to reach its setting
// looks exactly like a key that worked.
//
// The exit code is checked with several reporters configured at once, because
// that is where an aggregation mistake would hide: one reporter disagreeing
// about whether a run passed would fail every green build, and a single-reporter
// test cannot see it.
func TestAConfigFileDrivesAWholeRun(t *testing.T) {
	binary := build(t)

	write := func(t *testing.T, directory, endpoint string) {
		t.Helper()

		source, err := corpus.Load("widgets")
		if err != nil {
			t.Fatalf("loading: %v", err)
		}
		if err := os.WriteFile(filepath.Join(directory, "api.yml"), source, 0o600); err != nil {
			t.Fatalf("writing the description: %v", err)
		}

		config := "blueprint: ./api.yml\n" +
			"endpoint: " + endpoint + "\n" +
			"color: false\n" +
			"reporter: [cli, junit, html]\n" +
			`output: ["", report.xml, report.html]` + "\n"
		if err := os.WriteFile(filepath.Join(directory, "vertrag.yml"), []byte(config), 0o600); err != nil {
			t.Fatalf("writing the config: %v", err)
		}
	}

	runIn := func(t *testing.T, directory string) (string, int) {
		t.Helper()

		command := exec.Command(binary, "run")
		command.Dir = directory
		out, err := command.CombinedOutput()
		if exit, isExit := err.(*exec.ExitError); isExit {
			return string(out), exit.ExitCode()
		}
		if err != nil {
			t.Fatalf("running: %v\n%s", err, out)
		}
		return string(out), 0
	}

	t.Run("a conforming server exits zero and writes every report", func(t *testing.T) {
		endpoint, _ := serve(t, "widgets")
		directory := t.TempDir()
		write(t, directory, endpoint)

		output, code := runIn(t, directory)
		if code != 0 {
			t.Errorf("exit = %d for a conforming server; every green build would fail\n%s", code, output)
		}
		if !strings.Contains(output, "passing") {
			t.Errorf("the terminal reporter wrote nothing:\n%s", output)
		}

		// The empty output entry pairs the cli reporter with the terminal, and
		// the other two with files. A mis-paired list would send a report to
		// the wrong place or to none.
		for _, name := range []string{"report.xml", "report.html"} {
			written, err := os.ReadFile(filepath.Join(directory, name))
			if err != nil || len(written) == 0 {
				t.Errorf("%s was not written: %v", name, err)
			}
		}
	})

	t.Run("a faulty server exits non-zero", func(t *testing.T) {
		endpoint, _ := serve(t, "widgets", corpus.FaultWrongStatus)
		directory := t.TempDir()
		write(t, directory, endpoint)

		output, code := runIn(t, directory)
		if code == 0 {
			t.Errorf("exit = 0 for a server returning 500s\n%s", output)
		}
	})
}
