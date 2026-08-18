package hooks

import (
	"net"
	"os"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/runner"
)

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// freePort asks the kernel for one, so two tests running at once do not fight
// over the default.
// freePort asks the kernel for a port and then gives it back, which is the
// usual trick and carries the usual race: between the listener closing here and
// the worker binding there, anything on the machine may take it. That is not
// theoretical — it failed a CI run with "the hooks worker exited before it was
// ready", which said nothing about ports at all.
//
// The race cannot be closed from this side (the worker binds its own socket, in
// another process), so startWorker below retries instead, and the error message
// now carries the worker's own account of what went wrong.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// startWorker starts a hooks worker, retrying a port that was taken between
// being offered and being bound. Any other failure is returned at once: a
// syntax error in a hook file is not going to fix itself on the second attempt,
// and retrying it would turn a clear failure into a slow one.
func startWorker(t *testing.T, options Options) (*Client, error) {
	t.Helper()
	var err error
	for attempt := range 3 {
		options.Port = freePort(t)
		var client *Client
		client, err = Start(t.Context(), options)
		if err == nil {
			return client, nil
		}
		if !strings.Contains(err.Error(), "EADDRINUSE") &&
			!strings.Contains(err.Error(), "address already in use") {
			return nil, err
		}
		t.Logf("port %d was taken before the worker could bind it, retrying (attempt %d)", options.Port, attempt+1)
	}
	return nil, err
}

func transactionNamed(name, body string) *runner.Transaction {
	return runner.New("http://example.invalid").Prepare(compile.Transaction{
		Name:    name,
		Request: compile.Request{Method: "POST", URI: "/things", Body: body, Headers: []compile.Header{}},
	})
}
