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

	// Source is the file these settings came from, or "" for defaults.
	Source string

	// Unsupported records configuration keys that were present and are not
	// acted on. The caller reports them, so a run never quietly ignores what
	// the user asked for.
	Unsupported []string
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

// Filenames are tried in order. A vertrag file wins over a Dredd one, so a
// project can add its own settings without touching what it already has.
var Filenames = []string{"vertrag.yml", "vertrag.yaml", "dredd.yml", "dredd.yaml"}

// Discover finds a configuration file in the working directory.
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

	Method       []string `yaml:"method"`
	Only         []string `yaml:"only"`
	Header       []string `yaml:"header"`
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
	config.Source = path
	return config, nil
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
	config.Header = append(config.Header, parsed.Header...)
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
