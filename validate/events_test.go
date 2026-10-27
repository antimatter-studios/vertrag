package validate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseEventsFollowsTheEventStreamFormat(t *testing.T) {
	stream := "\uFEFF: a comment, ignored\r\n" +
		"retry: 1000\n" +
		"data: {\"a\":1}\n\n" + // no event line: message
		"event: new_message\nid: 7\ndata: {\"id\":\ndata:  2}\n\n" + // data lines join with \n; one leading space dropped
		"event: empty\n\n" + // no data: not dispatched
		"event: folder_update\rdata:x\r\r" +
		"event: cut\ndata: {\"partial\"" // the read ended mid-event
	got := ParseEvents(stream)
	want := []Event{
		{Name: "message", Data: `{"a":1}`},
		{ID: "7", Name: "new_message", Data: "{\"id\":\n 2}"},
		{ID: "7", Name: "folder_update", Data: "x"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseEvents:\n  got  %#v\n  want %#v", got, want)
	}
	if !HasEvent(stream, "folder_update") || HasEvent(stream, "cut") {
		t.Error("HasEvent reads an event the stream did not finish")
	}
}

// Each event's data is validated against the schema its name is given; a
// name the description does not give is a failure too (#83).
func TestEventsAreValidatedAgainstTheirOwnSchemas(t *testing.T) {
	schemas := map[string]json.RawMessage{
		"new_message": json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"integer"}}}`),
		"ping":        json.RawMessage(`{"type":"string"}`),
	}
	good := "event: new_message\ndata: {\"id\": 3}\n\nevent: ping\ndata: hello\n\n"
	if field := AgainstEventSchemas(schemas, good); !field.Valid {
		t.Fatalf("a correct stream failed: %v", field.Errors)
	}

	bad := "event: new_message\ndata: {\"id\": \"three\"}\n\nevent: deleted\ndata: {}\n\n"
	field := AgainstEventSchemas(schemas, bad)
	if field.Valid || len(field.Errors) != 2 {
		t.Fatalf("errors %q", field.Errors)
	}
	if !strings.HasPrefix(field.Errors[0], "event 1 (new_message): ") {
		t.Errorf("first error %q", field.Errors[0])
	}
	if field.Errors[1] != "event 2 (deleted) is not one the description names; it names new_message, ping" {
		t.Errorf("second error %q", field.Errors[1])
	}

	// Validate reads the events rather than the stream's string schema.
	expected := Message{StatusCode: "200", BodySchema: json.RawMessage(`{"type":"string"}`), EventSchemas: schemas}
	if result := Validate(expected, Message{StatusCode: "200", Body: bad}); result.Valid {
		t.Error("Validate passed a stream whose events break their schemas")
	}
}
