// Package config reads vertrag's configuration.
//
// The file is `vertrag.yml`, and it is a superset of Dredd's `dredd.yml`: every
// key Dredd understands means the same thing here, and vertrag's own keys are
// added alongside. So migrating is a rename — but it is a rename that has to
// happen, because a `dredd.yml` is no longer discovered. That fallback existed
// to make adoption a no-op and it earned its removal: it meant the same key
// meant different things depending on the name of the file holding it, and half
// of vertrag's own settings had to be refused from one of the two names to stop
// two testers silently disagreeing about what they tested. Now there is one
// question — is this file mine? — and the answer is its name.
//
// A file named on the command line is read as vertrag's whatever it is called.
// Naming a file is unambiguous about intent in a way that finding one is not.
//
// Options vertrag does not act on are accepted and reported rather than
// rejected: a project that Dredd runs should not fail here over a key that only
// affects output.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is a test run's settings.
type Config struct {
	// Spec is the API description document to test against, from the `spec` key.
	//
	// It was `blueprint`, which is still read and no longer documented: the key
	// was named after API Blueprint, and that is the one format vertrag does not
	// support. A primary setting named after the thing it cannot do is worse
	// than a rename.
	Spec     string
	Endpoint string

	// Hookfiles are loaded by a worker in the language they are written in:
	// `nodejs` (the default, for a project arriving from Dredd) or `python`.
	// Go has none — a Go program cannot load a Go source file at runtime, so
	// hooks in it would mean the user compiling their own binary.
	Hookfiles []string
	Language  string

	// Server is a command to start before testing, and ServerWait how long to
	// wait for it to listen.
	Server     string
	ServerWait time.Duration

	Method       []string
	Only         []string
	Tag          []string
	OperationID  []string
	Header       []string
	Path         []string
	Sorted       bool
	DryRun       bool
	Names        bool
	Color        bool
	InlineErrors bool
	Details      bool
	LogLevel     string
	User         string

	HooksWorkerHandlerHost string
	HooksWorkerHandlerPort int
	HooksWorkerTimeout     time.Duration
	HooksWorkerConnectWait time.Duration

	// Reporters name the output formats, each optionally paired with a file in
	// Outputs by position. A reporter with no file writes to stdout.
	Reporters []string
	Outputs   []string

	// Checks turns off the checks Dredd does not make. They are on by default:
	// a contract violation is worth reporting even when the tool a project came
	// from would have missed it.
	Checks Checks

	// Auth logs the run in once and carries the result on every request.
	Auth Auth

	// Transport is how requests reach the server: timeout, retries, pacing,
	// TLS trust and proxy. Zero values are vertrag's defaults.
	Transport Transport

	// MaxFailures stops the run after this many failures or errors; zero
	// means run everything.
	MaxFailures int

	// Workers is how many transactions to send at once; zero or one is
	// sequential.
	Workers int

	// Phases are what a run does: "examples" (the documented transactions,
	// always first and on by default), then optionally "coverage" (every
	// boundary each schema implies, deterministic) and "fuzz" (values drawn
	// at random). Empty means examples only, which is every run before phases
	// existed.
	Phases []string

	// Fuzz pins the random phase for CI: a fixed seed makes it reproduce, and
	// cases bounds its cost.
	Fuzz FuzzSettings

	// Skip takes transactions out of the run.
	Skip []SkipRule

	// ConditionalHeaders are the `header` entries written in vertrag's
	// conditional form.
	ConditionalHeaders []HeaderRule

	// Source is the file these settings came from, or "" for defaults.
	Source string

	// Unsupported records configuration keys that were present and are not
	// acted on. The caller reports them, so a run never quietly ignores what
	// the user asked for.
	Unsupported []string

	// Notes are messages about the configuration to print verbatim. Unsupported
	// says "vertrag cannot do this yet", which is the wrong thing to say about a
	// key that works and merely needs writing differently — a `blueprint` that is
	// now spelled `spec`, say.
	Notes []string
}

