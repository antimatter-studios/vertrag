package reporter

import (
	"fmt"
	"io"
	"strings"

	"github.com/antimatter-studios/vertrag/runner"
)

// Authorization is the authorization phase's matrix: every selected operation
// against every configured principal, with what the description requires of
// each cell and what became of it.
//
// It sits beside the results rather than replacing them. Each sent cell is an
// ordinary runner.Result too, so the table, the failures and the recordings
// already cover it; the matrix adds the one view those cannot give, which is
// who may do what, side by side.
type Authorization struct {
	Operations []string            `json:"operations"`
	Principals []string            `json:"principals"`
	Cells      []AuthorizationCell `json:"cells"`
}

// AuthorizationVerdict is what became of one cell.
type AuthorizationVerdict string

const (
	AuthorizationPass  AuthorizationVerdict = "pass"
	AuthorizationFail  AuthorizationVerdict = "fail"
	AuthorizationError AuthorizationVerdict = "error"
	// AuthorizationUnsent is a cell nobody checked: a safety skip, an
	// unsupported credential, a hook, a cancelled run. It is never a pass.
	AuthorizationUnsent AuthorizationVerdict = "unsent"
)

// AuthorizationCell is one operation sent, or not sent, as one principal.
type AuthorizationCell struct {
	Operation string `json:"operation"`
	Principal string `json:"principal"`
	// Expected is the status the description requires: the documented
	// success, 401 or 403. Empty when the description does not decide it.
	Expected string `json:"expected,omitempty"`
	// Actual is the status that came back; empty when nothing did.
	Actual  string               `json:"actual,omitempty"`
	Verdict AuthorizationVerdict `json:"verdict"`
	// Bypass marks success where the description requires a refusal.
	Bypass bool `json:"bypass,omitempty"`
	// Reason is why a cell failed, errored or was not sent.
	Reason string `json:"reason,omitempty"`
	// Replay repeats a failing request, credentials redacted.
	Replay string `json:"replay,omitempty"`
}

// AuthorizationVerdictOf is the verdict of a judged result. A cell that was
// not sent is unsent whatever its status says, a skip is unsent, and a status
// this package does not know is an error: nothing becomes a pass by default.
func AuthorizationVerdictOf(status runner.Status, unsent bool) AuthorizationVerdict {
	switch {
	case unsent || status == runner.StatusSkip:
		return AuthorizationUnsent
	case status == runner.StatusPass:
		return AuthorizationPass
	case status == runner.StatusFail:
		return AuthorizationFail
	}
	return AuthorizationError
}

// normal is the cell as every report shows it.
//
// The rules a cell must keep are enforced here, once, rather than trusted to
// whoever built it: a bypass is a failure; a pass needs an expectation and a
// status that came back, or nobody checked it; an unknown verdict is an
// error. And every free-text field has the run's registered credentials
// redacted, sanitizing or not, so a reason quoting a body or a replay quoting a query string
// cannot carry one out.
func (c AuthorizationCell) normal() AuthorizationCell {
	switch c.Verdict {
	case AuthorizationPass, AuthorizationFail, AuthorizationError, AuthorizationUnsent:
	default:
		c.Verdict, c.Reason = AuthorizationError, strings.TrimSpace("unknown verdict "+string(c.Verdict)+"; "+c.Reason)
	}
	if c.Bypass {
		c.Verdict = AuthorizationFail
	}
	if c.Verdict == AuthorizationPass && (c.Expected == "" || c.Actual == "") {
		c.Verdict = AuthorizationUnsent
		if c.Reason == "" {
			c.Reason = "nothing was checked"
		}
	}
	c.Reason = printable(redactRegistered(c.Reason))
	c.Replay = printable(redactRegistered(c.Replay))
	c.Actual = printable(redactRegistered(c.Actual))
	c.Expected = printable(c.Expected)
	return c
}

// Label is the cell's text in the grid.
func (c AuthorizationCell) Label() string {
	switch c.Verdict {
	case AuthorizationPass:
		return "pass " + c.Actual
	case AuthorizationFail:
		label := "FAIL " + c.Actual + " (want " + c.Expected + ")"
		if c.Bypass {
			label += " BYPASS"
		}
		return label
	case AuthorizationError:
		return "ERROR"
	}
	return "unsent"
}

// AuthorizationCounts are the matrix's totals.
type AuthorizationCounts struct {
	Cells    int `json:"cells"`
	Passing  int `json:"passing"`
	Failing  int `json:"failing"`
	Errors   int `json:"errors"`
	Unsent   int `json:"unsent"`
	Bypasses int `json:"bypasses"`
}

