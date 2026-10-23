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

// ${NAME} in a credential header is read from the environment when the config loads, for the
// run's own auth and for every principal; one that is not set is refused, naming it (#78).
func TestCredentialHeadersReadTheEnvironment(t *testing.T) {
	t.Setenv("VERTRAG_TEST_READER", "rd-123")
	t.Setenv("VERTRAG_TEST_RUN", "run-456")
	path := writeConfig(t, "vertrag.yml", `spec: ./api.yaml
endpoint: http://localhost:4210
phases: [authorization]
auth:
  header: 'Authorization: Bearer ${VERTRAG_TEST_RUN}'
principals:
  - name: reader
    roles: [read]
    auth:
      header: 'Authorization: Bearer ${VERTRAG_TEST_READER}'
  - name: literal
    auth:
      header: 'X-Key: a$b $NOT_EXPANDED'
`)
	settings, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Auth.Header != "Authorization: Bearer run-456" {
		t.Errorf("the run's header: %q", settings.Auth.Header)
	}
	if got := settings.Principals[0].Auth.Header; got != "Authorization: Bearer rd-123" {
		t.Errorf("the reader's header: %q", got)
	}
	if got := settings.Principals[1].Auth.Header; got != "X-Key: a$b $NOT_EXPANDED" {
		t.Errorf("only ${NAME} is read: %q", got)
	}

	for _, c := range []struct{ name, body, want string }{
		{"principal", "principals:\n  - name: reader\n    auth: {header: 'Authorization: Bearer ${VERTRAG_TEST_UNSET}'}\n", `"reader": auth.header: names ${VERTRAG_TEST_UNSET}, which is not set`},
		{"run", "auth: {header: 'Authorization: Bearer ${VERTRAG_TEST_UNSET}'}\n", "auth.header: names ${VERTRAG_TEST_UNSET}, which is not set"},
	} {
		path := writeConfig(t, "vertrag.yml", "spec: ./api.yaml\nendpoint: http://localhost:4210\n"+c.body)
		if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: an unset variable is refused: %v", c.name, err)
		}
	}
}