// FuzzSettings pin the fuzz phase of a run.
type FuzzSettings struct {
	// Seed reproduces a run; zero picks one and prints it.
	Seed uint64
	// Cases per body and parameter; zero means the default.
	Cases int
	// WholeRequest also draws every part together per case.
	WholeRequest bool
}

// Checks selects the checks beyond Dredd's.
type Checks struct {
	// ServerError reports a 5xx as the server failing rather than as a status
	// mismatch like any other.
	ServerError bool
	// ContentType reports a response carrying a media type the description
	// never promised. Dredd compares header presence only.
	ContentType bool
	// HeaderSchema validates a response header's value against the schema the
	// description gave it. Alone among these it is off by default: header
	// schemas have never been enforced by anything, so a description is quite
	// likely to carry one that was never true, and a suite that goes red on the
	// day it adopts vertrag teaches people to distrust the tool rather than the
	// description.
	HeaderSchema bool

	// IgnoredAuth re-sends each authenticated request without the credential
	// and reports an endpoint that answers it anyway. Off by default: it
	// doubles the requests a run makes.
	IgnoredAuth bool
}

// Auth describes how a run authenticates itself.
//
// This exists because authentication is the one thing very nearly every suite
// needs and almost none of it is specific to the project: log in, keep the
// credential, send it on everything afterwards. Expressing that as a hook file
// costs a worker process and a language runtime to run three steps that do not
// vary. What genuinely does vary — deriving a value per transaction, reacting to
// a response — stays in hooks, which is why this block deliberately has no
// conditionals in it.
type Auth struct {
	// Login is the request that obtains the credential. Zero value means the
	// credential is static and Header carries it directly.
	Login Login

	// OAuth2 obtains the credential by the client credentials grant, which is
	// the one OAuth2 flow a suite can perform unattended.
	OAuth2 OAuth2

	// Carry is how the credential is sent back: "cookie" or "bearer".
	Carry string

	// Cookie names a single cookie to keep out of the login response, for a
	// server that sets several and only one of them authenticates.
	Cookie string

	// Header sets a fixed header on every request, for an API key or a token
	// that does not need logging in for. Written as "Name: value".
	Header string

	// Except names transactions that must go out unauthenticated. A login
	// endpoint's own 401 case cannot be tested while holding a valid
	// credential, so this is needed by any suite that documents one.
	Except []string
}

// OAuth2 is the client credentials grant: a service asking for a token in its
// own name. The interactive grants need a browser and a human, so no headless
// suite performs them and none is offered here.
type OAuth2 struct {
	// TokenURL is the token endpoint, absolute when the identity provider is
	// not the API under test, or a path on the endpoint when it is.
	TokenURL string

	ClientID string

	// ClientSecretEnv names an environment variable holding the secret, which
	// is the documented way: a secret in a configuration file is a secret in
	// version control. ClientSecret takes it literally, for the CI systems
	// that generate their config with it already inside.
	ClientSecretEnv string
	ClientSecret    string

	Scopes []string
}

// Configured reports whether the grant was asked for.
func (o OAuth2) Configured() bool { return o.TokenURL != "" }

// HeaderRule adds a header to the transactions it matches.
//
// It shares the `header` key with Dredd's plain `Name: value` strings rather
// than taking a `headers:` of its own, because two keys a letter apart that did
// different things would be read wrong at a glance and mistyped forever.
type HeaderRule struct {
	Name  string
	Value string

	// Status and Method are the conditions, both optional. Empty matches every
	// transaction, which is the same as writing a plain string entry.
	Status string
	Method string
}

// SkipRule takes one transaction out of the run.
//
// The reason is optional but strongly worth giving: it is printed with the skip,
// so a report says why 40 transactions did not run instead of only that they
// did not. A skip list is where a suite's unexamined debt collects, and one that
// states its reasons is one somebody can eventually work through.
type SkipRule struct {
	Name   string
	Reason string
}

// Login is the request that obtains a credential.
type Login struct {
	Method string
	Path   string
	Body   map[string]any
}

