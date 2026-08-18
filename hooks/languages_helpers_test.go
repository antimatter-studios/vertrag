package hooks

import (
	"net"
	"os"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/runner"
)

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// freePort asks the kernel for one, so two tests running at once do not fight
// over the default.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func transactionNamed(name, body string) *runner.Transaction {
	return runner.New("http://example.invalid").Prepare(compile.Transaction{
		Name:    name,
		Request: compile.Request{Method: "POST", URI: "/things", Body: body, Headers: []compile.Header{}},
	})
}
