// Package server starts the API under test and takes it down again.
//
// `server:` is a command line that brings the API up before the suite runs —
// `npm run test:api`, `docker compose up`, a start script. It is the one
// setting inherited from Dredd that is a feature rather than a compatibility
// shim, and it was accepted and ignored for long enough to be worth saying
// what that cost: a project relying on it ran against a server nobody had
// started, and got a wall of connection errors that named no cause.
//
// Shutdown is what this package is shaped around, because shutdown is what
// goes wrong. The command runs in a process group of its own and the GROUP is
// signalled, never the one child: `server: npm run test:api` is npm spawning
// node, and killing npm leaves node holding the port. A suite that leaves an
// orphaned server behind is a bug people hit daily and blame on something
// else — the next run fails to bind, or worse, passes against yesterday's
// build.
//
// It follows the shape of the hooks worker — Options, a Start that waits for
// the thing to be ready, a Stop that is safe to defer — but shares no code
// with it. What hooks does is start a program vertrag ships, on a protocol it
// controls, and wait for the line that program prints; none of that is
// available here, where the command is somebody else's and the only sign of
// life is the port.
//
// The process-group calls are POSIX; vertrag releases for darwin and linux.
package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Options is what starting the server under test takes.
type Options struct {
	// Command is the shell command line from `server:`.
	Command string
	// Endpoint is the base URL the suite is about to send to, and so the
	// address the wait polls.
	Endpoint string
	// Wait bounds the wait, from `server-wait`.
	Wait time.Duration
}

// Process is a started server command.
type Process struct {
	command string
	process *exec.Cmd
	output  *tail

	// exited is closed once the command has been reaped, and waitErr is what
	// reaping returned. Anything reading waitErr must receive from exited
	// first; that is what orders the two.
	exited  chan struct{}
	waitErr error

	// copied is closed once everything the command wrote has been read out of
	// the pipe. A command exiting does not mean its output has arrived, and
	// that output is the entire value of the messages below.
	copied chan struct{}

	// reader is the read end of the pipe the command writes to. The command's
	// output goes through a pipe we own rather than through exec's own
	// copying, because exec's Wait does not return until every writer to that
	// pipe has closed — and a server that spawns a child leaves the child
	// holding it. Waiting for the port to close is not what "has the process
	// exited" should mean.
	reader *os.File

	// endpoint is kept because it is asked about twice: once to wait for, and
	// again on the way out, to tell a command that backgrounded its server
	// from one that died and took the server with it.
	endpoint string

	once sync.Once
	note string
}

const (
	// pollEvery is how often the endpoint is tried while waiting. Short enough
	// that a server that comes up in 200ms is not made to look like one that
	// takes a second; long enough not to spin.
	pollEvery = 50 * time.Millisecond

	// dialFor bounds one attempt, so a port that blackholes packets cannot
	// swallow the whole wait in a single connect.
	dialFor = 250 * time.Millisecond

	// confirmOpen is how long an answering port must keep answering, with the
	// command still alive, before the wait believes it. One poll interval: long
	// enough for a command that is about to die of a typo to do so, short
	// enough to be invisible against starting a server.
	confirmOpen = pollEvery

	// detachGrace is how long polling carries on after the command itself has
	// returned, for the command that backgrounds what it started.
	detachGrace = time.Second

	// drainFor is how long a message waits for the command's output to finish
	// arriving. Bounded because a child of the command may still be holding
	// the pipe open, and a diagnostic that hangs is worse than one that is a
	// line short.
	drainFor = 200 * time.Millisecond

	// settleEvery is how often the process group is asked whether it has gone.
	// Shorter than pollEvery: this one is on the way out of every run, and a
	// server that stops in five milliseconds should not cost fifty.
	settleEvery = 10 * time.Millisecond

	// keptOutput is how much of what the server printed is kept. A server that
	// logs every request would otherwise grow without bound over a long run,
	// and the diagnostic value is all at the end anyway.
	keptOutput = 64 << 10
)

// terminateGrace is how long the process group is given to go down on SIGTERM
// before it is killed. A variable so the tests can shorten it; nothing else
// writes to it.
var terminateGrace = 5 * time.Second

