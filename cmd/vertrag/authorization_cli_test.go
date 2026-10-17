package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The authorization phase through `vertrag run`, the way a pipeline runs it:
// a description, a vertrag.yml with principals, and a server.

const (
	cliRunKey    = "SENTINEL-RUNWIDE-ADMIN-91ab"
	cliTenantKey = "SENTINEL-TENANT-KEY-52cd"
	cliSessionID = "SENTINEL-SESSION-ID-73ef"
)

const cliAuthzSpec = `openapi: 3.1.0
info: {title: Authz, version: "1"}
components:
  securitySchemes:
    tenantKey: {type: apiKey, in: header, name: X-Tenant-Key}
    session: {type: apiKey, in: cookie, name: sid}
security:
  - tenantKey: [admin]
  - session: [admin]
paths:
  /users:
    get:
      operationId: listUsers
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {type: object, required: [users]}
  /purge:
    delete:
      operationId: purge
      responses:
        "204": {description: gone}
`

// cliAuthzAPI admits the run-wide key and the session cookie as admins and
// the tenant key as a caller without admin, and echoes every credential it
// was given in every body.
type cliAuthzAPI struct {
	broken bool   // let the tenant key into /users
	answer string // "500" or "schema": what the session admin gets instead

	mu   sync.Mutex
	seen []string
}

func (a *cliAuthzAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("X-Tenant-Key")
	sid := ""
	if cookie, err := r.Cookie("sid"); err == nil {
		sid = cookie.Value
	}
	a.mu.Lock()
	a.seen = append(a.seen, fmt.Sprintf("%s %s key=%s sid=%s", r.Method, r.URL.Path, key, sid))
	a.mu.Unlock()
	reply := func(status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
	echo := fmt.Sprintf(`"key":%q,"sid":%q,"cookie":%q`, key, sid, r.Header.Get("Cookie"))
	admin := key == cliRunKey || sid == cliSessionID
	switch {
	case r.URL.Path == "/purge":
		reply(http.StatusNoContent, "")
	case sid == cliSessionID && a.answer == "500":
		reply(http.StatusInternalServerError, `{`+echo+`}`)
	case sid == cliSessionID && a.answer == "schema":
		reply(http.StatusOK, `{`+echo+`}`)
	case admin || (key == cliTenantKey && a.broken):
		reply(http.StatusOK, `{"users":[],`+echo+`}`)
	case key == cliTenantKey:
		reply(http.StatusForbidden, `{"error":"not yours",`+echo+`}`)
	default:
		reply(http.StatusUnauthorized, `{"error":"who are you",`+echo+`}`)
	}
}

func (a *cliAuthzAPI) requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.seen)
}

