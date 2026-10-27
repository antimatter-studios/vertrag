package validate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Server-sent events.
//
// A `text/event-stream` body is not one document but a sequence of events,
// each with a name and its own data. OpenAPI has no keyword for what each
// event carries, so the description says it with an extension on the media
// type, which leaves the schema valid for every other tool:
//
//	text/event-stream:
//	  schema: {type: string}
//	  x-events:
//	    new_message: {$ref: "#/components/schemas/NewMessageEvent"}
//
// Each key is an event's name, and each value the schema of its data, read as
// JSON. A `oneOf` keyed by a discriminator cannot say this: the name travels
// outside the data, on its own `event:` line, so a discriminator would have no
// property to read.

// Event is one dispatched server-sent event.
type Event struct {
	ID   string
	Name string
	Data string
}

// ParseEvents reads a stream into the events it dispatched, as the WHATWG
// HTML standard's event-stream interpretation does: `field: value` lines, a
// blank line ending each event, a line starting with a colon a comment, and
// an event with no `event:` line named `message`. An event with no data is
// not dispatched, nor is a last one the read ended before its blank line —
// which, a stream being read for a bounded time, is usually the case.
func ParseEvents(stream string) []Event {
	stream = strings.TrimPrefix(stream, "\uFEFF")
	stream = strings.ReplaceAll(stream, "\r\n", "\n")
	stream = strings.ReplaceAll(stream, "\r", "\n")

	var events []Event
	var name, id string
	var data []string
	hasData := false
	for _, line := range strings.SplitAfter(stream, "\n") {
		if !strings.HasSuffix(line, "\n") {
			// Unterminated: the read ended mid-line.
			break
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			if hasData {
				if name == "" {
					name = "message"
				}
				events = append(events, Event{ID: id, Name: name, Data: strings.Join(data, "\n")})
			}
			name, data, hasData = "", nil, false
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			name = value
		case "data":
			data, hasData = append(data, value), true
		case "id":
			if !strings.Contains(value, "\x00") {
				id = value
			}
		}
	}
	return events
}

// AgainstEventSchemas validates every event in a stream against the schema
// the description gives its name. An event whose name the description does
// not give is a failure, since a client written from the description has no
// handler for it. Data that is not JSON is validated as the string it is.
func AgainstEventSchemas(schemas map[string]json.RawMessage, stream string) FieldResult {
	field := FieldResult{Valid: true, Kind: kind("json"), Errors: []string{}}
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}
	sort.Strings(names)

	for i, event := range ParseEvents(stream) {
		subject := fmt.Sprintf("event %d (%s)", i+1, event.Name)
		schema, described := schemas[event.Name]
		if !described {
			field.Valid = false
			field.Errors = append(field.Errors, fmt.Sprintf("%s is not one the description names; it names %s",
				subject, strings.Join(names, ", ")))
			continue
		}
		var document any = event.Data
		var decoded any
		if json.Unmarshal([]byte(event.Data), &decoded) == nil {
			document = decoded
		}
		checked := validateValue(schema, document, "the data of "+subject)
		if !checked.Valid {
			field.Valid = false
			for _, reason := range checked.Errors {
				field.Errors = append(field.Errors, subject+": "+reason)
			}
		}
	}
	return field
}

// HasEvent reports whether a stream has dispatched an event of this name.
func HasEvent(stream, name string) bool {
	for _, event := range ParseEvents(stream) {
		if event.Name == name {
			return true
		}
	}
	return false
}
