package main

import (
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/fuzz"
	"github.com/antimatter-studios/vertrag/link"
)

// A setting that SELECTS — that names transactions, paths, fields or statuses
// and applies only to what it names — has a failure mode the others do not: it
// can match nothing, and matching nothing looks exactly like matching
// everything it was supposed to.
//
// Every one of those failures this project has hit was of that shape. A pin
// naming a misspelled field held nothing while reading like a safety control. A
// login path written without the base path left a freshly minted session going
// to the operation that granted it. A skip list stopped applying to the probing
// phases and fifteen operations were generated for that had been marked "must
// never be sent". None of them announced anything, and in every case the cost
// was not a wrong answer but a silent absence of testing — coverage the reader
// believed they had.
//
// Most of them do warn now, each in its own place, written the day its own bug
// was found. That is the arrangement this file exists to end: a check every
// caller performs for itself is a check the next refactor drops, which is
// precisely how `skip` was lost. Here they are one table, evaluated once, and
// the test beside this file makes the table impossible to leave incomplete.

// selector is one setting that narrows a run by naming things.
type selector struct {
	// key is the setting as it is written in vertrag.yml.
	key string

	// values are what the user named, as text, for reporting.
	values func(config.Config) []string

	// matches counts how many of the compiled transactions a value reaches.
	// A value reaching none is the whole point of this file.
	matches func(config.Config, string, []compile.Transaction) int

	// consequence completes "…, so ___" and says what the run does instead.
	// It is the sentence that turns a warning into an instruction, and it is
	// required rather than optional: a warning that does not say what happens
	// next is one a reader learns to scroll past.
	consequence string
}

// selectors is every setting that selects, and the test beside this file
// asserts it is every one — see TestEveryConfigFieldIsClassified.
var selectors = []selector{
	{
		key: "tenants.ids",
		values: func(c config.Config) []string {
			var values []string
			for _, tenant := range slices.Sorted(maps.Keys(c.TenantIDs)) {
				for _, key := range slices.Sorted(maps.Keys(c.TenantIDs[tenant])) {
					values = append(values, tenant+": "+key)
				}
			}
			return values
		},
		matches: func(_ config.Config, value string, transactions []compile.Transaction) int {
			_, key, _ := strings.Cut(value, ": ")
			matched := 0
			for _, transaction := range transactions {
				if slices.ContainsFunc(transaction.Request.Parameters, func(p compile.Parameter) bool {
					return link.Match(key, p.In, p.Name)
				}) {
					matched++
				}
			}
			return matched
		},
		consequence: "no request is sent with the id, and the tenant's resources it meant stay unaddressed",
	},
	{
		key: "wait-for.name",
		values: func(c config.Config) []string {
			var names []string
			for _, wait := range c.WaitFor {
				names = append(names, wait.Name)
			}
			return names
		},
		matches:     byTransactionName,
		consequence: "no stream is held open for its event, and the event is not checked for",
	},
	{
		key: "wait-for.trigger",
		values: func(c config.Config) []string {
			var names []string
			for _, wait := range c.WaitFor {
				names = append(names, nonEmpty(wait.Trigger)...)
			}
			return names
		},
		matches:     byTransactionName,
		consequence: "nothing is sent to provoke the event, and the stream waits for it all the same",
	},
	{
		key:         "skip",
		values:      func(c config.Config) []string { return skipNames(c) },
		matches:     byTransactionName,
		consequence: "the operation it names will run and be probed like any other",
	},
	{
		key:         "auth.except",
		values:      func(c config.Config) []string { return c.Auth.Except },
		matches:     byExceptName,
		consequence: "the transaction it names will be sent authenticated",
	},
	{
		key:    "auth.login.path",
		values: func(c config.Config) []string { return nonEmpty(c.Auth.Login.Path) },
		matches: func(c config.Config, value string, transactions []compile.Transaction) int {
			matched := 0
			for _, transaction := range transactions {
				if pathOf(transaction.Request.URI) == value {
					matched++
				}
			}
			return matched
		},
		consequence: "the operation that grants the credential will be sent it like any other, " +
			"and a server may read that as already authenticated",
	},
	{
		key:    "fuzz.pin",
		values: func(c config.Config) []string { return fuzz.Pins(c.Fuzz.Pin).Names() },
		matches: func(c config.Config, value string, transactions []compile.Transaction) int {
			bodies, arguments := pinnable(transactions)
			pins := fuzz.Pins{value: c.Fuzz.Pin[value]}
			matched := 0
			for _, body := range bodies {
				if pins.Covers(body, value) {
					matched++
				}
			}
			for _, argument := range arguments {
				if argument == value {
					matched++
				}
			}
			return matched
		},
		consequence: "nothing is held and generation will send whatever the schema permits",
	},
	{
		key: "principals.auth.except",
		values: func(c config.Config) []string {
			var values []string
			for _, principal := range c.Principals {
				for _, name := range principal.Auth.Except {
					values = append(values, principal.Name+": "+name)
				}
			}
			return values
		},
		matches: func(c config.Config, value string, transactions []compile.Transaction) int {
			for _, principal := range c.Principals {
				for _, name := range principal.Auth.Except {
					if value == principal.Name+": "+name {
						return byExceptName(c, name, transactions)
					}
				}
			}
			return 0
		},
		consequence: "the principal's credential will not be withheld from the intended transaction",
	},
	{
		key: "principals.auth.login.path",
		values: func(c config.Config) []string {
			var values []string
			for _, principal := range c.Principals {
				if principal.Auth.Login.Path != "" {
					values = append(values, principal.Name+": "+principal.Auth.Login.Path)
				}
			}
			return values
		},
		matches: func(c config.Config, value string, transactions []compile.Transaction) int {
			for _, principal := range c.Principals {
				if value != principal.Name+": "+principal.Auth.Login.Path {
					continue
				}
				matched := 0
				for _, transaction := range transactions {
					if pathOf(transaction.Request.URI) == principal.Auth.Login.Path {
						matched++
					}
				}
				return matched
			}
			return 0
		},
		consequence: "the principal's credential will not be withheld from the operation that granted it",
	},
	{
		key: "surface.exclude",
		values: func(c config.Config) []string {
			names := make([]string, 0, len(c.Surface.Exclude))
			for _, exclusion := range c.Surface.Exclude {
				names = append(names, exclusionKey(exclusion))
			}
			return names
		},
		matches: func(c config.Config, value string, transactions []compile.Transaction) int {
			for _, exclusion := range c.Surface.Exclude {
				if exclusionKey(exclusion) == value {
					return exclusionReach(exclusion, transactions)
				}
			}
			return 0
		},
		consequence: "nothing is excluded, and the operation it meant still counts against the run's surface",
	},
	{
		key:    "header (conditional)",
		values: func(c config.Config) []string { return conditionalHeaderNames(c) },
		matches: func(c config.Config, value string, transactions []compile.Transaction) int {
			matched := 0
			for _, rule := range c.ConditionalHeaders {
				if conditionalHeaderName(rule) != value {
					continue
				}
				for _, transaction := range transactions {
					if rule.Status != "" && rule.Status != strings.TrimSpace(transaction.Response.Status) {
						continue
					}
					if rule.Method != "" && !strings.EqualFold(rule.Method, transaction.Request.Method) {
						continue
					}
					matched++
				}
			}
			return matched
		},
		consequence: "the header is never sent",
	},
}