// Normal is the matrix as every report shows it: each cell normalised, in
// operation then principal order, with a cell the plan left out shown as
// unsent rather than as nothing.
func (a Authorization) Normal() Authorization {
	byKey := make(map[[2]string]AuthorizationCell, len(a.Cells))
	for _, each := range a.Cells {
		byKey[[2]string{each.Operation, each.Principal}] = each.normal()
	}
	// Names come from the description and the config; neither may move a
	// terminal's cursor or smuggle a control character into a file.
	out := Authorization{Operations: []string{}, Principals: []string{}, Cells: []AuthorizationCell{}}
	for _, name := range a.Operations {
		out.Operations = append(out.Operations, printable(name))
	}
	for _, name := range a.Principals {
		out.Principals = append(out.Principals, printable(name))
	}
	for _, operation := range a.Operations {
		for _, principal := range a.Principals {
			each, ok := byKey[[2]string{operation, principal}]
			if !ok {
				each = AuthorizationCell{Verdict: AuthorizationUnsent, Reason: "no cell was planned"}
			}
			each.Operation, each.Principal = printable(operation), printable(principal)
			out.Cells = append(out.Cells, each)
		}
	}
	return out
}

// Counts totals the normalised matrix.
func (a Authorization) Counts() AuthorizationCounts {
	var counts AuthorizationCounts
	for _, each := range a.Normal().Cells {
		counts.Cells++
		switch each.Verdict {
		case AuthorizationPass:
			counts.Passing++
		case AuthorizationFail:
			counts.Failing++
		case AuthorizationError:
			counts.Errors++
		default:
			counts.Unsent++
		}
		if each.Bypass {
			counts.Bypasses++
		}
	}
	return counts
}

// Met says the matrix holds no failure and no error. Unsent cells do not fail
// it — they are counted and shown, as a skipped transaction is — but they
// never count as passing.
func (a Authorization) Met() bool {
	counts := a.Counts()
	return counts.Failing == 0 && counts.Errors == 0
}

// Verdict is the matrix's one-line summary.
func (a Authorization) Verdict() string {
	c := a.Counts()
	line := fmt.Sprintf("authorization: %d cells, %d passing, %d failing, %d errors, %d unsent",
		c.Cells, c.Passing, c.Failing, c.Errors, c.Unsent)
	if c.Bypasses > 0 {
		line += fmt.Sprintf("; %d authorization bypasses", c.Bypasses)
	}
	return line
}

// Grid is the matrix as rows of cells, one row per operation, one column per
// principal.
func (a Authorization) Grid() [][]AuthorizationCell {
	normal := a.Normal()
	rows := make([][]AuthorizationCell, 0, len(normal.Operations))
	for i := range normal.Operations {
		width := len(normal.Principals)
		rows = append(rows, normal.Cells[i*width:(i+1)*width])
	}
	return rows
}

// Findings are the cells that did not pass, in matrix order.
func (a Authorization) Findings() []AuthorizationCell {
	var out []AuthorizationCell
	for _, each := range a.Normal().Cells {
		if each.Verdict != AuthorizationPass {
			out = append(out, each)
		}
	}
	return out
}

// WithAuthorization hands the matrix to every reporter that renders one, as
// WithSurface does the surface.
func WithAuthorization(r Reporter, matrix *Authorization) Reporter {
	switch r := r.(type) {
	case Multi:
		out := make(Multi, len(r))
		for i, each := range r {
			out[i] = WithAuthorization(each, matrix)
		}
		return out
	case JUnit:
		r.Authorization = matrix
		return r
	case Markdown:
		r.Authorization = matrix
		return r
	case HTML:
		r.Authorization = matrix
		return r
	case JSON:
		r.Authorization = matrix
		return r
	}
	return r
}

// WriteAuthorization writes the matrix a run ends with on the terminal.
func WriteAuthorization(w io.Writer, a Authorization, color bool) {
	normal := a.Normal()
	fmt.Fprint(w, "\nAuthorization matrix\n\n")

	operation := len("operation")
	for _, name := range normal.Operations {
		operation = max(operation, len(printable(name)))
	}
	widths := make([]int, len(normal.Principals))
	for i, name := range normal.Principals {
		widths[i] = len(printable(name))
	}
	grid := a.Grid()
	for _, row := range grid {
		for i, each := range row {
			widths[i] = max(widths[i], len(each.Label()))
		}
	}

	fmt.Fprintf(w, "  %-*s", operation, "operation")
	for i, name := range normal.Principals {
		fmt.Fprintf(w, "  %-*s", widths[i], printable(name))
	}
	fmt.Fprintln(w)
	for r, row := range grid {
		fmt.Fprintf(w, "  %-*s", operation, printable(normal.Operations[r]))
		for i, each := range row {
			label := fmt.Sprintf("%-*s", widths[i], printable(each.Label()))
			fmt.Fprint(w, "  "+paint(color, verdictColour(each.Verdict), label))
		}
		fmt.Fprintln(w)
	}

	if findings := a.Findings(); len(findings) > 0 {
		fmt.Fprint(w, "\n  Cells that did not pass:\n")
		for _, each := range findings {
			fmt.Fprintf(w, "    %s as %s: %s", printable(each.Operation), printable(each.Principal), each.Verdict)
			if each.Reason != "" {
				fmt.Fprintf(w, ": %s", printable(each.Reason))
			}
			fmt.Fprintln(w)
			if each.Replay != "" {
				fmt.Fprintf(w, "      replay: %s\n", printable(each.Replay))
			}
		}
	}

	code := green
	if !a.Met() {
		code = red
	}
	fmt.Fprintf(w, "\n  %s\n", paint(color, code, a.Verdict()))
}