// Configured reports whether any authentication was asked for.
func (a Auth) Configured() bool {
	return a.Login.Path != "" || a.Header != "" || a.OAuth2.Configured()
}

// Filenames are tried in order, and are vertrag's own. `dredd.yml` used to be
// on the end of this list; DreddFilenames is what became of it.
var Filenames = []string{"vertrag.yml", "vertrag.yaml"}

// DreddFilenames are not read. They are still recognised, because a project
// holding one and no vertrag file has configuration that is about to be
// ignored, and being told is worth more than being right about it silently.
var DreddFilenames = []string{"dredd.yml", "dredd.yaml"}

// Discover finds a configuration file in the working directory.
func Discover() string {
	return firstPresent(Filenames)
}

// DreddFile names a Dredd configuration in the working directory, for the
// caller to refuse over when Discover found nothing.
//
// It reports the file's presence and nothing about its contents: the point is
// not to read it. A project running both testers keeps a file per tester, each
// complete on its own — and in that project Discover succeeds, so this is never
// consulted. It answers only the one case where a `dredd.yml` is the only
// configuration there is, which used to work and now needs the rename.
func DreddFile() string {
	return firstPresent(DreddFilenames)
}

func firstPresent(names []string) string {
	for _, name := range names {
		if _, err := os.Stat(name); err == nil {
			return name
		}
	}
	return ""
}

// file mirrors dredd.yml. Every field is a pointer or slice so an absent key can
// be told from one explicitly set to a zero value — `color: false` means
// something different from no `color` key at all.
type file struct {
	Spec *string `yaml:"spec"`
	// Blueprint is the former spelling of `spec`. Still read so no existing
	// config breaks, deliberately absent from the documentation.
	Blueprint  *string  `yaml:"blueprint"`
	Endpoint   *string  `yaml:"endpoint"`
	Hookfiles  any      `yaml:"hookfiles"`
	Language   *string  `yaml:"language"`
	Server     *string  `yaml:"server"`
	ServerWait *float64 `yaml:"server-wait"`

	Method []string `yaml:"method"`
	Only   []string `yaml:"only"`
	// Header is `any` because an entry may be Dredd's `Name: value` string or
	// vertrag's conditional form — see toHeaderRules.
	Header       []any    `yaml:"header"`
	Path         []string `yaml:"path"`
	Sorted       *bool    `yaml:"sorted"`
	DryRun       *bool    `yaml:"dry-run"`
	Names        *bool    `yaml:"names"`
	Color        *bool    `yaml:"color"`
	InlineErrors *bool    `yaml:"inline-errors"`
	Details      *bool    `yaml:"details"`
	LogLevel     *string  `yaml:"loglevel"`
	User         *string  `yaml:"user"`

	HooksWorkerHandlerHost *string  `yaml:"hooks-worker-handler-host"`
	HooksWorkerHandlerPort *int     `yaml:"hooks-worker-handler-port"`
	HooksWorkerTimeout     *float64 `yaml:"hooks-worker-timeout"`
	HooksWorkerConnectWait *float64 `yaml:"hooks-worker-after-connect-wait"`

	Reporter any `yaml:"reporter"`
	Output   any `yaml:"output"`

	// Keys Dredd accepts that vertrag does not act on. They are declared so
	// they can be reported rather than silently dropped.
	Require any `yaml:"require"`
	Custom  any `yaml:"custom"`
	Init    any `yaml:"init"`

	// vertrag's own.
	Checks *checksFile `yaml:"checks"`
	Auth   *authFile   `yaml:"auth"`
	// Transport is vertrag's own: how requests reach the server.
	Transport *transportFile `yaml:"transport"`
	// Skip is `any` because an entry may be written either way — see toSkipRules.
	Skip []any `yaml:"skip"`
	// Tag narrows the run to operations carrying one of these tags.
	Tag []string `yaml:"tag"`
	// OperationID narrows the run to these operationIds.
	OperationID []string `yaml:"operation-id"`
	// MaxFailures stops the run early.
	MaxFailures *int `yaml:"max-failures"`
	// Workers sends several transactions at once.
	Workers *int `yaml:"workers"`
	// Phases selects what a run does; Fuzz pins the fuzz phase.
	Phases []string  `yaml:"phases"`
	Fuzz   *fuzzFile `yaml:"fuzz"`
}