// Start runs the command and waits for the endpoint to accept a connection.
//
// The wait polls rather than sleeping out `server-wait`. Sleeping is the crude
// version in both directions: a server that is up in 300ms costs the full 30
// seconds on every run, and a sleep that was tuned short produces exactly the
// wall of connection errors this feature exists to prevent. Polling makes
// `server-wait` a bound on how long a start may take rather than a claim about
// how long it does take, which is the only one of the two anybody can state
// correctly.
func Start(ctx context.Context, options Options) (*Process, error) {
	// `sh -c` because `server:` is a command line and not a program with
	// arguments: `npm run test:api:hub` is four words that mean one thing, and
	// people write pipes and `&&` in it. That trusts whoever wrote the config
	// file to the extent of running their shell command — the same trust
	// `hookfiles` already asks for, since a hook file is code this process
	// loads and runs.
	command := exec.Command("sh", "-c", options.Command)

	// Its own process group, so that stopping the server can signal everything
	// the command spawned rather than only the shell at the top of it.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("starting `%s`: %w", options.Command, err)
	}
	command.Stdout = writer
	command.Stderr = writer

	server := &Process{
		command:  options.Command,
		endpoint: options.Endpoint,
		process:  command,
		output:   &tail{limit: keptOutput},
		exited:   make(chan struct{}),
		copied:   make(chan struct{}),
		reader:   reader,
	}

	occupied := accepting(options.Endpoint)

	if err := command.Start(); err != nil {
		reader.Close()
		writer.Close()
		return nil, fmt.Errorf("starting `%s`: %w", options.Command, err)
	}
	// The parent's copy of the write end goes now, so the reader below sees
	// EOF when the command and its children are done with theirs.
	writer.Close()

	go func() {
		io.Copy(server.output, reader)
		close(server.copied)
	}()
	go func() {
		server.waitErr = command.Wait()
		close(server.exited)
	}()

	if err := server.waitUntilListening(ctx, options); err != nil {
		server.Stop()
		return nil, err
	}
	if occupied {
		// Said rather than refused: a port that was already open is usually a
		// server somebody left running, and this run has just proved nothing
		// about the build it thinks it tested. It is not refused because
		// "already up" is also a legitimate arrangement, and failing the run
		// would take that away.
		server.note = fmt.Sprintf(
			"something was already listening at %s before `%s` ran, so the wait proved nothing about it",
			options.Endpoint, options.Command)
	}
	return server, nil
}

// waitUntilListening polls the endpoint until it accepts, the command exits,
// the wait runs out, or the run is interrupted.
func (p *Process) waitUntilListening(ctx context.Context, options Options) error {
	address, err := listenAddress(options.Endpoint)
	if err != nil {
		// Nothing to poll. Sleeping out the wait is the crude version and the
		// only one left, so it is what an endpoint we cannot resolve to a
		// host and port gets — rather than starting the suite immediately,
		// which is the one behaviour guaranteed to be wrong.
		return p.sleep(ctx, options.Wait)
	}

	deadline := time.Now().Add(options.Wait)

	// exitedAt is when the command itself returned, which is not the same
	// thing as failing: `docker compose up -d` and `./start.sh &` both return
	// as soon as the server they started belongs to somebody else. So polling
	// continues for a moment afterwards — long enough for a server being
	// backgrounded to finish binding, and nowhere near long enough to sit out
	// a thirty-second `server-wait` over a command that died on a typo.
	var exitedAt time.Time

	// openedAt is when a port that was ALREADY occupied first answered, which
	// starts the short grace above rather than ending the wait.
	var openedAt time.Time

	for {
		// A dial that answers is not proof the command succeeded, so it is
		// not believed until the command has had a chance to fail.
		//
		// A port is a machine-wide resource. Anything may be holding this one —
		// another suite, a leftover process, a service somebody left up — and
		// returning success on a socket a stranger opened reports a command
		// that died on a typo as a healthy server. The run then tests the
		// stranger's process, with errors describing neither it nor the
		// failure.
		//
		// Checking the exit status at the instant of a successful dial does not
		// close it: the dial answers in microseconds and a command takes
		// milliseconds to die, so that race is lost more often than won. CI lost
		// it twice, on different runners, against a command whose entire body
		// was `exit 1`.
		//
		// So a first sighting only starts a short confirmation; success needs
		// the port open AND the command not having failed a moment later. One
		// poll interval is the whole cost, on a path that is about to wait for
		// an HTTP server anyway.
		if conn, err := net.DialTimeout("tcp", address, dialFor); err == nil {
			conn.Close()
			select {
			case <-p.exited:
				// Decisive either way: non-zero is a failed command whatever is
				// on the port, and zero is the backgrounding case — `docker
				// compose up -d` and `./start.sh &` both return as soon as the
				// server they started belongs to somebody else.
				if p.waitErr != nil {
					return fmt.Errorf(
						"the server command `%s` failed (%s) while something else was listening "+
							"at %s — so the run would have tested whatever that is%s",
						p.command, p.status(), options.Endpoint, p.printed())
				}
				return nil
			default:
			}
			if openedAt.IsZero() {
				openedAt = time.Now()
			} else if !time.Now().Before(openedAt.Add(confirmOpen)) {
				return nil
			}
		}

		if exitedAt.IsZero() {
			select {
			case <-p.exited:
				exitedAt = time.Now()
			default:
			}
		}
		if !exitedAt.IsZero() && !time.Now().Before(exitedAt.Add(detachGrace)) {
			return fmt.Errorf("the server command `%s` exited before anything listened at %s (%s)%s",
				p.command, options.Endpoint, p.status(), p.printed())
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the server command `%s` did not accept a connection at %s within %s (server-wait)%s",
				p.command, options.Endpoint, options.Wait, p.printed())
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the server: %w", ctx.Err())
		case <-time.After(pollEvery):
		}
	}
}

