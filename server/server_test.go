package server

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain doubles as the server under test.
//
// What these tests need is a real process holding a real port, started from a
// shell command line the way `server:` is, and re-entering the test binary is
// how to have one without depending on node, python or nc being installed
// wherever the suite runs. A shutdown test whose evidence is a mock proves
// nothing about process groups.
func TestMain(m *testing.M) {
	if address := os.Getenv("VERTRAG_TEST_LISTEN"); address != "" {
		listenForever(address)
	}
	os.Exit(m.Run())
}

func listenForever(address string) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		fmt.Fprintf(os.Stderr, "test listener: %v\n", err)
		os.Exit(1)
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			os.Exit(1)
		}
		conn.Close()
	}
}

// listener is a shell fragment that runs the test binary as a listener.
func listener(t *testing.T, address string) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("finding the test binary: %v", err)
	}
	return "VERTRAG_TEST_LISTEN=" + address + " '" + binary + "'"
}

// freeAddress is a port nothing is listening on, and the endpoint that
// addresses it.
func freeAddress(t *testing.T) (address, endpoint string) {
	t.Helper()
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	address = held.Addr().String()
	held.Close()
	return address, "http://" + address
}

// gone reports whether a pid has left, polling because a signalled process
// takes a moment to go.
func gone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// free reports whether the address can be listened on again, which is the
// symptom that actually costs people a day: the next run cannot bind, and the
// error names the port rather than the suite that never let go of it.
func free(t *testing.T, address string) bool {
	t.Helper()
	held, err := net.Listen("tcp", address)
	if err != nil {
		return false
	}
	held.Close()
	return true
}

// TestTheEndpointIsPolledRatherThanSleptOn pins the choice between the two
// ways of honouring `server-wait`. A fixed sleep would make this run take the
// full ten seconds, which is what every run of a project with `server-wait: 30`
// used to cost under Dredd.
func TestTheEndpointIsPolledRatherThanSleptOn(t *testing.T) {
	address, endpoint := freeAddress(t)

	started := time.Now()
	process, err := Start(context.Background(), Options{
		Command:  "sleep 0.2; " + listener(t, address),
		Endpoint: endpoint,
		Wait:     10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer process.Stop()

	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("waited %s for a server that was up in 200ms; the wait is not polling", took)
	}
	if free(t, address) {
		t.Error("Start returned before anything was listening")
	}
}

// TestAServerThatNeverStartsIsReportedWithItsOutput is the failure this whole
// feature exists to replace: a command that dies on a missing module used to
// become a wall of connection errors naming no cause.
func TestAServerThatNeverStartsIsReportedWithItsOutput(t *testing.T) {
	_, endpoint := freeAddress(t)

	_, err := Start(context.Background(), Options{
		Command:  `echo "Cannot find module ./app.js" >&2; exit 1`,
		Endpoint: endpoint,
		Wait:     5 * time.Second,
	})
	if err == nil {
		t.Fatal("a command that exits 1 started successfully")
	}
	if !strings.Contains(err.Error(), "Cannot find module ./app.js") {
		t.Errorf("the error does not carry what the command printed: %v", err)
	}
	if !strings.Contains(err.Error(), "app.js") || !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("the error does not name the command and how it ended: %v", err)
	}
}