// fuzzFile is the `fuzz` section as written.
type fuzzFile struct {
	Seed         *uint64 `yaml:"seed"`
	Cases        *int    `yaml:"cases"`
	WholeRequest *bool   `yaml:"whole-request"`
}

// Transport is the `transport` section, resolved.
type Transport struct {
	Timeout  time.Duration
	Retries  int
	Delay    time.Duration
	Insecure bool
	CACert   string
	Proxy    string

	// ClientCert and ClientCertKey are the certificate a server that requires
	// mutual TLS asks for. The key may live in the certificate file, in which
	// case ClientCertKey is empty.
	ClientCert    string
	ClientCertKey string
}

// transportFile is the `transport` section as written. Durations are Go
// duration strings ("10s", "250ms") so a reader never has to guess the unit.
type transportFile struct {
	Timeout  *string `yaml:"timeout"`
	Retries  *int    `yaml:"retries"`
	Delay    *string `yaml:"delay"`
	Insecure *bool   `yaml:"insecure"`
	CACert   *string `yaml:"ca-cert"`
	Proxy    *string `yaml:"proxy"`
	// Cert is the client certificate and CertKey its key, in the spelling the
	// flags use: `ca-cert` is what to trust, `cert` is what to present.
	Cert    *string `yaml:"cert"`
	CertKey *string `yaml:"cert-key"`
}

// authFile is the `auth` section. Dredd has no equivalent: authenticating a
// suite there means a hook file, a worker process and a language runtime to run
// it in, to do what is nearly always the same three steps — log in once, keep
// what came back, send it on everything after.
type authFile struct {
	Login  *loginFile  `yaml:"login"`
	OAuth2 *oauth2File `yaml:"oauth2"`
	Carry  *string     `yaml:"carry"`
	Cookie *string     `yaml:"cookie"`
	Header *string     `yaml:"header"`
	Except []string    `yaml:"except"`
}

// oauth2File is the `auth.oauth2` section as written.
type oauth2File struct {
	TokenURL        *string  `yaml:"token-url"`
	ClientID        *string  `yaml:"client-id"`
	ClientSecretEnv *string  `yaml:"client-secret-env"`
	ClientSecret    *string  `yaml:"client-secret"`
	Scopes          []string `yaml:"scopes"`
}

// loginFile is the request that obtains the credential.
type loginFile struct {
	Method *string        `yaml:"method"`
	Path   *string        `yaml:"path"`
	Body   map[string]any `yaml:"body"`
}

// checksFile is the `checks` section, which Dredd has no equivalent of.
type checksFile struct {
	ServerError  *bool `yaml:"server-error"`
	ContentType  *bool `yaml:"content-type"`
	HeaderSchema *bool `yaml:"header-schema"`
	IgnoredAuth  *bool `yaml:"ignored-auth"`
}

// Default returns the settings a run starts from.
func Default() Config {
	return Config{
		Language:               "nodejs",
		Color:                  true,
		LogLevel:               "warning",
		ServerWait:             3 * time.Second,
		HooksWorkerHandlerHost: "127.0.0.1",
		HooksWorkerHandlerPort: 61321,
		HooksWorkerTimeout:     5 * time.Second,
		HooksWorkerConnectWait: 100 * time.Millisecond,
		Reporters:              []string{"cli"},
		Checks:                 Checks{ServerError: true, ContentType: true},
	}
}

