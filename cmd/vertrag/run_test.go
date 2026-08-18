package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
)

// TestStripAPIName pins the name hooks actually see.
//
// The compiler builds "Title > /path > Operation > 200", which is what Dredd's
// compiler produces and what `vertrag compile` reports. Dredd's runner then
// drops the title, and the shortened name is what hook files address
// transactions by. Getting this wrong does not fail loudly — every named hook in
// an existing project simply stops matching, which is how it was found.
func TestStripAPIName(t *testing.T) {
	transactions := []compile.Transaction{
		{
			Name:   "inPACE 2.0 > /api/v1/auth/login > Login > 200 > application/json",
			Origin: compile.Origin{APIName: "inPACE 2.0"},
		},
		{
			// A path that repeats the title keeps its own copy: only the
			// leading occurrence is removed.
			Name:   "API > /api > Read > 200",
			Origin: compile.Origin{APIName: "API"},
		},
		{
			// Nothing to strip when the description has no title.
			Name:   "/things > List > 200",
			Origin: compile.Origin{},
		},
	}

	want := []string{
		"/api/v1/auth/login > Login > 200 > application/json",
		"/api > Read > 200",
		"/things > List > 200",
	}

	for i, transaction := range stripAPIName(transactions) {
		if transaction.Name != want[i] {
			t.Errorf("name %d = %q, want %q", i, transaction.Name, want[i])
		}
	}
}

// orders is a transaction named the way real ones are, so the regular
// expression rows can search inside a name rather than match all of a
// one-letter one — which would pass whether the match is a search or a
// comparison and prove nothing about which it is.
const orders = "/orders > List orders > 200 > application/json"

