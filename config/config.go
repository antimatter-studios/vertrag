// Package config reads vertrag's configuration.
//
// The file is `vertrag.yml`, and it is a superset of Dredd's `dredd.yml`: every
// key Dredd understands means the same thing here, and vertrag's own keys are
// added alongside. A `dredd.yml` is still read when no vertrag file is present,
// so adopting vertrag needs no change at all — and renaming the file is then
// the whole of the migration.
//
// Options vertrag does not act on are accepted and reported rather than
// rejected: a project that Dredd runs should not fail here over a key that only
// affects output.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is a test run's settings.
type Config struct {
	// Blueprint is the API description document to test against. Dredd calls it
	// a blueprint whatever the format, and the key is kept for compatibility.
	Blueprint string
	Endpoint  string

	// Hookfiles are loaded by a language-specific worker, except for Go, which
	// has no hooks yet.
	Hookfiles []string
	Language  string

	// Server is a command to start before testing, and ServerWait how long to
	// wait for it to listen.
	Server     string
	ServerWait time.Duration

	Method       []string
	Only         []string
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

	// Auth logs the run in once and carries the result on every request. It is
	// read only from a vertrag file — see Discover.
	Auth Auth

	// Skip takes transactions out of the run. Read only from a vertrag file.
	Skip []SkipRule

	// ConditionalHeaders are the `header` entries written in vertrag's
	// conditional form. Read only from a vertrag file.
	ConditionalHeaders []HeaderRule

	// Source is the file these settings came from, or "" for defaults.
	Source string

	// Unsupported records configuration keys that were present and are not
	// acted on. The caller reports them, so a run never quietly ignores what
	// the user asked for.
	Unsupported []string

	// Notes are messages about the configuration to print verbatim. Unsupported
	// says "vertrag cannot do this yet", which is the wrong thing to say about
	// a key that works perfectly well from the right file.
	Notes []string
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
	return a.Login.Path != "" || a.Header != ""
}

// Filenames are tried in order. A vertrag file wins over a Dredd one, so a
// project can add its own settings without touching what it already has.
var Filenames = []string{"vertrag.yml", "vertrag.yaml", "dredd.yml", "dredd.yaml"}

// Discover finds a configuration file in the working directory.
//
// A vertrag file and a Dredd file are alternatives, not layers: the first found
// is the whole configuration and the other is not read. Merging them would mean
// a key's effect depended on a file it never mentions, and a setting silently
// outranked from somewhere else is expensive to diagnose. A project running both
// testers keeps a file per tester, each complete on its own.
//
// Reading dredd.yml is a migration convenience with an expected end, not a
// second supported format. vertrag's own keys go only in a vertrag file, and as
// the two formats diverge the fallback is expected to be removed — so anything
// written here should be read as "still works for now".
func Discover() string {
	for _, name := range Filenames {
		if _, err := os.Stat(name); err == nil {
			return name
		}
	}
	return ""
}

// IsDreddFile reports whether a path names a Dredd configuration, which the
// caller mentions so the reader knows a rename is available.
func IsDreddFile(path string) bool {
	base := filepath.Base(path)
	return base == "dredd.yml" || base == "dredd.yaml"
}

// file mirrors dredd.yml. Every field is a pointer or slice so an absent key can
// be told from one explicitly set to a zero value — `color: false` means
// something different from no `color` key at all.
type file struct {
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
	// Skip is `any` because an entry may be written either way — see toSkipRules.
	Skip []any `yaml:"skip"`
}

// authFile is the `auth` section. Dredd has no equivalent: authenticating a
// suite there means a hook file, a worker process and a language runtime to run
// it in, to do what is nearly always the same three steps — log in once, keep
// what came back, send it on everything after.
type authFile struct {
	Login  *loginFile `yaml:"login"`
	Carry  *string    `yaml:"carry"`
	Cookie *string    `yaml:"cookie"`
	Header *string    `yaml:"header"`
	Except []string   `yaml:"except"`
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

// Load reads a dredd.yml, layering it over the defaults.
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

	// vertrag's own keys are honoured only from a vertrag file. Read out of a
	// dredd.yml, `auth` would authenticate vertrag's run and not Dredd's — and
	// Dredd ignores keys it does not recognise without a word — so a project
	// running both would have the two quietly testing different things.
	var own []string
	if parsed.Auth != nil {
		own = append(own, "`auth`")
	}
	if len(parsed.Skip) > 0 {
		own = append(own, "`skip`")
	}
	conditional := toHeaderRules(parsed.Header)
	if len(conditional) > 0 {
		own = append(own, "the conditional entries in `header`")
	}

	switch {
	case len(own) == 0:
	case IsDreddFile(path):
		config.Notes = append(config.Notes, fmt.Sprintf(
			"%s in %s %s ignored: keys that change what is sent or run are read "+
				"only from a vertrag.yml, because Dredd ignores keys it does not "+
				"know without a word, and the two testers would then disagree about "+
				"what they tested from one file that looks shared. Move them to a "+
				"vertrag.yml, which may hold everything %s does.",
			strings.Join(own, " and "), path, plural(len(own), "is", "are"), path))
	default:
		if parsed.Auth != nil {
			applyAuth(&config.Auth, *parsed.Auth)
		}
		config.Skip = append(config.Skip, toSkipRules(parsed.Skip)...)
		config.ConditionalHeaders = append(config.ConditionalHeaders, conditional...)
	}

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

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
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

	// A login that captures a cookie is carrying a cookie. Making the config say
	// so twice is a chance to say it inconsistently.
	if auth.Carry == "" && auth.Cookie != "" {
		auth.Carry = "cookie"
	}
}

func apply(config *Config, parsed file) {
	setString(&config.Blueprint, parsed.Blueprint)
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
	}

	for key, value := range map[string]any{
		"require": parsed.Require,
		"custom":  parsed.Custom,
	} {
		if isSet(value) {
			config.Unsupported = append(config.Unsupported, key)
		}
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
	if c.Blueprint == "" {
		return fmt.Errorf("no API description given: set `blueprint` in the config or pass one as an argument")
	}
	if c.Endpoint == "" {
		return fmt.Errorf("no endpoint given: set `endpoint` in the config or pass --endpoint")
	}
	if !strings.Contains(c.Endpoint, "://") {
		return fmt.Errorf("endpoint %q needs a scheme, such as http://", c.Endpoint)
	}
	return nil
}