// Load reads a configuration file, layering it over the defaults.
func Load(path string) (Config, error) {
	config := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		return config, err
	}

	var parsed file
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return config, fmt.Errorf("reading %s: %w", path, err)
	}

	apply(&config, parsed)

	switch {
	case parsed.Spec != nil && parsed.Blueprint != nil:
		config.Notes = append(config.Notes, fmt.Sprintf(
			"%s sets both `spec` and `blueprint`; using spec (%s). `blueprint` is the "+
				"former name for the same setting and can be deleted.", path, config.Spec))
	case parsed.Blueprint != nil:
		config.Notes = append(config.Notes, fmt.Sprintf(
			"%s uses `blueprint`, which still works and is no longer documented. It is "+
				"now `spec` — the old name came from API Blueprint, the one format "+
				"vertrag does not support.", path))
	}

	// Every key in the file is applied, including vertrag's own.
	//
	// These ten used to be gated on the file's name: read out of a `dredd.yml`,
	// `auth` would have authenticated vertrag's run and not Dredd's, and since
	// Dredd ignores keys it does not recognise without a word, a project running
	// both testers from what looked like a shared file would have had them
	// quietly testing different things. The gate went when the fallback did —
	// vertrag no longer discovers a `dredd.yml`, so no file reaches here except
	// one that vertrag was pointed at, and refusing half the keys in a file
	// someone named on the command line would be the surprising behaviour.
	if parsed.Auth != nil {
		applyAuth(&config.Auth, *parsed.Auth)
	}
	if parsed.Transport != nil {
		if err := applyTransport(&config.Transport, *parsed.Transport); err != nil {
			return config, fmt.Errorf("%s: transport: %w", path, err)
		}
	}
	config.Skip = append(config.Skip, toSkipRules(parsed.Skip)...)
	config.Tag = append(config.Tag, parsed.Tag...)
	config.OperationID = append(config.OperationID, parsed.OperationID...)
	if len(parsed.Phases) > 0 {
		phases, err := normalisePhases(parsed.Phases)
		if err != nil {
			return config, fmt.Errorf("%s: %w", path, err)
		}
		config.Phases = phases
	}
	if parsed.Fuzz != nil {
		if parsed.Fuzz.Seed != nil {
			config.Fuzz.Seed = *parsed.Fuzz.Seed
		}
		if parsed.Fuzz.Cases != nil {
			config.Fuzz.Cases = *parsed.Fuzz.Cases
		}
		if parsed.Fuzz.WholeRequest != nil {
			config.Fuzz.WholeRequest = *parsed.Fuzz.WholeRequest
		}
	}
	if parsed.Workers != nil {
		if *parsed.Workers < 1 {
			return config, fmt.Errorf("%s: workers must be at least 1, got %d", path, *parsed.Workers)
		}
		config.Workers = *parsed.Workers
	}
	if parsed.MaxFailures != nil {
		if *parsed.MaxFailures < 0 {
			return config, fmt.Errorf("%s: max-failures must not be negative, got %d", path, *parsed.MaxFailures)
		}
		config.MaxFailures = *parsed.MaxFailures
	}
	config.ConditionalHeaders = append(config.ConditionalHeaders, toHeaderRules(parsed.Header)...)

	config.Source = path
	return config, nil
}

// toHeaderRules reads the conditional entries out of the `header` list,
// ignoring the plain strings that apply.go already took:
//
//	header:
//	  - 'X-Trace: on'                       # Dredd's form, every transaction
//	  - name: X-Mock-Scenario               # vertrag's, matched transactions
//	    value: absent
//	    when: {status: 404}
//
// A status is compared as text so `404` and `"404"` mean the same thing. YAML
// reads the first as a number, and a config failing over the quotes would be a
// silly way to lose an afternoon.
func toHeaderRules(entries []any) []HeaderRule {
	var rules []HeaderRule
	for _, entry := range entries {
		fields, ok := entry.(map[string]any)
		if !ok {
			continue
		}

		name, _ := fields["name"].(string)
		if name == "" {
			continue
		}
		rule := HeaderRule{Name: name}
		rule.Value, _ = fields["value"].(string)

		if when, ok := fields["when"].(map[string]any); ok {
			if status, present := when["status"]; present {
				rule.Status = strings.TrimSpace(fmt.Sprint(status))
			}
			rule.Method, _ = when["method"].(string)
		}
		rules = append(rules, rule)
	}
	return rules
}

