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
	"strconv"
	"sync"
	"time"

	"github.com/antimatter-studios/vertrag/runner"
)

// nodeWorker is embedded so a released binary carries everything it needs. A
// user installing vertrag from a tap gets hook support without a second
// download, and the worker can never be a version out of step with the binary
// driving it.
//
//go:embed worker/nodejs.js
var nodeWorker []byte

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
	if options.Language != "nodejs" {
		return nil, fmt.Errorf(
			"hooks language %q is not supported yet; only nodejs is", options.Language)
	}
	if _, err := exec.LookPath("node"); err != nil {
		return nil, fmt.Errorf("hook files need Node.js on PATH: %w", err)
	}

	client := &Client{options: options}

	workerPath, err := client.writeWorker()
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

	client.command = exec.CommandContext(ctx, "node", args...)
	client.command.Stderr = options.Stderr
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
		ready <- fmt.Errorf("the hooks worker exited before it was ready")
	}()

	select {
	case err := <-ready:
		if err != nil {
			client.Stop()
			return nil, err
		}
	case <-time.After(10 * time.Second):
		client.Stop()
		return nil, fmt.Errorf("the hooks worker did not start within 10s")
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

// writeWorker unpacks the embedded worker next to the hook files it will load.
func (c *Client) writeWorker() (string, error) {
	dir, err := os.MkdirTemp("", "vertrag-hooks-")
	if err != nil {
		return "", err
	}
	c.scratch = dir

	path := filepath.Join(dir, "nodejs.js")
	if err := os.WriteFile(path, nodeWorker, 0o600); err != nil {
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