func TestFilterTransactions(t *testing.T) {
	transactions := []compile.Transaction{
		{Name: "a", Request: compile.Request{Method: "GET"}, Tags: []string{"network"}, OperationID: "listThings"},
		{Name: "b", Request: compile.Request{Method: "POST"}, Tags: []string{"network", "admin"}, OperationID: "createThing"},
		{Name: "c", Request: compile.Request{Method: "GET"}},
		{Name: orders, Request: compile.Request{Method: "GET"}, Tags: []string{"orders"}, OperationID: "listOrders"},
	}

	for _, test := range []struct {
		name     string
		settings config.Config
		want     []string
	}{
		{"no filters keeps everything", config.Config{}, []string{"a", "b", "c", orders}},
		{"only", config.Config{Only: []string{"b"}}, []string{"b"}},
		{"method", config.Config{Method: []string{"get"}}, []string{"a", "c", orders}},
		{"both", config.Config{Only: []string{"a", "b"}, Method: []string{"POST"}}, []string{"b"}},
		{"no matches", config.Config{Only: []string{"absent"}}, nil},
		{"tag", config.Config{Tag: []string{"admin"}}, []string{"b"}},
		// Two tags widen the run the way two methods do; an untagged
		// transaction matches no tag and is left out.
		{"tags widen", config.Config{Tag: []string{"network", "admin"}}, []string{"a", "b"}},
		{"tag and method narrow together", config.Config{Tag: []string{"network"}, Method: []string{"GET"}}, []string{"a"}},
		{"operation-id", config.Config{OperationID: []string{"createThing"}}, []string{"b"}},
		{"operation-ids widen", config.Config{OperationID: []string{"listThings", "createThing"}}, []string{"a", "b"}},
		// A transaction with no operationId matches no named one.
		{"operation-id misses the unnamed", config.Config{OperationID: []string{"noSuchOp"}}, nil},

		// A pattern searches the name rather than being compared with the
		// whole of it, so one word of a long generated name is enough — and a
		// pattern that does mean the whole name says so with ^ and $.
		{"only-matching searches inside the name", config.Config{OnlyMatching: []string{"orders"}}, []string{orders}},
		{"only-matching anchors when asked", config.Config{OnlyMatching: []string{"^b$"}}, []string{"b"}},
		{"only-matching widens", config.Config{OnlyMatching: []string{"^a$", "^b$"}}, []string{"a", "b"}},
		{"only-matching narrows with method", config.Config{OnlyMatching: []string{"^[ab]$"}, Method: []string{"GET"}}, []string{"a"}},

		{"exclude", config.Config{Exclude: []string{"b"}}, []string{"a", "c", orders}},
		{"exclude-matching", config.Config{ExcludeMatching: []string{"orders"}}, []string{"a", "b", "c"}},
		{"exclude-method is case-insensitive like method", config.Config{ExcludeMethod: []string{"get"}}, []string{"b"}},
		{"exclude-tag", config.Config{ExcludeTag: []string{"network"}}, []string{"c", orders}},
		{"exclude-operation-id", config.Config{ExcludeOperationID: []string{"listThings"}}, []string{"b", "c", orders}},
		// An untagged transaction carries no tag to exclude, so exclude-tag
		// leaves it in — the mirror of a tag include leaving it out.
		{"exclude-tag keeps the untagged", config.Config{ExcludeTag: []string{"orders"}}, []string{"a", "b", "c"}},

		// An exclude wins over every include, because the two are written
		// together and the exclude is the more specific half of the pair.
		{"an exclude beats a named include", config.Config{Only: []string{"a", "b"}, Exclude: []string{"b"}}, []string{"a"}},
		{"an exclude beats a tag include", config.Config{Tag: []string{"network"}, ExcludeMethod: []string{"POST"}}, []string{"a"}},
		{"an exclude beats a matching include", config.Config{OnlyMatching: []string{"^[abc]$"}, ExcludeMatching: []string{"^b$"}}, []string{"a", "c"}},
		{"an exclude may empty the run", config.Config{Only: []string{"b"}, Exclude: []string{"b"}}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, _, err := filterTransactions(transactions, test.settings)
			if err != nil {
				t.Fatalf("filterTransactions: %v", err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("kept %d, want %d", len(got), len(test.want))
			}
			for i, name := range test.want {
				if got[i].Name != name {
					t.Errorf("kept[%d] = %q, want %q", i, got[i].Name, name)
				}
			}
		})
	}
}

// TestAnInvalidPatternStopsTheRunAndNamesItself pins that a regular expression
// which does not compile is an error rather than a filter that matches nothing.
//
// The silently-inert filter is the failure this project keeps having to fix. An
// include matching nothing runs no transactions and reads as an API with
// nothing to test; an exclude matching nothing sends every request the pattern
// was written to prevent, which is the one that reaches a real server. Neither
// mentions the typo, so the pattern has to be in the message.
func TestAnInvalidPatternStopsTheRunAndNamesItself(t *testing.T) {
	for _, test := range []struct {
		key      string
		settings config.Config
	}{
		{"only-matching", config.Config{OnlyMatching: []string{"orders(unclosed"}}},
		{"exclude-matching", config.Config{ExcludeMatching: []string{"orders(unclosed"}}},
	} {
		t.Run(test.key, func(t *testing.T) {
			_, _, err := filterTransactions(nil, test.settings)
			if err == nil {
				t.Fatal("a pattern that does not compile should stop the run")
			}
			if !strings.Contains(err.Error(), "orders(unclosed") {
				t.Errorf("the error should quote the pattern: %v", err)
			}
			if !strings.Contains(err.Error(), test.key) {
				t.Errorf("the error should name the option it came from: %v", err)
			}
		})
	}
}

