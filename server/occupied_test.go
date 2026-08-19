package server

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// TestAFailedCommandIsNotRescuedBySomebodyElsesPort reproduces what CI hit, and
// what it hit was a real defect rather than a flaky test.
//
// freeAddress asks the kernel for a port and gives it straight back, so between
// the release and the poll anything on the machine may take it. On two of three
// runners something did — and Start reported that a command whose whole body was
// `exit 1` had started successfully, because it dialled the port and found it
// open.
//
// A port is a machine-wide resource. Treating "something answers here" as proof
// that THIS command succeeded means a server that died on a typo is reported
// healthy, and the suite then runs against a stranger's process with errors
// describing neither. The exit status is the evidence that belongs to the
// command; the socket is not.
func TestAFailedCommandIsNotRescuedBySomebodyElsesPort(t *testing.T) {
	// An occupant that is emphatically not the server under test, holding the
	// port for the whole attempt.
	occupant, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupant.Close()
	endpoint := "http://" + occupant.Addr().String()

	_, err = Start(context.Background(), Options{
		Command:  `echo "Cannot find module ./app.js" >&2; exit 1`,
		Endpoint: endpoint,
		Wait:     2 * time.Second,
	})
	if err == nil {
		t.Fatal("a command that exited 1 was reported as started because another process held the port")
	}
	// The report has to say both halves, or the reader goes looking in the
	// wrong place: their command failed, AND what they would have tested.
	for _, want := range []string{"Cannot find module ./app.js", "exit status 1", "already listening"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// TestABackgroundingCommandIsStillAccepted guards the fix against over-reaching.
// `docker compose up -d` and `./start.sh &` both return as soon as the server
// belongs to somebody else — with status zero, which is exactly what separates
// them from a command that died.
func TestABackgroundingCommandIsStillAccepted(t *testing.T) {
	address, endpoint := freeAddress(t)

	// Exits zero immediately, having left something listening behind it.
	server, err := Start(context.Background(), Options{
		Command:  `(nc -l ` + portOf(address) + ` >/dev/null 2>&1 &) ; exit 0`,
		Endpoint: endpoint,
		Wait:     3 * time.Second,
	})
	if err != nil {
		t.Skipf("no usable backgrounding listener here: %v", err)
	}
	defer server.Stop()
}

// portOf takes the port out of a host:port address.
func portOf(address string) string {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	return port
}
