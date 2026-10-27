package config_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/config"
)

// `wait-for` names a stream, the event it is held open for, how long, and the
// transaction that provokes it (#83).
func TestWaitFor(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", "spec: ./api.yaml\nendpoint: http://localhost:4210\n"+
		"wait-for:\n  - {name: stream, event: new_message, within: 3s, trigger: send}\n  - {name: other, event: ping}\n")
	settings, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []config.WaitFor{
		{Name: "stream", Event: "new_message", Within: 3 * time.Second, Trigger: "send"},
		{Name: "other", Event: "ping", Within: config.DefaultWaitWithin},
	}
	if !reflect.DeepEqual(settings.WaitFor, want) {
		t.Errorf("wait-for = %+v, want %+v", settings.WaitFor, want)
	}
	for _, entry := range []string{"{event: ping}", "{name: stream}", "{name: s, event: e, within: soon}", "{name: s, event: e, within: -1s}"} {
		path := writeConfig(t, "vertrag.yml", "spec: ./api.yaml\nendpoint: http://localhost:4210\nwait-for:\n  - "+entry+"\n")
		if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "wait-for entry 1") {
			t.Errorf("%s: %v", entry, err)
		}
	}
}