// TestAFilterValueThatMatchesNothingIsReported pins the same treatment an
// unmatched `skip` entry gets, for the same reason: a value that matches
// nothing is nearly always a typo or a transaction that was renamed, and its
// effect is a run that does something other than what the configuration reads
// as. The two directions are opposites and say so — an unmatched include tests
// less than was asked for, an unmatched exclude sends what was written down to
// be kept back.
func TestAFilterValueThatMatchesNothingIsReported(t *testing.T) {
	transactions := []compile.Transaction{
		{Name: "a", Request: compile.Request{Method: "GET"}, Tags: []string{"network"}, OperationID: "listThings"},
	}

	_, unmatched, err := filterTransactions(transactions, config.Config{
		Only:            []string{"a", "renamed"},
		Tag:             []string{"network"},
		Exclude:         []string{"gone"},
		ExcludeTag:      []string{"admn"},
		ExcludeMatching: []string{"^nothing$"},
	})
	if err != nil {
		t.Fatalf("filterTransactions: %v", err)
	}

	reported := strings.Join(unmatched, "\n")
	for _, want := range []string{
		`only has no transaction named "renamed"`,
		`exclude has no transaction named "gone"`,
		`exclude-tag has no transaction carrying the tag "admn"`,
		`exclude-matching has no transaction whose name matches "^nothing$"`,
	} {
		if !strings.Contains(reported, want) {
			t.Errorf("no report says %s:\n%s", want, reported)
		}
	}

	// The values that did match are not reported, or the reports would be
	// noise nobody reads.
	if strings.Contains(reported, `named "a"`) || strings.Contains(reported, "network") {
		t.Errorf("a value that matched was reported as unmatched:\n%s", reported)
	}
	if !strings.Contains(reported, "narrower") || !strings.Contains(reported, "wider") {
		t.Errorf("the reports should say which way the run was moved:\n%s", reported)
	}
}

// TestAFilterIsNotBlamedForWhatAnotherFilterDropped pins that every filter is
// tested against every transaction, rather than the loop stopping as soon as
// one of them has settled the verdict.
//
// `--only a --exclude-tag admin` is the case: only `a` survives, and the
// transaction carrying the admin tag is dropped by `only` before the tag filter
// is reached. Stop early and the tag is reported as matching nothing — a false
// alarm about the filter that was working perfectly well, which teaches the
// reader to ignore the report that matters.
func TestAFilterIsNotBlamedForWhatAnotherFilterDropped(t *testing.T) {
	transactions := []compile.Transaction{
		{Name: "a", Request: compile.Request{Method: "GET"}, Tags: []string{"network"}},
		{Name: "b", Request: compile.Request{Method: "POST"}, Tags: []string{"admin"}},
	}

	kept, unmatched, err := filterTransactions(transactions, config.Config{
		Only: []string{"a"}, ExcludeTag: []string{"admin"},
	})
	if err != nil {
		t.Fatalf("filterTransactions: %v", err)
	}
	if len(kept) != 1 || kept[0].Name != "a" {
		t.Errorf("kept = %v, want just a", names(kept))
	}
	if len(unmatched) != 0 {
		t.Errorf("nothing should be reported as unmatched: %v", unmatched)
	}
}

func TestHasErrors(t *testing.T) {
	if hasErrors([]compile.Annotation{{Type: "warning"}}) {
		t.Error("warnings alone are not errors")
	}
	if !hasErrors([]compile.Annotation{{Type: "warning"}, {Type: "error"}}) {
		t.Error("an error anywhere should be reported")
	}
}

func TestToAnnotations(t *testing.T) {
	converted := toAnnotations([]compile.Annotation{
		{Type: "warning", Message: "m", Location: [][]int{{7, 3}, {7, 9}}},
		{Type: "error", Message: "no location"},
	})

	if converted[0].Line != 7 || converted[0].Column != 3 {
		t.Errorf("position = %d:%d, want 7:3", converted[0].Line, converted[0].Column)
	}
	if converted[1].Line != 0 {
		t.Error("an annotation without a location should report none")
	}
}