// toSkipRules reads the `skip` list, whose entries may be a bare name or a
// mapping carrying the reason:
//
//	skip:
//	  - '/devices > List > 500 > application/json'
//	  - name: '/devices/{id} > Get > 404 > application/json'
//	    reason: the mock always finds the device
//
// Both forms are accepted because requiring the long one for every entry would
// push people to the short one everywhere and lose the reasons entirely.
func toSkipRules(entries []any) []SkipRule {
	var rules []SkipRule
	for _, entry := range entries {
		switch value := entry.(type) {
		case string:
			if value != "" {
				rules = append(rules, SkipRule{Name: value})
			}
		case map[string]any:
			name, _ := value["name"].(string)
			if name == "" {
				continue
			}
			reason, _ := value["reason"].(string)
			rules = append(rules, SkipRule{Name: name, Reason: reason})
		}
	}
	return rules
}

// Phase names, as written in `phases:` and --phases.
const (
	PhaseExamples = "examples"
	PhaseCoverage = "coverage"
	PhaseFuzz     = "fuzz"
	// PhaseStateful runs the chains the description's links describe.
	PhaseStateful = "stateful"
)

// normalisePhases validates and orders a phase list. Order is fixed —
// examples, coverage, fuzz — whatever order they were written in, because
// the documented transactions establish the baseline the probing phases
// judge against. Examples is always included: a run that only fuzzes is
// `vertrag fuzz`, and a config that seems to ask for one is more likely a
// mistake than an intent.
func normalisePhases(names []string) ([]string, error) {
	seen := map[string]bool{}
	for _, name := range names {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case PhaseExamples, PhaseCoverage, PhaseFuzz, PhaseStateful:
			seen[strings.ToLower(strings.TrimSpace(name))] = true
		default:
			return nil, fmt.Errorf("unknown phase %q; phases are examples, coverage, fuzz and stateful", name)
		}
	}
	out := []string{PhaseExamples}
	if seen[PhaseCoverage] {
		out = append(out, PhaseCoverage)
	}
	if seen[PhaseFuzz] {
		out = append(out, PhaseFuzz)
	}
	// Stateful last: it runs whole lifecycles and leaves the server holding
	// less than it started with, so anything judging the documented state
	// should have run already.
	if seen[PhaseStateful] {
		out = append(out, PhaseStateful)
	}
	return out, nil
}

// NormalisePhases is normalisePhases for the command line.
func NormalisePhases(names []string) ([]string, error) { return normalisePhases(names) }

// applyTransport reads the transport section, rejecting a duration it
// cannot parse rather than silently running with the default.
func applyTransport(t *Transport, parsed transportFile) error {
	if parsed.Timeout != nil {
		d, err := time.ParseDuration(*parsed.Timeout)
		if err != nil {
			return fmt.Errorf("timeout %q: %w", *parsed.Timeout, err)
		}
		t.Timeout = d
	}
	if parsed.Delay != nil {
		d, err := time.ParseDuration(*parsed.Delay)
		if err != nil {
			return fmt.Errorf("delay %q: %w", *parsed.Delay, err)
		}
		t.Delay = d
	}
	if parsed.Retries != nil {
		if *parsed.Retries < 0 {
			return fmt.Errorf("retries must not be negative, got %d", *parsed.Retries)
		}
		t.Retries = *parsed.Retries
	}
	if parsed.Insecure != nil {
		t.Insecure = *parsed.Insecure
	}
	if parsed.CACert != nil {
		t.CACert = *parsed.CACert
	}
	if parsed.Proxy != nil {
		t.Proxy = *parsed.Proxy
	}
	if parsed.Cert != nil {
		t.ClientCert = *parsed.Cert
	}
	if parsed.CertKey != nil {
		t.ClientCertKey = *parsed.CertKey
	}
	return nil
}

