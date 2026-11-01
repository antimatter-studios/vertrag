package hooks

import (
	"os"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/runner"
)

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// startWorker starts a hooks worker on a free port.
func startWorker(t *testing.T, options Options) (*Client, error) {
	t.Helper()
	options.Port = 0
	return Start(t.Context(), options)
}

func transactionNamed(name, body string) *runner.Transaction {
	return runner.New("http://example.invalid").Prepare(compile.Transaction{
		Name:    name,
		Request: compile.Request{Method: "POST", URI: "/things", Body: body, Headers: []compile.Header{}},
	})
}