// sleep waits out the whole period, for the endpoint that cannot be polled.
func (p *Process) sleep(ctx context.Context, wait time.Duration) error {
	select {
	case <-p.exited:
		return fmt.Errorf("the server command `%s` exited while starting (%s)%s",
			p.command, p.status(), p.printed())
	case <-ctx.Done():
		return fmt.Errorf("waiting for the server: %w", ctx.Err())
	case <-time.After(wait):
		return nil
	}
}

// Stop takes the server down and reports anything worth saying about it.
//
// It is written to be deferred and called from anywhere: it may run twice, it
// may run before the suite sent a single request, and it must leave nothing
// behind on any of the paths out of a run — a clean pass, a failing one, a
// run cut short by --max-failures, a Ctrl-C, or a panic unwinding the stack.
func (p *Process) Stop() string {
	if p == nil {
		return ""
	}
	p.once.Do(p.stop)
	return p.note
}

func (p *Process) stop() {
	defer p.reader.Close()

	if p.process.Process == nil {
		return
	}

	// A command that has returned is not necessarily a command that failed:
	// `docker compose up -d`, and any start script ending in `&`, return as
	// soon as the server they started belongs to somebody else, and the port
	// stays up. So what is worth reporting is the command that went and took
	// the server with it — every connection error in the report above has
	// that one cause, and nothing else says so.
	select {
	case <-p.exited:
		if !accepting(p.endpoint) {
			p.note = fmt.Sprintf("the server command `%s` exited during the run (%s)%s",
				p.command, p.status(), p.printed())
		}
	default:
	}

	// Asked before anything is signalled, because a group that has already
	// emptied needs nothing — and because the pid this group is named after has
	// been reaped by then, so the kernel is free to hand the number to somebody
	// else. Signal 0 is the one question that cannot hurt whoever holds it now.
	if p.groupWentDown(0) {
		return
	}

	// SIGTERM first: a server asked to stop closes its listener, finishes what
	// it was answering and flushes whatever it writes at exit — coverage data,
	// in the projects most likely to be using this. The group is signalled
	// even when the command itself has already gone, because a start script
	// that backgrounds a server and exits leaves that server in the group it
	// was started in, as the only member left to kill.
	p.signalGroup(syscall.SIGTERM)
	if p.groupWentDown(terminateGrace) {
		return
	}

	p.signalGroup(syscall.SIGKILL)
	if !p.groupWentDown(terminateGrace) {
		p.note = fmt.Sprintf("the server command `%s` did not go down; a process may be left behind", p.command)
	}
}

// groupWentDown polls until nothing is left in the process group, or until the
// grace period runs out.
//
// Polling because a process group is not something a parent can wait on, and
// waiting on the direct child instead is precisely the mistake this package
// exists to avoid: `npm run test:api` exits the moment npm does, while node
// keeps the port. Signal 0 answers ESRCH once the group is empty, which is the
// only question being asked.
func (p *Process) groupWentDown(grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		if err := syscall.Kill(-p.process.Process.Pid, 0); err == syscall.ESRCH {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(settleEvery)
	}
}

// signalGroup signals the whole process group, which is what the negative pid
// means. Errors are ignored on purpose: every one of them is "nothing there",
// which is the outcome being asked for.
func (p *Process) signalGroup(signal syscall.Signal) {
	pid := p.process.Process.Pid
	if pid <= 0 {
		return
	}
	syscall.Kill(-pid, signal)
}

// status describes how the command ended. Only called after exited is closed.
func (p *Process) status() string {
	if p.waitErr == nil {
		return "exit status 0"
	}
	return p.waitErr.Error()
}

// printed is what the server said, for an error that would otherwise be a
// bare "it did not start" about a command whose own message says why.
func (p *Process) printed() string {
	select {
	case <-p.copied:
	case <-time.After(drainFor):
	}

	output := strings.TrimRight(p.output.String(), "\n")
	if output == "" {
		return "; it printed nothing"
	}
	return "; it printed:\n" + output
}

// accepting reports whether something is already answering at the endpoint.
func accepting(endpoint string) bool {
	address, err := listenAddress(endpoint)
	if err != nil {
		return false
	}
	conn, err := net.DialTimeout("tcp", address, dialFor)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// listenAddress is the host and port to poll, from the endpoint the suite will
// send to. A URL with no port means the scheme's own.
func listenAddress(endpoint string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("endpoint %q has no host to poll", endpoint)
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(host, port), nil
}

// tail keeps the last of what the server printed.
//
// The output is kept rather than streamed to the terminal: it is the whole
// diagnostic when the server fails to start, and pure noise when it starts
// fine — a server logging a line per request would interleave itself through
// the report, one line at a time, in the middle of the results somebody is
// reading. So it is held here and printed only where it explains something.
type tail struct {
	mu      sync.Mutex
	limit   int
	buf     []byte
	dropped bool
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
		t.dropped = true
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.dropped {
		return "[…earlier output dropped…]\n" + string(t.buf)
	}
	return string(t.buf)
}
