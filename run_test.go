package main

import (
	"testing"

	"github.com/antimatter-studios/vertrag/internal/compile"
	"github.com/antimatter-studios/vertrag/internal/config"
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
		{Name: "a", Request: compile.Request{Method: "GET"}},
		{Name: "b", Request: compile.Request{Method: "POST"}},
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
