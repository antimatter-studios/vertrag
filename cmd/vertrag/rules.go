package main

import (
	"context"
	"fmt"
	"os"

	"github.com/antimatter-studios/vertrag/auth"
	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
)

// signature names what is about to run, on stderr, before anything is sent.
//
// It is not optional and there is no flag for it, because the information is
// worth exactly the runs nobody expected to have to explain. A flag would be
// added after the confusing run rather than before it, which is when it is
// needed: a CI log reporting 74 transactions where there should be 172 is
// explained at a glance by the version that printed it, and nobody would have
// asked for that line in advance.
//
// The endpoint and the configuration file are here for the same reason. Two
// afternoons went into "which config was read" and "which server answered" on a
// project where two hubs share a port and only one is the mock, and neither
// question was answerable from the report.
//
// stderr rather than stdout so the report stays the only thing on stdout. That
// is not the same as hidden — a consumer writing `2>&1 | …` sees it — but every
// diagnostic vertrag already prints goes the same way, and a line that begins
// with the program's name cannot be mistaken for a transaction line by anything
// anchoring on `^pass: METHOD `.
//
// The joining lives in the reporter package because a cassette carries the same
// line in its header, and a recording whose header disagrees with the terminal
// it was produced at is the kind of small contradiction that costs an hour.
func signature(settings config.Config) string {
	return provenance(settings).Summary()
}

// provenance is the one place the four values that identify a run are put
// together, so that the signature line on the terminal and the properties in
// a JUnit report cannot say different things. The version is main's alone —
// it is stamped into this package at build time — which is why the reporter
// package is handed the value rather than asked to find it.
func provenance(settings config.Config) reporter.Provenance {
	return reporter.Provenance{
		Version:  version,
		Spec:     settings.Spec,
		Endpoint: settings.Endpoint,
		Config:   settings.Source,
	}
}

// applyConfiguredRules puts the settings that shape requests onto an engine: the
// credential, the conditional headers and the skip list.
//
// Both commands need them. `fuzz` in particular cannot do without the
// credential: an authenticated API would answer 401 to every generated case, so
// the baseline check would find no transaction worth probing and the run would
// report having tested nothing rather than that it never got in.
func applyConfiguredRules(
	ctx context.Context,
	engine *runner.Runner,
	settings config.Config,
	transactions []compile.Transaction,
) error {
	// Authenticating happens before hooks start, so a project keeping its own
	// login hook is unaffected, and before the first request, so nothing is sent
	// unauthenticated by accident.
	if settings.Auth.Configured() {
		credential, err := auth.Obtain(ctx, engine.Client, settings.Endpoint, settings.Auth)
		if err != nil {
			return err
		}
		except, unmatched := exceptedTransactions(settings.Auth.Except, transactions)
		// An `except` entry matching nothing is nearly always a typo, and its
		// effect is to authenticate a transaction that was meant to go without —
		// which fails later with a message about the API rather than the config.
		for _, name := range unmatched {
			fmt.Fprintf(os.Stderr,
				"vertrag: auth.except has no transaction named %q; it will be sent authenticated\n", name)
		}
		engine.Auth = runner.Credential{
			Header: credential, Except: except,
			LoginMethod: settings.Auth.Login.Method,
			LoginPath:   settings.Auth.Login.Path,
		}
	}

	for _, rule := range settings.ConditionalHeaders {
		engine.ConditionalHeaders = append(engine.ConditionalHeaders, runner.ConditionalHeader{
			Name: rule.Name, Value: rule.Value, Status: rule.Status, Method: rule.Method,
		})
	}

	if len(settings.Skip) > 0 {
		skips, unmatched := configuredSkips(settings.Skip, transactions)
		// An unmatched skip is worth more noise than an unmatched except: it
		// usually means a transaction was renamed, and the entry now protects
		// nothing while still reading like a decision somebody made.
		for _, name := range unmatched {
			fmt.Fprintf(os.Stderr,
				"vertrag: skip has no transaction named %q; it matches nothing and will not run\n", name)
		}
		engine.Skip = skips
	}

	engine.MaxFailures = settings.MaxFailures

	return nil
}

// withoutSkipped removes the skipped transactions from a list, returning the
// names removed.
//
// Runner.Skip is honoured by Run, which probing does not use — it calls Send
// directly, one generated body at a time. So the skip list has to be applied
// here as well, or `skip` would work in one command and quietly do nothing in
// the other.
func withoutSkipped(transactions []compile.Transaction, skips map[string]string) ([]compile.Transaction, []string) {
	if len(skips) == 0 {
		return transactions, nil
	}

	kept := make([]compile.Transaction, 0, len(transactions))
	var removed []string
	for _, transaction := range transactions {
		if _, skipped := skips[transaction.Name]; skipped {
			removed = append(removed, transaction.Name)
			continue
		}
		kept = append(kept, transaction)
	}
	return kept, removed
}

// configuredSkips turns the configured skip rules into a name-to-reason map, and
// returns the names that matched no transaction.
func configuredSkips(rules []config.SkipRule, transactions []compile.Transaction) (map[string]string, []string) {
	present := transactionNames(transactions)

	skips := make(map[string]string, len(rules))
	var unmatched []string
	for _, rule := range rules {
		if !present[rule.Name] {
			unmatched = append(unmatched, rule.Name)
			continue
		}
		skips[rule.Name] = rule.Reason
	}
	return skips, unmatched
}

// exceptedTransactions turns the configured exception names into a set, and
// returns the names that matched no transaction.
//
// Names are matched exactly, the same way `only` matches them, so a name that
// works in one works in the other. Two filters in one file that looked alike and
// matched differently would be worse than either.
func exceptedTransactions(names []string, transactions []compile.Transaction) (map[string]bool, []string) {
	if len(names) == 0 {
		return nil, nil
	}
	present := transactionNames(transactions)

	except := make(map[string]bool, len(names))
	var unmatched []string
	for _, name := range names {
		if !present[name] {
			unmatched = append(unmatched, name)
			continue
		}
		except[name] = true
	}
	return except, unmatched
}

func transactionNames(transactions []compile.Transaction) map[string]bool {
	names := make(map[string]bool, len(transactions))
	for _, transaction := range transactions {
		names[transaction.Name] = true
	}
	return names
}