// writeAuthzConfig writes the description and a vertrag.yml for endpoint.
// The purge is skipped: it must never be sent, by any principal.
func writeAuthzConfig(t *testing.T, endpoint, extra string) string {
	t.Helper()
	dir := t.TempDir()
	spec := filepath.Join(dir, "openapi.yaml")
	if err := os.WriteFile(spec, []byte(cliAuthzSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "vertrag.yml")
	config := fmt.Sprintf(`spec: %s
endpoint: %s
phases: [authorization]
auth: {header: "X-Tenant-Key: %s"}
skip:
  - name: /purge > DELETE > 204
    reason: irreversible
principals:
  - name: anonymous
  - name: keyholder
    auth: {header: "X-Tenant-Key: %s"}
  - name: sessionadmin
    auth: {header: "Cookie: sid=%s"}
    roles: [admin]
%s`, spec, endpoint, cliRunKey, cliTenantKey, cliSessionID, extra)
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runAuthzCLI runs `vertrag run` and returns what it printed and returned.
func runAuthzCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	real := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	captured := make(chan string, 1)
	go func() {
		text, _ := io.ReadAll(read)
		captured <- string(text)
	}()
	runErr := runRun(append(args, "--no-color"))
	write.Close()
	os.Stdout = real
	return <-captured, runErr
}

func expectNoCredential(t *testing.T, where, text string) {
	t.Helper()
	for _, secret := range []string{cliRunKey, cliTenantKey, cliSessionID} {
		if strings.Contains(text, secret) {
			t.Errorf("%s contains the credential %s", where, secret)
		}
	}
}

// matrixCells reads the JSON report's matrix as "operation principal verdict
// expected actual bypass" lines.
func matrixCells(t *testing.T, report []byte) ([]string, json.RawMessage) {
	t.Helper()
	var parsed struct {
		Authorization json.RawMessage `json:"authorization"`
	}
	if err := json.Unmarshal(report, &parsed); err != nil {
		t.Fatalf("json report: %v\n%s", err, report)
	}
	var matrix struct {
		Cells []struct {
			Operation, Principal, Verdict, Expected, Actual string
			Bypass                                          bool
		} `json:"cells"`
	}
	if err := json.Unmarshal(parsed.Authorization, &matrix); err != nil {
		t.Fatalf("authorization key: %v\n%s", err, parsed.Authorization)
	}
	var lines []string
	for _, cell := range matrix.Cells {
		lines = append(lines, fmt.Sprintf("%s %s %s %s/%s %v",
			cell.Operation, cell.Principal, cell.Verdict, cell.Expected, cell.Actual, cell.Bypass))
	}
	return lines, parsed.Authorization
}

func runAuthzJSON(t *testing.T, api *cliAuthzAPI) ([]string, json.RawMessage, string, error) {
	t.Helper()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	return runAuthzJSONAt(t, server.URL)
}

func runAuthzJSONAt(t *testing.T, endpoint string) ([]string, json.RawMessage, string, error) {
	t.Helper()
	config := writeAuthzConfig(t, endpoint, "")
	output := filepath.Join(t.TempDir(), "report.json")
	stdout, err := runAuthzCLI(t, "--config", config, "--reporter", "json", "--output", output)
	report, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatalf("report: %v (run: %v)\n%s", readErr, err, stdout)
	}
	expectNoCredential(t, "the json report", string(report))
	expectNoCredential(t, "stdout", stdout)
	cells, matrix := matrixCells(t, report)
	return cells, matrix, stdout, err
}

func expectNoPurge(t *testing.T, api *cliAuthzAPI) {
	t.Helper()
	for _, request := range api.requests() {
		if strings.HasPrefix(request, "DELETE /purge") {
			t.Errorf("a skipped, irreversible operation was sent: %s", request)
		}
	}
}

func TestTheAuthorizationPhasePassesACorrectServer(t *testing.T) {
	api := &cliAuthzAPI{}
	cells, _, stdout, err := runAuthzJSON(t, api)
	if err != nil {
		t.Fatalf("a correct server failed the run: %v\n%s", err, stdout)
	}
	want := []string{
		"DELETE /purge anonymous unsent 401/ false",
		"DELETE /purge keyholder unsent 403/ false",
		"DELETE /purge sessionadmin unsent 204/ false",
		"GET /users anonymous pass 401/401 false",
		"GET /users keyholder pass 403/403 false",
		"GET /users sessionadmin pass 200/200 false",
	}
	if !slices.Equal(cells, want) {
		t.Errorf("matrix =\n%s\nwant\n%s", strings.Join(cells, "\n"), strings.Join(want, "\n"))
	}
	expectNoPurge(t, api)

	// Each principal sent its own credential and nothing else: the run-wide
	// admin key went only with the examples phase's own request.
	var users []string
	for _, request := range api.requests() {
		if strings.HasPrefix(request, "GET /users") {
			users = append(users, request)
		}
	}
	wantUsers := []string{
		"GET /users key=" + cliRunKey + " sid=",
		"GET /users key= sid=",
		"GET /users key=" + cliTenantKey + " sid=",
		"GET /users key= sid=" + cliSessionID,
	}
	if !slices.Equal(users, wantUsers) {
		t.Errorf("requests =\n%s\nwant\n%s", strings.Join(users, "\n"), strings.Join(wantUsers, "\n"))
	}
	for _, line := range []string{"Authorization matrix", "skipped by configuration: irreversible",
		"6 cells, 3 passing, 0 failing, 0 errors, 3 unsent"} {
		if !strings.Contains(stdout, line) {
			t.Errorf("stdout lacks %q:\n%s", line, stdout)
		}
	}
}