// TestTheWaitIsBoundedByServerWait pins that a server that never listens is
// given up on, and that giving up does not leave it running.
func TestTheWaitIsBoundedByServerWait(t *testing.T) {
	_, endpoint := freeAddress(t)

	started := time.Now()
	_, err := Start(context.Background(), Options{
		Command:  "sleep 60",
		Endpoint: endpoint,
		Wait:     300 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("a command that never listens started successfully")
	}
	if !strings.Contains(err.Error(), "server-wait") {
		t.Errorf("the error does not say which setting bounded it: %v", err)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("a 300ms server-wait took %s", took)
	}
}

// TestTheProcessGroupIsStoppedNotJustTheCommand is the one that matters.
//
// `server: npm run test:api` is npm spawning node: killing the command leaves
// the server holding the port, the next run cannot bind, and nobody connects
// that to the test suite. The command here is the same shape — a shell that
// outlives a listener it backgrounded — and the assertion is that the
// listener, not the shell, is gone.
func TestTheProcessGroupIsStoppedNotJustTheCommand(t *testing.T) {
	address, endpoint := freeAddress(t)
	pidFile := filepath.Join(t.TempDir(), "listener.pid")

	process, err := Start(context.Background(), Options{
		Command:  listener(t, address) + ` & echo $! > ` + pidFile + `; sleep 60`,
		Endpoint: endpoint,
		Wait:     10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	child := readPID(t, pidFile)
	process.Stop()

	if !gone(child, 5*time.Second) {
		syscall.Kill(child, syscall.SIGKILL)
		t.Errorf("the listener (pid %d) survived Stop; only the command it was spawned from was killed", child)
	}
	if !free(t, address) {
		t.Errorf("%s is still held after Stop", address)
	}
}

// TestAServerThatIgnoresSIGTERMIsKilled pins the second half of the shutdown:
// asking politely is right, and being ignored cannot be the end of it, or a
// server with a signal handler it never finishes outlives every run.
func TestAServerThatIgnoresSIGTERMIsKilled(t *testing.T) {
	restore := terminateGrace
	terminateGrace = 300 * time.Millisecond
	defer func() { terminateGrace = restore }()

	address, endpoint := freeAddress(t)
	pidFile := filepath.Join(t.TempDir(), "listener.pid")

	process, err := Start(context.Background(), Options{
		Command:  `trap "" TERM; ` + listener(t, address) + ` & echo $! > ` + pidFile + `; wait`,
		Endpoint: endpoint,
		Wait:     10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	child := readPID(t, pidFile)
	process.Stop()

	if !gone(child, 5*time.Second) {
		syscall.Kill(child, syscall.SIGKILL)
		t.Fatalf("a server ignoring SIGTERM (pid %d) was never killed", child)
	}
	if !free(t, address) {
		t.Errorf("%s is still held after Stop", address)
	}
}

// TestAServerThatBackgroundsItselfIsStillStopped covers `docker compose up -d`
// and every `start.sh` that ends in `&`: the command returns, the server does
// not, and both facts have to be handled. Refusing to start would break the
// arrangement; forgetting about it would leave the server running.
func TestAServerThatBackgroundsItselfIsStillStopped(t *testing.T) {
	address, endpoint := freeAddress(t)
	pidFile := filepath.Join(t.TempDir(), "listener.pid")

	process, err := Start(context.Background(), Options{
		Command:  listener(t, address) + ` & echo $! > ` + pidFile,
		Endpoint: endpoint,
		Wait:     10 * time.Second,
	})
	if err != nil {
		t.Fatalf("a command that backgrounds its server was refused: %v", err)
	}

	child := readPID(t, pidFile)
	if note := process.Stop(); note != "" {
		t.Errorf("a command that backgrounded its server was reported as having died: %s", note)
	}
	if !gone(child, 5*time.Second) {
		syscall.Kill(child, syscall.SIGKILL)
		t.Errorf("the backgrounded listener (pid %d) survived Stop", child)
	}
}

// TestAServerThatDiesDuringTheRunIsReported: every failure in the report above
// has one cause, and the report is not the place that says so.
func TestAServerThatDiesDuringTheRunIsReported(t *testing.T) {
	address, endpoint := freeAddress(t)

	process, err := Start(context.Background(), Options{
		Command:  listener(t, address) + ` & sleep 0.2; kill $!; echo "out of memory" >&2; exit 7`,
		Endpoint: endpoint,
		Wait:     10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Long enough for the command to have died, as it would partway through a
	// run of any size.
	time.Sleep(700 * time.Millisecond)

	note := process.Stop()
	if !strings.Contains(note, "exited during the run") {
		t.Errorf("a server that died mid-run was not reported: %q", note)
	}
	if !strings.Contains(note, "out of memory") {
		t.Errorf("the note does not carry what the server printed: %q", note)
	}
}

// TestStopIsSafeToCallTwice: it is deferred, and the paths out of a run are
// not all mutually exclusive — Start stops what it could not wait for, and the
// defer then runs anyway.
func TestStopIsSafeToCallTwice(t *testing.T) {
	address, endpoint := freeAddress(t)

	process, err := Start(context.Background(), Options{
		Command:  listener(t, address),
		Endpoint: endpoint,
		Wait:     10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	first := process.Stop()
	second := process.Stop()
	if first != second {
		t.Errorf("Stop said %q and then %q", first, second)
	}
	if !free(t, address) {
		t.Errorf("%s is still held after Stop", address)
	}
}

// TestAnInterruptedWaitStopsTheServer is the Ctrl-C path: the run is cancelled
// while the server is still coming up, and what has been started so far still
// has to go.
func TestAnInterruptedWaitStopsTheServer(t *testing.T) {
	address, endpoint := freeAddress(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	_, err := Start(ctx, Options{
		// Listens far too late to be waited for, and would hold the port for
		// a minute if nothing stopped it.
		Command:  "sleep 60; " + listener(t, address),
		Endpoint: endpoint,
		Wait:     60 * time.Second,
	})
	defer cancel()

	if err == nil {
		t.Fatal("Start returned successfully after the run was cancelled")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("the error does not say the wait was interrupted: %v", err)
	}
	if !free(t, address) {
		t.Errorf("%s is still held after an interrupted start", address)
	}
}

// TestAPortThatWasAlreadyOpenIsReported. Polling a port cannot tell the server
// this run started from one somebody left running yesterday, so the one thing
// it can do is say what it saw. Refusing would break a legitimate arrangement;
// silence would let a run pass against a build nobody tested.
func TestAPortThatWasAlreadyOpenIsReported(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer held.Close()
	endpoint := "http://" + held.Addr().String()

	process, err := Start(context.Background(), Options{
		Command:  "sleep 60",
		Endpoint: endpoint,
		Wait:     5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	note := process.Stop()
	if !strings.Contains(note, "already listening") {
		t.Errorf("a port that was open before the command ran was not reported: %q", note)
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		contents, err := os.ReadFile(path)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(contents))); err == nil && pid > 0 {
				return pid
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("no pid was written to %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