// skipNames is the names a skip list refers to, which are written either bare
// or as a mapping carrying the reason.
func skipNames(c config.Config) []string {
	names := make([]string, 0, len(c.Skip))
	for _, rule := range c.Skip {
		names = append(names, rule.Name)
	}
	return names
}

// conditionalHeaderNames identifies each conditional header by name and
// condition, because two rules for one header name are ordinary — that is how a
// header takes a different value per status.
func conditionalHeaderNames(c config.Config) []string {
	names := make([]string, 0, len(c.ConditionalHeaders))
	for _, rule := range c.ConditionalHeaders {
		names = append(names, conditionalHeaderName(rule))
	}
	return names
}

func conditionalHeaderName(rule config.HeaderRule) string {
	name := rule.Name
	if rule.Status != "" {
		name += " when status " + rule.Status
	}
	if rule.Method != "" {
		name += " when method " + rule.Method
	}
	return name
}

func byTransactionName(_ config.Config, value string, transactions []compile.Transaction) int {
	matched := 0
	for _, transaction := range transactions {
		if transaction.Name == value {
			matched++
		}
	}
	return matched
}

// byExceptName matches an `auth.except` entry with exceptedTransactions, the
// function a run withholds the credential by, so doctor cannot call an entry
// dead that the run applies, or the other way round.
func byExceptName(_ config.Config, value string, transactions []compile.Transaction) int {
	except, _ := exceptedTransactions([]string{value}, transactions)
	return len(except)
}

func pathOf(uri string) string {
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		return uri[:i]
	}
	return uri
}

func nonEmpty(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return []string{value}
}

// inert is one setting value that reaches nothing.
type inert struct {
	key         string
	value       string
	consequence string
}

