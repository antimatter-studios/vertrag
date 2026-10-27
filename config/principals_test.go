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
      login: {path: /login, body: {username: reader}, public: [username]}
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

// The `authorization` section names who makes the phase's fixtures and which
// statuses refuse; a refusal outside 4xx and an owner that is no principal are
// refused, since either would make the matrix say something false (#81).
func TestAuthorizationSettings(t *testing.T) {
	base := "spec: ./api.yaml\nendpoint: http://localhost:4210\nphases: [authorization]\n" +
		"principals:\n  - name: anonymous\n  - name: admin\n    auth: {header: 'Authorization: Bearer a'}\n"
	settings, err := config.Load(writeConfig(t, "vertrag.yml", base+"authorization: {owner: admin, refuse: [403, 404]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := config.AuthorizationSettings{Owner: "admin", Refuse: []string{"403", "404"}}
	if !reflect.DeepEqual(settings.Authorization, want) {
		t.Errorf("authorization = %+v, want %+v", settings.Authorization, want)
	}
	if err := settings.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	if _, err := config.Load(writeConfig(t, "vertrag.yml", base+"authorization: {refuse: [200]}\n")); err == nil ||
		!strings.Contains(err.Error(), "authorization: refuse: 200 is not a 4xx") {
		t.Errorf("a 2xx refusal: %v", err)
	}
	unknown, err := config.Load(writeConfig(t, "vertrag.yml", base+"authorization: {owner: nobody}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := unknown.Validate(); err == nil || !strings.Contains(err.Error(), `authorization.owner "nobody" names no principal`) {
		t.Errorf("an unknown owner: %v", err)
	}
}

// `tenants` groups principals for the ownership phase: two at least, each
// member a principal, none in two tenants. `ownership.refuse` defaults to 403
// or 404 (#82).
func TestTenantsAndOwnership(t *testing.T) {
	base := "spec: ./api.yaml\nendpoint: http://localhost:4210\nphases: [ownership]\n" +
		"principals:\n  - name: a1\n  - name: b1\n"
	settings, err := config.Load(writeConfig(t, "vertrag.yml", base+"tenants: {a: [a1], b: [b1]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	if !reflect.DeepEqual(settings.Tenants, map[string][]string{"a": {"a1"}, "b": {"b1"}}) ||
		!reflect.DeepEqual(settings.Ownership.Refuse, []string{"403", "404"}) {
		t.Errorf("tenants %v, refuse %v", settings.Tenants, settings.Ownership.Refuse)
	}
	if !reflect.DeepEqual(settings.Phases, []string{config.PhaseExamples, config.PhaseOwnership}) {
		t.Errorf("phases = %v", settings.Phases)
	}

	custom, err := config.Load(writeConfig(t, "vertrag.yml", base+"tenants: {a: [a1], b: [b1]}\nownership: {refuse: [404]}\n"))
	if err != nil || !reflect.DeepEqual(custom.Ownership.Refuse, []string{"404"}) {
		t.Errorf("custom refuse: %v %v", custom.Ownership.Refuse, err)
	}

	for tenants, want := range map[string]string{
		"tenants: {a: [a1]}\n":              "at least two",
		"tenants: {a: [a1], b: [nobody]}\n": `"b" lists "nobody", which names no principal`,
		"tenants: {a: [a1], b: [a1]}\n":     `"a1" is in both "a" and "b"`,
		"tenants: {a: [a1], b: []}\n":       `"b" has no principals`,
	} {
		loaded, err := config.Load(writeConfig(t, "vertrag.yml", base+tenants))
		if err != nil {
			t.Fatal(err)
		}
		if err := loaded.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", strings.TrimSpace(tenants), err, want)
		}
	}
}