func applyAuth(auth *Auth, parsed authFile) {
	setString(&auth.Carry, parsed.Carry)
	setString(&auth.Cookie, parsed.Cookie)
	setString(&auth.Header, parsed.Header)
	auth.Except = append(auth.Except, parsed.Except...)

	if parsed.Login != nil {
		setString(&auth.Login.Method, parsed.Login.Method)
		setString(&auth.Login.Path, parsed.Login.Path)
		auth.Login.Body = parsed.Login.Body
		// A login is a POST unless it says otherwise; nothing else is common
		// enough to be worth making every config state it.
		if auth.Login.Method == "" && auth.Login.Path != "" {
			auth.Login.Method = "POST"
		}
	}

	if parsed.OAuth2 != nil {
		setString(&auth.OAuth2.TokenURL, parsed.OAuth2.TokenURL)
		setString(&auth.OAuth2.ClientID, parsed.OAuth2.ClientID)
		setString(&auth.OAuth2.ClientSecretEnv, parsed.OAuth2.ClientSecretEnv)
		setString(&auth.OAuth2.ClientSecret, parsed.OAuth2.ClientSecret)
		auth.OAuth2.Scopes = append(auth.OAuth2.Scopes, parsed.OAuth2.Scopes...)
	}

	// A login that captures a cookie is carrying a cookie. Making the config say
	// so twice is a chance to say it inconsistently.
	if auth.Carry == "" && auth.Cookie != "" {
		auth.Carry = "cookie"
	}
}

