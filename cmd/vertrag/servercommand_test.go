package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/corpus"
)

// TestMain doubles as the server a `server:` command starts.
//
// These tests are about what happens to a real process around a real run, so a
// real process is what they use: re-entering the test binary in a serving mode
// gives one that answers a corpus description, without adding a fixture
// program to the tree or making the suite depend on node being installed.
func TestMain(m *testing.M) {
	if spec := os.Getenv("VERTRAG_TEST_SERVE"); spec != "" {
		serveCorpus(spec)
	}
	os.Exit(m.Run())
}

// serveCorpus answers a corpus description until it is killed. The argument is
// "<description>|<address>|<pid file>|<fault>|<pause>", and the last two are
// optional. The pause is how long each answer is held back, which is how a
// test can be sure a run is still in flight when it interrupts it.
func serveCorpus(spec string) {
	parts := strings.Split(spec, "|")
	if len(parts) < 3 {
		fmt.Fprintf(os.Stderr, "test server: malformed spec %q\n", spec)
		os.Exit(1)
	}

	var faults []corpus.Fault
	if len(parts) > 3 && parts[3] != "" {
		faults = append(faults, corpus.Fault(parts[3]))
	}

	var pause time.Duration
	if len(parts) > 4 && parts[4] != "" {
		var err error
		if pause, err = time.ParseDuration(parts[4]); err != nil {
			fmt.Fprintf(os.Stderr, "test server: %v\n", err)
			os.Exit(1)
		}
	}

	served, err := corpus.NewNamed(parts[0], faults...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "test server: %v\n", err)
		os.Exit(1)
	}

	listener, err := net.Listen("tcp", parts[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "test server: %v\n", err)
		os.Exit(1)
	}
	// Written after the port is held, so a test that has read the file knows
	// the server is up as well as which process is holding it.
	if err := os.WriteFile(parts[2], []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "test server: %v\n", err)
		os.Exit(1)
	}

	handler := served.Handler()
	http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(pause)
		handler.ServeHTTP(w, r)
	}))
	os.Exit(1)
}

// startedServer writes a config whose `server:` starts the corpus server in a
// subprocess, and returns the config path and the file that process writes its
// pid to.
//
// The command backgrounds the server and then waits, which is the shape of
// every real one: `npm run test:api` is npm spawning node, so what has to die
// at the end of the run is a process the command started rather than the
// command itself.
func startedServer(t *testing.T, fault corpus.Fault, pause, extra string) (configPath, pidFile string) {
	t.Helper()

	dir := t.TempDir()
	description := filepath.Join(dir, "widgets.yml")
	source, err := corpus.Load("widgets")
	if err != nil {
		t.Fatalf("loading the description: %v", err)
	}
	if err := os.WriteFile(description, source, 0o600); err != nil {
		t.Fatalf("writing the description: %v", err)
	}

	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("finding the test binary: %v", err)
	}

	address := reservedAddress(t)
	pidFile = filepath.Join(dir, "server.pid")
	command := fmt.Sprintf("VERTRAG_TEST_SERVE='widgets|%s|%s|%s|%s' '%s' & sleep 60",
		address, pidFile, fault, pause, binary)

	configPath = filepath.Join(dir, "vertrag.yml")
	contents := fmt.Sprintf("spec: %s\nendpoint: http://%s\nserver: \"%s\"\nserver-wait: 10\n%s",
		description, address, command, extra)
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	return configPath, pidFile
}

// reservedAddress is a port nothing is listening on.
func reservedAddress(t *testing.T) string {
	t.Helper()
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer held.Close()
	return held.Addr().String()
}

