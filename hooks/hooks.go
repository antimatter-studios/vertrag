// Package hooks runs a project's hook files and lets them rewrite transactions.
//
// Hooks are how a suite does what an API description cannot express:
// authenticate, seed data, skip an endpoint that needs a state the test cannot
// reach. Dredd loads Node hook files into its own process, which a Go program
// cannot do — so vertrag ships a small Node worker, runs the hook files there,
// and exchanges transactions with it over a socket. The hook files themselves
// are unchanged, which is the point: they address transactions by names derived
// from the description, and rewriting them by hand is where suites break.
package hooks

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/antimatter-studios/vertrag/runner"
	"io"
)

// nodeWorker is embedded so a released binary carries everything it needs. A
// user installing vertrag from a tap gets hook support without a second
// download, and the worker can never be a version out of step with the binary
// driving it.
//
//go:embed worker/nodejs.js
var nodeWorker []byte

//go:embed worker/python.py
var pythonWorker []byte

// runtime is what a hook language needs: the worker to unpack, the file name
// to unpack it as, and the interpreters that might run it, in preference
// order. Adding a language is adding an entry and a worker file — the
// protocol is the same in every one, which is what keeps them equivalent.
type runtime struct {
	worker       []byte
	filename     string
	interpreters []string
	// missing is what to say when none of the interpreters is on PATH.
	missing string
}

var runtimes = map[string]runtime{
	"nodejs": {
		worker:       nodeWorker,
		filename:     "nodejs.js",
		interpreters: []string{"node"},
		missing:      "hook files need Node.js on PATH",
	},
	"python": {
		worker:   pythonWorker,
		filename: "python.py",
		// python3 first: on a system carrying both, `python` may still be
		// Python 2, which cannot run this worker.
		interpreters: []string{"python3", "python"},
		missing:      "hook files need Python 3 on PATH (looked for python3, then python)",
	},
}

// Languages lists what `language:` accepts, for an error message that offers
// the alternatives rather than only rejecting what was asked for.
func Languages() []string {
	names := make([]string, 0, len(runtimes))
	for name := range runtimes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Options configures the worker.
type Options struct {
	Language    string
	Hookfiles   []string
	Host        string
	Port        int
	Timeout     time.Duration
	ConnectWait time.Duration
	// Stderr receives the worker's own output, so a hook's console output and a
	// crashing hook file both reach the user.
	Stderr *os.File
}

// Client drives a hooks worker process.
type Client struct {
	options Options

	command *exec.Cmd
	conn    net.Conn
	reader  *bufio.Reader

	// mu serialises exchanges. The protocol correlates by uuid, but vertrag
	// runs transactions in order, so one at a time is both sufficient and what
	// makes hook side effects predictable.
	mu      sync.Mutex
	scratch string
}

// message is one frame in either direction.
type message struct {
	Event string          `json:"event,omitempty"`
	UUID  string          `json:"uuid"`
	Data  json.RawMessage `json:"data"`
	Error string          `json:"error,omitempty"`
}

// Start launches the worker and waits for it to accept connections.
func Start(ctx context.Context, options Options) (*Client, error) {
	language, known := runtimes[options.Language]
	if !known {
		return nil, fmt.Errorf("hooks language %q is not supported; vertrag has %s",
			options.Language, strings.Join(Languages(), " and "))
	}

	interpreter := ""
	for _, candidate := range language.interpreters {
		if _, err := exec.LookPath(candidate); err == nil {
			interpreter = candidate
			break
		}
	}
	if interpreter == "" {
		return nil, fmt.Errorf("%s", language.missing)
	}

	client := &Client{options: options}

	workerPath, err := client.writeWorker(language)
	if err != nil {
		return nil, err
	}

	args := []string{workerPath, "--port", strconv.Itoa(options.Port)}
	for _, file := range options.Hookfiles {
		absolute, err := filepath.Abs(file)
		if err != nil {
			return nil, fmt.Errorf("resolving hook file %s: %w", file, err)
		}
		if _, err := os.Stat(absolute); err != nil {
			return nil, fmt.Errorf("hook file %s: %w", file, err)
		}
		args = append(args, absolute)
	}

	client.command = exec.CommandContext(ctx, interpreter, args...)
	// The worker's stderr is kept as well as passed on, because it is the only
	// place that says WHY a worker died and it was being thrown away at exactly
	// the moment it mattered.
	//
	// A hook file with a syntax error, a port already held by something else, a
	// missing TypeScript loader — all of them end the worker before it
	// announces itself, and all of them reported the same bare line: "the hooks
	// worker exited before it was ready". The cause was one scroll away on a
	// stream nobody was reading (Options.Stderr is nil unless a caller sets it,
	// and a nil Stderr on exec.Cmd goes to /dev/null). A CI run failed this way
	// and told us nothing.
	notes := &tail{limit: 4096}
	if options.Stderr != nil {
		client.command.Stderr = io.MultiWriter(options.Stderr, notes)
	} else {
		client.command.Stderr = notes
	}
	stdout, err := client.command.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := client.command.Start(); err != nil {
		return nil, fmt.Errorf("starting the hooks worker: %w", err)
	}

	// The worker announces itself once it is listening. Waiting for that beats
	// retrying a connection, which cannot tell "not yet" from "crashed on a
	// syntax error in the hook file".
	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "vertrag-hooks-ready" {
				ready <- nil
				return
			}
		}
		ready <- fmt.Errorf("the hooks worker exited before it was ready%s", notes.suffix())
	}()

	select {
	case err := <-ready:
		if err != nil {
			client.Stop()
			return nil, err
		}
	case <-time.After(10 * time.Second):
		client.Stop()
		return nil, fmt.Errorf("the hooks worker did not start within 10s%s", notes.suffix())
	}

	if options.ConnectWait > 0 {
		time.Sleep(options.ConnectWait)
	}

	address := net.JoinHostPort(options.Host, strconv.Itoa(options.Port))
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		client.Stop()
		return nil, fmt.Errorf("connecting to the hooks worker on %s: %w", address, err)
	}
	client.conn = conn
	client.reader = bufio.NewReader(conn)

	return client, nil
}