// inertSelectors reports every selecting value that matches no transaction.
//
// It answers one question — "is this configuration doing what it says?" —
// against the compiled transactions and without sending anything, which is what
// makes it usable before a run rather than as an explanation afterwards.
func inertSelectors(settings config.Config, transactions []compile.Transaction) []inert {
	var found []inert
	for _, s := range selectors {
		for _, value := range s.values(settings) {
			if s.matches(settings, value, transactions) == 0 {
				found = append(found, inert{key: s.key, value: value, consequence: s.consequence})
			}
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].key != found[j].key {
			return found[i].key < found[j].key
		}
		return found[i].value < found[j].value
	})
	return found
}

// reportDoctor writes what the configuration will and will not reach.
//
// It prints the silent things loudly and the working things briefly, because a
// preflight that lists everything is one nobody reads twice — and this exists
// to be read by somebody who has just been told their suite found nothing.
func reportDoctor(w io.Writer, settings config.Config, transactions []compile.Transaction) bool {
	fmt.Fprintf(w, "%d transaction(s) compiled from %s\n", len(transactions), settings.Spec)

	dead := inertSelectors(settings, transactions)
	if len(dead) == 0 {
		fmt.Fprintln(w, "\nEvery setting that names something reaches something.")
	} else {
		fmt.Fprintf(w, "\n%d setting(s) reach nothing:\n", len(dead))
		for _, d := range dead {
			fmt.Fprintf(w, "  %s: %q matches no transaction, so %s\n", d.key, d.value, d.consequence)
		}
	}

	// Two things that are not "matches nothing" but are the same question —
	// will this do what its writer thinks — and neither is visible from a run
	// until it is too late to matter.
	var notes []string
	if staged := stagedConditionalHeaders(settings); len(staged) > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d conditional header(s) are keyed on a documented status, so they reach the "+
				"documented requests and NOT the generated ones. A header that tells a mock "+
				"which response to serve would otherwise decide what a probe was judged by",
			len(staged)))
	}
	if settings.Auth.Configured() && !settings.Checks.IgnoredAuth {
		notes = append(notes, "this run authenticates and --check-ignored-auth is off, so nothing "+
			"tests whether the endpoints actually require the credential. It doubles the requests; "+
			"at one project it found 56 of 117 endpoints answering without one")
	}
	if len(notes) > 0 {
		fmt.Fprintln(w, "\nWorth knowing:")
		for _, note := range notes {
			fmt.Fprintf(w, "  %s\n", note)
		}
	}

	return len(dead) > 0
}

// stagedConditionalHeaders are the conditional headers keyed on a status, which
// by construction apply to documented requests only.
func stagedConditionalHeaders(settings config.Config) []config.HeaderRule {
	var staged []config.HeaderRule
	for _, rule := range settings.ConditionalHeaders {
		if rule.Status != "" {
			staged = append(staged, rule)
		}
	}
	return staged
}

// runDoctor is `vertrag doctor`: read the configuration and the description,
// say what the settings will and will not reach, and send nothing.
//
// It exists because every one of these answers was already available before a
// run and none of them was reachable. They were scattered across warnings that
// appear part-way through a wall of output, at the moment somebody is reading
// results rather than checking a configuration — so a suite could report a
// clean run while a skip list, a pin and a login exclusion all reached nothing,
// and the person reading it had no way to ask.
//
// Sending no API requests is the point rather than an economy. The question "is this
// configuration doing what it says" should be answerable against an API that is
// not running, from a laptop, before anybody has decided to trust a result.
// A description supplied as a URL is fetched, just as run and compile fetch it.
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to a vertrag.yml (default: the first found here)")

	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}

	settings, err := resolveConfig(*configPath, positional)
	if err != nil {
		return err
	}
	if settings.Spec == "" {
		return fmt.Errorf("no API description given: set `spec` in the config or name one")
	}

	source, err := readDescription(settings.Spec)
	if err != nil {
		return err
	}
	result, _, err := namedTransactionsFor(source, settings.Spec, settings)
	if err != nil {
		return err
	}

	// Reported rather than refused, and the exit code says which.
	//
	// A setting reaching nothing is a defect in the configuration, not in this
	// command, so `doctor` succeeding would be the wrong answer — a pipeline
	// that runs it wants to fail on it. Exit 1 keeps that available without
	// making it an error in the ordinary sense.
	if reportDoctor(os.Stdout, settings, result.Transactions) {
		return errFailed
	}
	return nil
}

// exclusionKey names a `surface.exclude` entry the way doctor lists it: the
// operation, and the status when the entry has one.
func exclusionKey(exclusion config.SurfaceExclusion) string {
	if exclusion.Status == "" {
		return exclusion.Operation
	}
	return strings.TrimSpace(exclusion.Operation + " status " + exclusion.Status)
}