// stopped asserts that the process the run started is no longer there.
//
// This is the assertion the whole feature turns on. A suite that leaves its
// server running is not a cosmetic problem: the next run cannot bind the port,
// and the error it prints is about the port rather than about the run that
// never let go of it.
func stopped(t *testing.T, pidFile string) {
	t.Helper()

	pid := readPID(t, pidFile)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		if !time.Now().Before(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the server (pid %d) was still running after the run finished", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for {
		contents, err := os.ReadFile(path)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(contents))); err == nil && pid > 0 {
				return pid
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("nothing ever wrote a pid to %s, so the server never started", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestTheServerCommandIsStartedAndStoppedAroundARun is the whole feature in
// one test: nothing is listening when the run begins, the run passes anyway
// because `server:` brought the API up, and nothing is listening when it ends.
func TestTheServerCommandIsStartedAndStoppedAroundARun(t *testing.T) {
	config, pidFile := startedServer(t, "", "", "reporter: [dot]\n")

	if err := runRun([]string{"--config", config, "--no-color"}); err != nil {
		t.Fatalf("run against a server vertrag started: %v", err)
	}
	stopped(t, pidFile)
}

// TestTheServerIsStoppedWhenTheRunFails is the path that matters more, because
// it is the one a suite takes on the days people are watching: a failing run
// leaves through a different return and must still take the server with it.
//
// --max-failures is here as its own case for the same reason. It stops the run
// partway through, and "partway through" is where an early return that forgets
// to clean up would live.
func TestTheServerIsStoppedWhenTheRunFails(t *testing.T) {
	for _, test := range []struct {
		name  string
		extra string
	}{
		{"a failing run", "reporter: [dot]\n"},
		{"a run cut short by max-failures", "reporter: [dot]\nmax-failures: 1\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, pidFile := startedServer(t, corpus.FaultWrongStatus, "", test.extra)

			if err := runRun([]string{"--config", config, "--no-color"}); err != errFailed {
				t.Fatalf("err = %v, want the run to have failed", err)
			}
			stopped(t, pidFile)
		})
	}
}

// TestAServerThatFailsToStartIsReportedByName pins the diagnostic this feature
// was built for. A command that dies on a missing module used to become a
// screenful of connection errors, one per transaction, none of which mentioned
// the server, the command, or the reason.
func TestAServerThatFailsToStartIsReportedByName(t *testing.T) {
	dir := t.TempDir()
	description := filepath.Join(dir, "widgets.yml")
	source, err := corpus.Load("widgets")
	if err != nil {
		t.Fatalf("loading the description: %v", err)
	}
	if err := os.WriteFile(description, source, 0o600); err != nil {
		t.Fatalf("writing the description: %v", err)
	}

	config := filepath.Join(dir, "vertrag.yml")
	contents := fmt.Sprintf("spec: %s\nendpoint: http://%s\n"+
		"server: \"echo 'Error: Cannot find module ./app.js' >&2; exit 1\"\nserver-wait: 5\n",
		description, reservedAddress(t))
	if err := os.WriteFile(config, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing the config: %v", err)
	}

	err = runRun([]string{"--config", config, "--no-color"})
	if err == nil {
		t.Fatal("a run whose server never started reported success")
	}
	if !strings.Contains(err.Error(), "Cannot find module") {
		t.Errorf("the error does not carry what the command printed: %v", err)
	}
	if !strings.Contains(err.Error(), "app.js") {
		t.Errorf("the error does not name the command that failed: %v", err)
	}
}

// TestTheServerIsStoppedOnCtrlC is the path a defer alone does not obviously
// cover, and the one people meet most often: a run interrupted halfway through
// must not leave the server it started behind. It drives the built binary
// rather than the function, because the signal handling being tested is the
// process's.
func TestTheServerIsStoppedOnCtrlC(t *testing.T) {
	binary := build(t)

	// The server holds every answer back for a good while, so the run is
	// certainly still in flight when the signal arrives. An earlier version of
	// this test paced the run with `transport.delay` and raced it: the whole
	// run finished in 30ms, the interrupt landed on a process that had already
	// exited normally, and the test passed while proving nothing.
	config, pidFile := startedServer(t, "", "10s", "reporter: [dot]\n")

	vertrag := exec.Command(binary, "run", "--config", config, "--no-color")
	if err := vertrag.Start(); err != nil {
		t.Fatalf("starting vertrag: %v", err)
	}

	// The pid file is written once the server holds the port, so reading it is
	// how the test knows the run has got as far as sending.
	pid := readPID(t, pidFile)

	done := make(chan error, 1)
	go func() { done <- vertrag.Wait() }()

	// Not yet finished, which is what makes this an interrupted run rather
	// than a completed one.
	select {
	case err := <-done:
		t.Fatalf("the run finished before it could be interrupted: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	if err := vertrag.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupting vertrag: %v", err)
	}

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		vertrag.Process.Kill()
		t.Fatal("vertrag did not exit after an interrupt")
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		if !time.Now().Before(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the server (pid %d) outlived the interrupted run", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
