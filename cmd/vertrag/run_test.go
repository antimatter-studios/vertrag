package main

import (
	"path/filepath"
	"strings"
	"testing"

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

func TestFilterTransactions(t *testing.T) {
	transactions := []compile.Transaction{
		{Name: "a", Request: compile.Request{Method: "GET"}, Tags: []string{"network"}},
		{Name: "b", Request: compile.Request{Method: "POST"}, Tags: []string{"network", "admin"}},
		{Name: "c", Request: compile.Request{Method: "GET"}},
	}

	for _, test := range []struct {
		name     string
		settings config.Config
		want     []string
	}{
		{"no filters keeps everything", config.Config{}, []string{"a", "b", "c"}},
		{"only", config.Config{Only: []string{"b"}}, []string{"b"}},
		{"method", config.Config{Method: []string{"get"}}, []string{"a", "c"}},
		{"both", config.Config{Only: []string{"a", "b"}, Method: []string{"POST"}}, []string{"b"}},
		{"no matches", config.Config{Only: []string{"absent"}}, nil},
		{"tag", config.Config{Tag: []string{"admin"}}, []string{"b"}},
		// Two tags widen the run the way two methods do; an untagged
		// transaction matches no tag and is left out.
		{"tags widen", config.Config{Tag: []string{"network", "admin"}}, []string{"a", "b"}},
		{"tag and method narrow together", config.Config{Tag: []string{"network"}, Method: []string{"GET"}}, []string{"a"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := filterTransactions(transactions, test.settings)
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