// TestEveryDocumentedReporterCanBeBuilt pins that the names the help text offers
// are the names this switch accepts.
//
// A reporter that exists but is not wired up fails at the worst moment: the run
// itself is fine, and the pipeline that asked for a report file gets an error
// instead of one. The names are listed here rather than derived so that adding a
// reporter without offering it, or offering one that was renamed, fails.
func TestEveryDocumentedReporterCanBeBuilt(t *testing.T) {
	for _, name := range []string{"cli", "", "dot", "markdown", "md", "html", "junit", "xunit"} {
		settings := config.Config{Reporters: []string{name}, Outputs: []string{filepath.Join(t.TempDir(), "report")}}
		if _, closeReport, err := newReporter(settings); err != nil {
			t.Errorf("reporter %q should be available: %v", name, err)
		} else {
			closeReport()
		}
	}
}

// TestAnUnknownReporterIsRefusedByName pins that a typo stops the run rather
// than silently producing no report — a suite whose report never appeared looks
// exactly like a suite that was never run.
func TestAnUnknownReporterIsRefusedByName(t *testing.T) {
	_, closeReport, err := newReporter(config.Config{Reporters: []string{"nyan"}})
	closeReport()

	if err == nil {
		t.Fatal("an unknown reporter should be refused")
	}
	if !strings.Contains(err.Error(), "nyan") || !strings.Contains(err.Error(), "markdown") {
		t.Errorf("the error should name what was asked for and what is available: %v", err)
	}
}

// TestSortedGroupsByMethod pins the option that was accepted, stored and then
// ignored.
//
// `sorted` existed in the config struct and was set by both the flag and the
// YAML key, and nothing ever read it. A project that set it got document order
// anyway — worse than an unsupported option, which at least says so.
func TestSortedGroupsByMethod(t *testing.T) {
	transactions := []compile.Transaction{
		{Name: "delete", Request: compile.Request{Method: "DELETE"}},
		{Name: "get", Request: compile.Request{Method: "GET"}},
		{Name: "post", Request: compile.Request{Method: "POST"}},
	}

	unsorted := sortTransactions(transactions, false)
	if unsorted[0].Name != "delete" {
		t.Errorf("without the option the order should be untouched, got %s first", unsorted[0].Name)
	}

	// Dredd's order: POST before GET so the thing being fetched exists, and
	// DELETE last so it does not remove what the others need.
	sorted := sortTransactions(transactions, true)
	for i, want := range []string{"post", "get", "delete"} {
		if sorted[i].Name != want {
			t.Errorf("sorted[%d] = %s, want %s", i, sorted[i].Name, want)
		}
	}
}

// TestSortedIsStable pins that transactions sharing a method keep document
// order, so two POSTs that must run in sequence still do.
func TestSortedIsStable(t *testing.T) {
	transactions := []compile.Transaction{
		{Name: "first", Request: compile.Request{Method: "POST"}},
		{Name: "second", Request: compile.Request{Method: "POST"}},
		{Name: "third", Request: compile.Request{Method: "POST"}},
	}
	for i, want := range []string{"first", "second", "third"} {
		if got := sortTransactions(transactions, true)[i].Name; got != want {
			t.Errorf("sorted[%d] = %s, want %s", i, got, want)
		}
	}
}

// TestSortedPutsUnknownMethodsLast pins that a method Dredd's list does not
// name sorts after every one it does, rather than sharing a bucket with
// CONNECT because both rank zero.
func TestSortedPutsUnknownMethodsLast(t *testing.T) {
	transactions := []compile.Transaction{
		{Name: "odd", Request: compile.Request{Method: "PURGE"}},
		{Name: "get", Request: compile.Request{Method: "GET"}},
	}
	if got := sortTransactions(transactions, true)[0].Name; got != "get" {
		t.Errorf("sorted[0] = %s, want get", got)
	}
}