// writeWorker unpacks the embedded worker for a language.
func (c *Client) writeWorker(language runtime) (string, error) {
	dir, err := os.MkdirTemp("", "vertrag-hooks-")
	if err != nil {
		return "", err
	}
	c.scratch = dir

	path := filepath.Join(dir, language.filename)
	if err := os.WriteFile(path, language.worker, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Stop shuts the worker down and removes what it unpacked.
func (c *Client) Stop() {
	if c.conn != nil {
		c.conn.Close()
	}
	if c.command != nil && c.command.Process != nil {
		c.command.Process.Kill()
		c.command.Wait()
	}
	if c.scratch != "" {
		os.RemoveAll(c.scratch)
	}
}

// exchange sends one event and applies whatever the worker sends back.
func (c *Client) exchange(event string, payload any, into any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	request, err := json.Marshal(message{Event: event, UUID: nextUUID(), Data: encoded})
	if err != nil {
		return err
	}
	if _, err := c.conn.Write(append(request, '\n')); err != nil {
		return fmt.Errorf("sending %s to the hooks worker: %w", event, err)
	}

	if c.options.Timeout > 0 {
		c.conn.SetReadDeadline(time.Now().Add(c.options.Timeout))
		defer c.conn.SetReadDeadline(time.Time{})
	}

	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("waiting for the %s hook: %w", event, err)
	}

	var response message
	if err := json.Unmarshal(line, &response); err != nil {
		return fmt.Errorf("reading the %s hook's reply: %w", event, err)
	}
	if response.Error != "" {
		return fmt.Errorf("%s hook: %s", event, response.Error)
	}
	if len(response.Data) == 0 {
		return nil
	}
	return json.Unmarshal(response.Data, into)
}

// uuid counts exchanges. The protocol only needs values to be distinct within a
// run, and a counter is easier to follow in a transcript than a random one.
var uuidCounter struct {
	sync.Mutex
	n int
}

func nextUUID() string {
	uuidCounter.Lock()
	defer uuidCounter.Unlock()
	uuidCounter.n++
	return "vertrag-" + strconv.Itoa(uuidCounter.n)
}

// The runner.Hooks implementation.

func (c *Client) BeforeAll(transactions []*runner.Transaction) error {
	return c.exchangeAll("beforeAll", transactions)
}

func (c *Client) AfterAll(transactions []*runner.Transaction) error {
	return c.exchangeAll("afterAll", transactions)
}

func (c *Client) BeforeEach(transaction *runner.Transaction) error {
	return c.exchangeOne("beforeEach", transaction)
}

func (c *Client) BeforeEachValidation(transaction *runner.Transaction) error {
	return c.exchangeOne("beforeEachValidation", transaction)
}

func (c *Client) AfterEach(transaction *runner.Transaction) error {
	return c.exchangeOne("afterEach", transaction)
}

func (c *Client) exchangeOne(event string, transaction *runner.Transaction) error {
	var updated wireTransaction
	if err := c.exchange(event, toWire(transaction), &updated); err != nil {
		return err
	}
	applyWire(transaction, updated)
	return nil
}

func (c *Client) exchangeAll(event string, transactions []*runner.Transaction) error {
	payload := make([]wireTransaction, 0, len(transactions))
	for _, transaction := range transactions {
		payload = append(payload, toWire(transaction))
	}

	var updated []wireTransaction
	if err := c.exchange(event, payload, &updated); err != nil {
		return err
	}
	for i := range updated {
		if i < len(transactions) {
			applyWire(transactions[i], updated[i])
		}
	}
	return nil
}

// tail keeps the last of a stream, for a diagnostic that is only wanted when
// something failed.
//
// Bounded because a chatty hook file could otherwise print megabytes into an
// error message, and it is the END that carries the cause: a stack trace's
// message comes after its frames, and a worker that logged happily for a while
// before dying is best explained by what it said last.
type tail struct {
	mu    sync.Mutex
	limit int
	kept  []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.kept = append(t.kept, p...)
	if len(t.kept) > t.limit {
		t.kept = t.kept[len(t.kept)-t.limit:]
	}
	return len(p), nil
}

// suffix renders what was kept for appending to an error, or "" when the worker
// died silently and there is nothing to add.
func (t *tail) suffix() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	text := strings.TrimSpace(string(t.kept))
	if text == "" {
		return ""
	}
	return ":\n" + text
}