func apply(config *Config, parsed file) {
	// The old spelling first, so `spec` wins when a file carries both. Load
	// reports that rather than letting it be discovered by experiment.
	setString(&config.Spec, parsed.Blueprint)
	setString(&config.Spec, parsed.Spec)
	setString(&config.Endpoint, parsed.Endpoint)
	setString(&config.Language, parsed.Language)
	setString(&config.Server, parsed.Server)
	setString(&config.LogLevel, parsed.LogLevel)
	setString(&config.User, parsed.User)
	setString(&config.HooksWorkerHandlerHost, parsed.HooksWorkerHandlerHost)

	setBool(&config.Sorted, parsed.Sorted)
	setBool(&config.DryRun, parsed.DryRun)
	setBool(&config.Names, parsed.Names)
	setBool(&config.Color, parsed.Color)
	setBool(&config.InlineErrors, parsed.InlineErrors)
	setBool(&config.Details, parsed.Details)

	if parsed.HooksWorkerHandlerPort != nil {
		config.HooksWorkerHandlerPort = *parsed.HooksWorkerHandlerPort
	}

	// Dredd expresses these in seconds, and the worker ones in milliseconds.
	setDuration(&config.ServerWait, parsed.ServerWait, time.Second)
	setDuration(&config.HooksWorkerTimeout, parsed.HooksWorkerTimeout, time.Millisecond)
	setDuration(&config.HooksWorkerConnectWait, parsed.HooksWorkerConnectWait, time.Millisecond)

	config.Method = append(config.Method, parsed.Method...)
	config.Only = append(config.Only, parsed.Only...)
	// Only the plain `Name: value` strings are taken here. The conditional form
	// is vertrag's own and is applied in Load, which knows which file it came
	// from.
	config.Header = append(config.Header, toStrings(parsed.Header)...)
	config.Path = append(config.Path, parsed.Path...)
	config.Hookfiles = append(config.Hookfiles, toStrings(parsed.Hookfiles)...)

	// Dredd wrote `xunit` where vertrag writes `junit`; both are accepted so a
	// pipeline's existing configuration keeps working.
	if reporters := toStrings(parsed.Reporter); len(reporters) > 0 {
		config.Reporters = reporters
	}
	// Outputs pair with reporters by position, so an empty entry is meaningful:
	// it says "this reporter writes to stdout". Dropping empties the way
	// hookfiles does would silently shift every file to the wrong reporter.
	config.Outputs = toStringsKeepingBlanks(parsed.Output)

	if parsed.Checks != nil {
		setBool(&config.Checks.ServerError, parsed.Checks.ServerError)
		setBool(&config.Checks.ContentType, parsed.Checks.ContentType)
		setBool(&config.Checks.HeaderSchema, parsed.Checks.HeaderSchema)
		setBool(&config.Checks.IgnoredAuth, parsed.Checks.IgnoredAuth)
	}

	// Keys read into Config and then acted on by nobody. They were reported as
	// supported because the field existed, which is the worst way to be wrong
	// about it: `names: true` asked for a list of transaction names and got a
	// test run instead, and `user` asked for credentials on every request and
	// got none, both without a word.
	//
	// Listed by hand rather than derived, because "the field is never read" is
	// not something the compiler will tell us — an unused struct field is legal.
	// `require` and `custom` are `any`, so isSet can inspect them.
	for key, value := range map[string]any{
		"require": parsed.Require,
		"custom":  parsed.Custom,
	} {
		if isSet(value) {
			config.Unsupported = append(config.Unsupported, key)
		}
	}

	// The rest are typed pointers and slices, and must NOT go through isSet: a
	// nil *string put in an `any` is not the untyped nil isSet tests for — the
	// interface is non-nil and holds a nil pointer — so every one of them would
	// read as set, and a config full of `server: null` would warn about all of
	// it. Checked explicitly instead.
	for _, unsupported := range []struct {
		key string
		set bool
	}{
		{"server", parsed.Server != nil && *parsed.Server != ""},
		{"user", parsed.User != nil && *parsed.User != ""},
		{"path", len(parsed.Path) > 0},
		{"names", parsed.Names != nil && *parsed.Names},
		{"inline-errors", parsed.InlineErrors != nil && *parsed.InlineErrors},
	} {
		if unsupported.set {
			config.Unsupported = append(config.Unsupported, unsupported.key)
		}
	}

	// `loglevel` is also not acted on, but nearly every configuration carries
	// `loglevel: warning` — Dredd's own default, written out by its config
	// generator — and warning about that on every run would be noise nobody
	// reads. Only a level that was asked for on purpose is worth mentioning.
	if parsed.LogLevel != nil && *parsed.LogLevel != "" && *parsed.LogLevel != Default().LogLevel {
		config.Unsupported = append(config.Unsupported, "loglevel")
	}
}

// isSet reports whether a key carried a meaningful value. Dredd's own generated
// config writes `null` and empty collections for options left at their default,
// and reporting those as unsupported would warn about nothing.
func isSet(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	case string:
		return v != ""
	case bool:
		return v
	default:
		return true
	}
}

// toStrings accepts either a single value or a list, because Dredd's hookfiles
// key is written both ways in the wild.
func toStrings(value any) []string {
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		var out []string
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// toStringsKeepingBlanks reads a list without discarding empty entries.
func toStringsKeepingBlanks(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			text, _ := item.(string)
			out = append(out, text)
		}
		return out
	default:
		return nil
	}
}

func setString(target *string, value *string) {
	if value != nil && *value != "" {
		*target = *value
	}
}

func setBool(target *bool, value *bool) {
	if value != nil {
		*target = *value
	}
}

func setDuration(target *time.Duration, value *float64, unit time.Duration) {
	if value != nil && *value > 0 {
		*target = time.Duration(*value * float64(unit))
	}
}

// Validate reports settings that would make a run impossible.
func (c Config) Validate() error {
	if c.Spec == "" {
		return fmt.Errorf("no API description given: set `spec` in the config or pass one as an argument")
	}
	if c.Endpoint == "" {
		return fmt.Errorf("no endpoint given: set `endpoint` in the config or pass --endpoint")
	}
	if !strings.Contains(c.Endpoint, "://") {
		return fmt.Errorf("endpoint %q needs a scheme, such as http://", c.Endpoint)
	}
	return nil
}