// TestTransportFlagsOverrideTheFile pins the merge rule for the network
// knobs: a flag that was given wins, one that was not leaves the file's value
// alone — including a zero given on purpose, so `--retries 0` can switch off
// what the file turned on.
func TestTransportFlagsOverrideTheFile(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/vertrag.yml"
	os.WriteFile(cfg, []byte("spec: "+dir+"/api.yml\nendpoint: http://localhost:1\ntransport:\n  timeout: 10s\n  retries: 3\n  delay: 1s\n"), 0o600)
	os.WriteFile(dir+"/api.yml", []byte("openapi: 3.0.0\ninfo: {title: T, version: '1'}\npaths: {}\n"), 0o600)

	flags, err := parseRunFlags([]string{"--config", cfg, "--retries", "0", "--timeout", "5s"})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := settingsFor(flags)
	if err != nil {
		t.Fatal(err)
	}
	tr := settings.Transport
	if tr.Retries != 0 {
		t.Errorf("--retries 0 did not override the file's 3: %d", tr.Retries)
	}
	if tr.Timeout != 5*time.Second {
		t.Errorf("--timeout 5s did not override the file's 10s: %s", tr.Timeout)
	}
	if tr.Delay != time.Second {
		t.Errorf("delay was not given as a flag and should keep the file's 1s: %s", tr.Delay)
	}
}

// TestTheClientCertificateFlagsReachEveryCommandThatSends: a certificate is
// only of use to a command that opens a connection, and the three that do share
// one definition of these flags so they cannot drift apart. Each is still asked
// for itself, because a flag can be perfectly implemented in the runner and
// misspelled, unwired, or parsed into the wrong variable by the command, with
// every unit test in the tree staying green.
//
// The certificate named does not exist, which is the reply that arrives before
// any request — so no server is needed to prove the flag was carried.
func TestTheClientCertificateFlagsReachEveryCommandThatSends(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.pem")

	commands := map[string]func([]string) error{
		"run":      runRun,
		"fuzz":     runFuzz,
		"coverage": runCoverage,
	}
	for name, command := range commands {
		t.Run(name, func(t *testing.T) {
			description := filepath.Join(t.TempDir(), "api.yml")
			if err := os.WriteFile(description, []byte(parameterisedAPI), 0o600); err != nil {
				t.Fatal(err)
			}

			real := os.Stdout
			devNull, _ := os.Open(os.DevNull)
			os.Stdout = devNull
			err := command([]string{
				"--endpoint", "https://127.0.0.1:1", "--no-color",
				"--cert", missing, "--cert-key", missing, description,
			})
			os.Stdout = real
			devNull.Close()

			if err == nil {
				t.Fatalf("%s accepted a certificate that cannot be read", name)
			}
			if !strings.Contains(err.Error(), "client certificate") {
				t.Errorf("%s did not fail on the client certificate: %v", name, err)
			}
		})
	}
}

// TestFailFastIsMaxFailuresOne pins the alias, and that an explicit
// --max-failures is what a reader expects to win when both are given.
func TestFailFastIsMaxFailuresOne(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/api.yml", []byte("openapi: 3.0.0\ninfo: {title: T, version: '1'}\npaths: {}\n"), 0o600)
	os.WriteFile(dir+"/vertrag.yml", []byte("spec: "+dir+"/api.yml\nendpoint: http://localhost:1\n"), 0o600)

	flags, _ := parseRunFlags([]string{"--config", dir + "/vertrag.yml", "--fail-fast"})
	settings, err := settingsFor(flags)
	if err != nil {
		t.Fatal(err)
	}
	if settings.MaxFailures != 1 {
		t.Errorf("--fail-fast: MaxFailures = %d, want 1", settings.MaxFailures)
	}

	flags, _ = parseRunFlags([]string{"--config", dir + "/vertrag.yml", "--max-failures", "5"})
	settings, _ = settingsFor(flags)
	if settings.MaxFailures != 5 {
		t.Errorf("--max-failures 5: MaxFailures = %d", settings.MaxFailures)
	}
}
