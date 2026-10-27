package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const eventStreamSpec = `openapi: 3.1.0
info: {title: Events, version: "1"}
paths:
  /accounts/1/events:
    get:
      summary: Stream events
      responses:
        "200":
          description: the account's events
          content:
            text/event-stream:
              schema: {type: string}
              x-events:
                new_message:
                  type: object
                  required: [id]
                  properties: {id: {type: integer}}
  /messages:
    post:
      summary: Send
      requestBody:
        content:
          application/json:
            schema: {type: object}
            example: {to: 1}
      responses:
        "201":
          description: sent
          content:
            application/json:
              schema: {type: object}
`

// mailbox streams a new_message event to every open stream whenever a message
// is sent. wrongShape sends the event with a string id, which its schema
// forbids.
type mailbox struct {
	wrongShape bool

	mu          sync.Mutex
	subscribers []chan string
}

func (m *mailbox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/accounts/1/events":
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, ": connected\n\n")
		w.(http.Flusher).Flush()
		events := make(chan string, 4)
		m.mu.Lock()
		m.subscribers = append(m.subscribers, events)
		m.mu.Unlock()
		for {
			select {
			case <-r.Context().Done():
				return
			case event := <-events:
				fmt.Fprint(w, event)
				w.(http.Flusher).Flush()
			}
		}
	case "/messages":
		id := `7`
		if m.wrongShape {
			id = `"seven"`
		}
		m.mu.Lock()
		for _, subscriber := range m.subscribers {
			subscriber <- "event: new_message\ndata: {\"id\": " + id + "}\n\n"
		}
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":7}`)
	default:
		http.NotFound(w, r)
	}
}

func writeEventStreamConfig(t *testing.T, endpoint, waitFor string) string {
	t.Helper()
	dir := t.TempDir()
	spec := filepath.Join(dir, "openapi.yaml")
	if err := os.WriteFile(spec, []byte(eventStreamSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "vertrag.yml")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("spec: %s\nendpoint: %s\n%s", spec, endpoint, waitFor)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const streamName = "/accounts/1/events > Stream events > 200 > text/event-stream"

// The done-when of #83: a run opens the event stream, sends a message, gets
// new_message and validates its data; an event of the wrong shape fails it.
func TestAnEventStreamIsHeldOpenForItsEventAndEachEventIsValidated(t *testing.T) {
	waitFor := fmt.Sprintf("wait-for:\n  - name: %q\n    event: new_message\n    within: 5s\n    trigger: \"/messages > Send > 201 > application/json\"\n", streamName)

	correct := httptest.NewServer(&mailbox{})
	defer correct.Close()
	stdout, err := runAuthzCLI(t, "--config", writeEventStreamConfig(t, correct.URL, waitFor))
	if err != nil {
		t.Fatalf("a correct stream: %v\n%s", err, stdout)
	}

	broken := httptest.NewServer(&mailbox{wrongShape: true})
	defer broken.Close()
	stdout, err = runAuthzCLI(t, "--config", writeEventStreamConfig(t, broken.URL, waitFor))
	if err == nil {
		t.Fatalf("an event of the wrong shape passed:\n%s", stdout)
	}
	if !strings.Contains(stdout, "event 1 (new_message)") {
		t.Errorf("the failure does not name the event:\n%s", stdout)
	}
}

// An event that never arrives is a failure, and says how long it was given.
func TestAnEventThatNeverArrivesFailsTheWait(t *testing.T) {
	server := httptest.NewServer(&mailbox{})
	defer server.Close()
	waitFor := fmt.Sprintf("wait-for:\n  - {name: %q, event: new_message, within: 300ms}\n", streamName)
	stdout, err := runAuthzCLI(t, "--config", writeEventStreamConfig(t, server.URL, waitFor))
	if err == nil {
		t.Fatalf("a missing event passed:\n%s", stdout)
	}
	if !strings.Contains(stdout, `no "new_message" event arrived within 300ms`) {
		t.Errorf("stdout:\n%s", stdout)
	}
}
