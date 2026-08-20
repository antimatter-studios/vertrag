package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
)

// doesNotSelect are the Config fields that name nothing — they set a value, a
// destination or a switch, and cannot reach zero things because they do not
// reach things at all.
//
// Each carries its reason, and the reason is the point. This list and
// `selectors` in doctor.go together have to account for EVERY field of
// config.Config, so a new setting cannot be added without somebody deciding
// which kind it is. That decision is the one nobody made for `fuzz.pin`,
// `auth.login.path` or the conditional headers, and each of those went on to
// match nothing in silence.
//
// The alternative — a registry somebody remembers to update — is itself a
// control that is present, inert and silent, which is the failure this whole
// file exists to prevent. So the test below fails on an unclassified field
// rather than trusting anyone to come back and add it.
var doesNotSelect = map[string]string{
	"Spec":                   "names the description document, not transactions in it",
	"Endpoint":               "where requests go",
	"Hookfiles":              "files to load; a hook file that matches nothing is the hook worker's business",
	"Language":               "which worker runs the hooks",
	"Server":                 "a command to run before testing",
	"ServerWait":             "how long to wait for that command",
	"Header":                 "plain headers go on every request, so they cannot miss",
	"Sorted":                 "ordering",
	"DryRun":                 "whether to send at all",
	"Names":                  "output switch",
	"Color":                  "output switch",
	"InlineErrors":           "output switch",
	"Details":                "output switch",
	"LogLevel":               "output switch",
	"User":                   "accepted and not acted on",
	"Path":                   "accepted and not acted on",
	"HooksWorkerHandlerHost": "where the hook worker listens",
	"HooksWorkerHandlerPort": "where the hook worker listens",
	"HooksWorkerTimeout":     "how long to wait on the hook worker",
	"HooksWorkerConnectWait": "how long to wait on the hook worker",
	"Reporters":              "output formats; an unknown one is refused at startup",
	"Outputs":                "where the reports go",
	"Checks":                 "switches for checks that apply to everything",
	"Auth":                   "the credential itself; its Except and Login.Path DO select and are registered",
	"Transport":              "how requests are sent, applied to all of them",
	"MaxFailures":            "a count",
	"Workers":                "a count",
	"Phases":                 "which phases run; an unknown one is refused at startup",
	"GraphQL":                "endpoint path, depth and the mutation switch",
	"Source":                 "which file the settings came from",
	"Unsupported":            "what was read and not acted on; reported already",
	"Notes":                  "messages to print",

	// These SELECT, and they are deliberately not in `selectors` — the run
	// already reports each unmatched value through filterTransactions, which
	// has its own tests. Listed here so the classification is complete and the
	// reason is recorded rather than inferred from absence.
	"Method":             "a filter; unmatched values are reported by filterTransactions",
	"Only":               "a filter; unmatched values are reported by filterTransactions",
	"Tag":                "a filter; unmatched values are reported by filterTransactions",
	"OperationID":        "a filter; unmatched values are reported by filterTransactions",
	"OnlyMatching":       "a filter; unmatched values are reported by filterTransactions",
	"Exclude":            "a filter; unmatched values are reported by filterTransactions",
	"ExcludeMatching":    "a filter; unmatched values are reported by filterTransactions",
	"ExcludeMethod":      "a filter; unmatched values are reported by filterTransactions",
	"ExcludeTag":         "a filter; unmatched values are reported by filterTransactions",
	"ExcludeOperationID": "a filter; unmatched values are reported by filterTransactions",

	// Fuzz.Pin and Fuzz.Accept select; Pin is registered. Accept is the one
	// remaining silent selector and is recorded as such rather than quietly
	// omitted — see TestTheKnownGapIsRecordedRatherThanForgotten.
	"Fuzz": "seed, cases and whole-request are values; Pin is registered, Accept is a known gap",

	// Skip and ConditionalHeaders are registered in `selectors`; naming them
	// here would be a contradiction, so they are absent on purpose and the test
	// below requires exactly that.
}