func TestTheAuthorizationPhaseFailsTheRunOnABrokenPermissionCheck(t *testing.T) {
	api := &cliAuthzAPI{broken: true}
	cells, _, stdout, err := runAuthzJSON(t, api)
	if err != errFailed {
		t.Fatalf("a bypass returned %v, want the failing exit\n%s", err, stdout)
	}
	if !slices.Contains(cells, "GET /users keyholder fail 403/200 true") {
		t.Errorf("the bypass is not in the matrix:\n%s", strings.Join(cells, "\n"))
	}
	if !strings.Contains(stdout, "authorization bypass") || !strings.Contains(stdout, "X-Tenant-Key: <redacted>") {
		t.Errorf("stdout does not report the bypass with a redacted replay:\n%s", stdout)
	}
	expectNoPurge(t, api)
}

func TestTheAuthorizationPhaseFailsServerErrorsAndBrokenBodies(t *testing.T) {
	for answer, want := range map[string]string{
		"500":    "GET /users sessionadmin fail 200/500 false",
		"schema": "GET /users sessionadmin fail 200/200 false",
	} {
		t.Run(answer, func(t *testing.T) {
			api := &cliAuthzAPI{answer: answer}
			cells, _, stdout, err := runAuthzJSON(t, api)
			if err != errFailed {
				t.Fatalf("returned %v, want the failing exit\n%s", err, stdout)
			}
			if !slices.Contains(cells, want) {
				t.Errorf("matrix lacks %q:\n%s", want, strings.Join(cells, "\n"))
			}
		})
	}
}

// Every report format: the matrix and the cells' results may carry no
// principal's credential, though the server echoes them in every body. The
// matrix is redacted under --no-sanitize too (see the reporter's tests); the
// ordinary results follow --no-sanitize as every other result does.
func TestTheAuthorizationPhaseLeaksNoCredentialInAnyFormat(t *testing.T) {
	for _, format := range []string{"cli", "markdown", "html", "junit", "json"} {
		t.Run(format, func(t *testing.T) {
			api := &cliAuthzAPI{broken: true}
			server := httptest.NewServer(api)
			defer server.Close()
			config := writeAuthzConfig(t, server.URL, "")
			output := filepath.Join(t.TempDir(), "report")
			stdout, err := runAuthzCLI(t, "--config", config, "--reporter", format, "--output", output)
			if err != errFailed {
				t.Fatalf("returned %v\n%s", err, stdout)
			}
			report, readErr := os.ReadFile(output)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(report), "keyholder") {
				t.Errorf("the %s report does not mention the cells:\n%s", format, report)
			}
			expectNoCredential(t, "the "+format+" report", string(report))
			expectNoCredential(t, "stdout", stdout)
		})
	}
}

func TestTheAuthorizationMatrixIsTheSameEveryRun(t *testing.T) {
	server := httptest.NewServer(&cliAuthzAPI{broken: true})
	defer server.Close()
	_, first, _, _ := runAuthzJSONAt(t, server.URL)
	_, second, _, _ := runAuthzJSONAt(t, server.URL)
	if string(first) != string(second) {
		t.Errorf("two runs reported different matrices:\n%s\n%s", first, second)
	}
}

// A dry run plans the matrix and sends nothing: not a cell, not the run's
// login, not a principal's OAuth2 token request.
func TestTheAuthorizationDryRunSendsNothing(t *testing.T) {
	api := &cliAuthzAPI{}
	server := httptest.NewServer(api)
	defer server.Close()
	config := writeAuthzConfig(t, server.URL, fmt.Sprintf(`  - name: oauth
    auth: {oauth2: {token-url: "%s/token", client-id: c, client-secret: s}}
    roles: [admin]
`, server.URL))
	stdout, err := runAuthzCLI(t, "--config", config, "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, stdout)
	}
	if seen := api.requests(); len(seen) != 0 {
		t.Errorf("a dry run sent %d request(s): %v", len(seen), seen)
	}
	for _, line := range []string{
		"Authorization matrix",
		"GET /users as keyholder: unsent: not sent: dry run; the description requires 403",
		"GET /users as oauth: unsent: not sent: dry run; the description requires 401",
		"DELETE /purge as anonymous: unsent: not sent: skipped by configuration: irreversible",
		"8 cells, 0 passing, 0 failing, 0 errors, 8 unsent",
	} {
		if !strings.Contains(stdout, line) {
			t.Errorf("dry run lacks %q:\n%s", line, stdout)
		}
	}
	expectNoCredential(t, "stdout", stdout)
}
