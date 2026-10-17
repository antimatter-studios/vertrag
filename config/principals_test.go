package config_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/config"
)

func TestPrincipalsReuseAuthAndPreserveTheirOrder(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `spec: ./api.yaml
endpoint: http://localhost:4210
phases: [authorization]
principals:
  - name: admin
    roles: [read, write]
    auth:
      header: 'Authorization: Bearer fixture-admin'
  - name: reader
    roles: [read]
    auth:
      login: {path: /login, body: {username: reader}}
      cookie: session
  - name: anonymous
`)
	settings, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Principals) != 3 {
		t.Fatalf("principals = %+v", settings.Principals)
	}
	admin, reader, anonymous := settings.Principals[0], settings.Principals[1], settings.Principals[2]
	if admin.Name != "admin" || !reflect.DeepEqual(admin.Roles, []string{"read", "write"}) ||
		admin.Auth.Header != "Authorization: Bearer fixture-admin" {
		t.Errorf("admin = %+v", admin)
	}
	if reader.Name != "reader" || reader.Auth.Login.Method != "POST" || reader.Auth.Carry != "cookie" || reader.Auth.Cookie != "session" {
		t.Errorf("reader did not reuse auth defaults: %+v", reader)
	}
	if anonymous.Name != "anonymous" || anonymous.Auth.Configured() {
		t.Errorf("anonymous = %+v", anonymous)
	}
	if !reflect.DeepEqual(settings.Phases, []string{config.PhaseExamples, config.PhaseAuthorization}) {
		t.Errorf("phases = %v", settings.Phases)
	}
}

func TestPrincipalsRejectAmbiguousNames(t *testing.T) {
	for _, principals := range []string{
		"- roles: [read]\n",
		"- name: '   '\n",
		"- name: reader\n  - name: reader\n",
	} {
		path := writeConfig(t, "vertrag.yml", "spec: ./api.yaml\nendpoint: http://localhost:4210\nprincipals:\n  "+principals)
		if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "principals") {
			t.Errorf("Load(%q) = %v, want a principal name error", principals, err)
		}
	}
}