// registeredSelectorFields maps a Config field to the selector key that covers
// it, for fields whose selecting part lives in `selectors`.
var registeredSelectorFields = map[string]string{
	"Skip":               "skip",
	"ConditionalHeaders": "header (conditional)",
}

// TestEveryConfigFieldIsClassified is the mechanism, and everything else in
// doctor.go is what it enforces.
//
// Every failure of this class began the same way: a setting was added, nobody
// asked whether it could match nothing, and it later matched nothing without
// saying so. A hand-kept registry does not fix that, because forgetting to
// register is the same forgetting. Reflecting over the struct does: a new field
// fails this test on the day it lands, and the only way to pass is to decide
// which kind of setting it is and write the reason down.
func TestEveryConfigFieldIsClassified(t *testing.T) {
	fields := reflect.TypeOf(config.Config{})

	registered := map[string]bool{}
	for _, s := range selectors {
		registered[s.key] = true
	}

	for i := range fields.NumField() {
		name := fields.Field(i).Name
		if !fields.Field(i).IsExported() {
			continue
		}

		reason, classified := doesNotSelect[name]
		key, isSelector := registeredSelectorFields[name]

		switch {
		case classified && isSelector:
			t.Errorf("%s is listed as both selecting and not selecting; it can only be one", name)
		case isSelector:
			if !registered[key] {
				t.Errorf("%s claims to be covered by selector %q, which is not in selectors", name, key)
			}
		case classified:
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s is listed as not selecting with no reason; the reason is the point", name)
			}
		default:
			t.Errorf("config.Config.%s is new and unclassified. Decide: does it NAME things "+
				"(a transaction, a path, a field, a status)? If so register it in selectors so a "+
				"value matching nothing is reported. If not, add it to doesNotSelect with the "+
				"reason. Every silent-matcher bug this tool has had was a setting nobody asked "+
				"this question about", name)
		}
	}
}

// TestTheKnownGapIsRecordedRatherThanForgotten pins a deferral so that it
// cannot rot into a thing nobody remembers.
//
// `fuzz.accept` selects — it names statuses — and a status that never occurs is
// an entry doing nothing. It is not in `selectors` because whether a status
// occurs is only knowable from a run, not from the compiled transactions, so
// the preflight genuinely cannot answer it. The suppression count answers the
// other half at runtime.
//
// This test exists so that is a decision on the record rather than an absence.
// If accept ever becomes checkable before a run, this test is what tells the
// next person the gap was known.
func TestTheKnownGapIsRecordedRatherThanForgotten(t *testing.T) {
	if !strings.Contains(doesNotSelect["Fuzz"], "Accept is a known gap") {
		t.Error("the fuzz.accept gap has stopped being recorded; either it is now checked " +
			"before a run, in which case register it, or the note should say why not")
	}
}

// TestASettingThatReachesNothingIsReported is the behaviour the mechanism
// protects: each registered selector actually reports a value matching nothing.
func TestASettingThatReachesNothingIsReported(t *testing.T) {
	transactions := []compile.Transaction{{
		Name:     "GET /things > List > 200 > application/json",
		Request:  compile.Request{Method: "GET", URI: "/things"},
		Response: compile.Response{Status: "200"},
	}}

	settings := config.Config{
		Spec: "./api.yml",
		Skip: []config.SkipRule{{Name: "no such transaction"}},
		Auth: config.Auth{
			Login:  config.Login{Path: "/auth/login"},
			Except: []string{"also not a transaction"},
			Header: "Authorization: Bearer x",
		},
		Fuzz:               config.FuzzSettings{Pin: map[string]any{"dry_runn": true}},
		ConditionalHeaders: []config.HeaderRule{{Name: "X-Mock", Value: "on", Status: "404"}},
	}

	dead := inertSelectors(settings, transactions)

	// Every one of these is a real bug this project shipped, in the form it
	// shipped in.
	want := map[string]bool{
		"skip":                 true, // stopped applying to the probing phases
		"auth.except":          true,
		"auth.login.path":      true, // matched nothing, so the login carried its own session
		"fuzz.pin":             true, // a typo held nothing while reading as a safety control
		"header (conditional)": true,
	}
	for _, d := range dead {
		delete(want, d.key)
	}
	for key := range want {
		t.Errorf("%s reached nothing and was not reported", key)
	}
}