func verdictColour(verdict AuthorizationVerdict) string {
	switch verdict {
	case AuthorizationPass:
		return green
	case AuthorizationUnsent:
		return yellow
	}
	return red
}

// markdownText is text safe in a Markdown table cell: cell's escaping, and the
// characters that would otherwise open HTML in a renderer that allows it.
func markdownText(text string) string {
	text = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "`", "&#96;").Replace(printable(text))
	return cell(text)
}

// markdownFence is a code fence longer than any run of backticks in text, so
// a replay cannot close its own block.
func markdownFence(text string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
			continue
		}
		run = 0
	}
	return strings.Repeat("`", max(3, longest+1))
}

// authorization is the Markdown section for the matrix.
func (r Markdown) authorization(a Authorization) {
	normal := a.Normal()
	fmt.Fprint(r.Out, "\n## Authorization matrix\n\n| Operation |")
	for _, name := range normal.Principals {
		fmt.Fprintf(r.Out, " %s |", markdownText(name))
	}
	fmt.Fprint(r.Out, "\n| --- |")
	for range normal.Principals {
		fmt.Fprint(r.Out, " --- |")
	}
	fmt.Fprintln(r.Out)
	for i, row := range a.Grid() {
		fmt.Fprintf(r.Out, "| %s |", markdownText(normal.Operations[i]))
		for _, each := range row {
			fmt.Fprintf(r.Out, " %s |", markdownText(each.Label()))
		}
		fmt.Fprintln(r.Out)
	}
	fmt.Fprintf(r.Out, "\n**%s**\n", markdownText(a.Verdict()))

	findings := a.Findings()
	if len(findings) == 0 {
		return
	}
	fmt.Fprint(r.Out, "\n### Cells that did not pass\n\n| Operation | Principal | Verdict | Why |\n| --- | --- | --- | --- |\n")
	for _, each := range findings {
		fmt.Fprintf(r.Out, "| %s | %s | %s | %s |\n", markdownText(each.Operation), markdownText(each.Principal),
			markdownText(string(each.Verdict)), markdownText(each.Reason))
	}
	for _, each := range findings {
		if each.Replay == "" {
			continue
		}
		fence := markdownFence(each.Replay)
		fmt.Fprintf(r.Out, "\nReplay %s as %s:\n\n%ssh\n%s\n%s\n", markdownText(each.Operation),
			markdownText(each.Principal), fence, printable(each.Replay), fence)
	}
}

// properties adds the matrix to the suite's properties: its totals, then one
// property per cell, named for the cell and valued with its verdict.
func (a Authorization) properties(existing *junitProperties) *junitProperties {
	if existing == nil {
		existing = &junitProperties{}
	}
	add := func(name, value string) {
		existing.Properties = append(existing.Properties, junitProperty{Name: name, Value: printable(value)})
	}
	c := a.Counts()
	add("vertrag.authorization.cells", fmt.Sprint(c.Cells))
	add("vertrag.authorization.passing", fmt.Sprint(c.Passing))
	add("vertrag.authorization.failing", fmt.Sprint(c.Failing))
	add("vertrag.authorization.errors", fmt.Sprint(c.Errors))
	add("vertrag.authorization.unsent", fmt.Sprint(c.Unsent))
	add("vertrag.authorization.bypasses", fmt.Sprint(c.Bypasses))
	for _, each := range a.Normal().Cells {
		value := string(each.Verdict)
		if each.Expected != "" {
			value += " expected " + each.Expected
		}
		if each.Actual != "" {
			value += " got " + each.Actual
		}
		if each.Bypass {
			value += " bypass"
		}
		if each.Reason != "" {
			value += ": " + each.Reason
		}
		add("vertrag.authorization.cell["+each.Operation+" as "+each.Principal+"]", value)
	}
	return existing
}