// TestDoctorNamesEveryDeadSettingAndSendsNothing drives the built binary,
// because the value of this command is what somebody reads on their terminal
// before they trust a result — and because every bug it exists to surface was
// found by driving a binary rather than by reading code.
//
// The configuration below carries five mistakes, and each is one this project
// actually shipped.
func TestDoctorNamesEveryDeadSettingAndSendsNothing(t *testing.T) {
	binary := build(t)

	dir := t.TempDir()
	description := `openapi: 3.0.3
info: {title: Doc, version: "1.0"}
paths:
  /orders:
    post:
      requestBody: {required: true, content: {application/json: {schema: {type: object, required: [dry_run], properties: {dry_run: {type: boolean}}}}}}
      responses: {"200": {description: ok, content: {application/json: {schema: {type: object}}}}}
`
	// The endpoint is a closed port: nothing may be sent, and a connection
	// error in the output would prove something was.
	config := `spec: ./api.yml
endpoint: http://127.0.0.1:1
auth:
  login: {path: /login}
  header: "Authorization: Bearer x"
  except: ["no such transaction"]
skip:
  - name: "also not a transaction"
    reason: "typo"
fuzz:
  pin: {dry_runn: true}
header:
  - name: X-Mock-Scenario
    value: staged
    when: {status: 404}
`
	for name, content := range map[string]string{"api.yml": description, "vertrag.yml": config} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	output, code := runIn(t, dir, binary, "doctor")
	if code == 0 {
		t.Errorf("a configuration with five dead settings exited 0:\n%s", output)
	}
	for _, want := range []string{"auth.except", "auth.login.path", "fuzz.pin", "header (conditional)", "skip"} {
		if !strings.Contains(output, want) {
			t.Errorf("%s reached nothing and doctor did not say so:\n%s", want, output)
		}
	}
	// The point of the command: it answers against a description, not a server.
	if strings.Contains(output, "connection refused") {
		t.Errorf("doctor sent something:\n%s", output)
	}
	// And it names the check that found 56 open endpoints elsewhere, because a
	// check nobody knows about is a check nobody runs.
	if !strings.Contains(output, "check-ignored-auth") {
		t.Errorf("an authenticated run was not told the auth check exists:\n%s", output)
	}
}

// TestDoctorIsQuietWhenTheConfigurationIsRight guards the command against
// becoming noise. If it always has something to say, it says nothing.
func TestDoctorIsQuietWhenTheConfigurationIsRight(t *testing.T) {
	binary := build(t)

	dir := t.TempDir()
	description := `openapi: 3.0.3
info: {title: Doc, version: "1.0"}
paths:
  /orders:
    post:
      requestBody: {required: true, content: {application/json: {schema: {type: object, required: [dry_run], properties: {dry_run: {type: boolean}}}}}}
      responses: {"200": {description: ok, content: {application/json: {schema: {type: object}}}}}
`
	config := `spec: ./api.yml
endpoint: http://127.0.0.1:1
fuzz:
  pin: {dry_run: true}
`
	for name, content := range map[string]string{"api.yml": description, "vertrag.yml": config} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	output, code := runIn(t, dir, binary, "doctor")
	if code != 0 {
		t.Errorf("a correct configuration exited %d:\n%s", code, output)
	}
	if !strings.Contains(output, "reaches something") {
		t.Errorf("doctor did not say the configuration was sound:\n%s", output)
	}
}
