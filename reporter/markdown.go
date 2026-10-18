package reporter

import (
	"fmt"
	"io"
	"strings"

	"github.com/antimatter-studios/vertrag/runner"
)

// Markdown writes a run as a document: the counts, a table of every
// transaction, and a section per failure.
//
// It is written by hand rather than with a library. Markdown generation is
// `fmt.Fprintf` with two escaping rules, and the packages that would do it
// instead are builders for arbitrary documents — a dependency, and a supply
// chain, in exchange for nothing this file does not already say plainly.
type Markdown struct {
	Out io.Writer
	// Title heads the document. It defaults to the tool's name.
	Title string
	// Details includes the exchange of passing transactions too, matching the
	// CLI reporter's flag.
	Details bool
	// Surface is how much of the description the run reached. It gets a
	// section of its own after the table; nil leaves it out.
	Surface *Surface
	// Authorization is the authorization phase's matrix, a section after the
	// surface; nil leaves it out.
	Authorization *Authorization
}

// Report writes the document and returns true when the run passed.
func (r Markdown) Report(results []runner.Result) bool {
	doc := newDocument(r.title(), results, r.Details)

	fmt.Fprintf(r.Out, "# %s\n\n", doc.Title)
	fmt.Fprintf(r.Out, "**%s**\n", doc.Summary.Line())

	r.table(doc)
	if r.Surface != nil {
		r.surface(*r.Surface)
	}
	if r.Authorization != nil {
		r.authorization(*r.Authorization)
	}
	for _, entry := range doc.Cases {
		r.section(entry)
	}

	return doc.Summary.Passed() && (r.Surface == nil || r.Surface.Met()) &&
		(r.Authorization == nil || r.Authorization.Met())
}

// surface is how much of the description the run reached. It comes before the
// failures, beside the table, because it answers the same question the table
// does — what did this run test — and a reader deciding whether to trust the
// table needs it first.
func (r Markdown) surface(s Surface) {
	fmt.Fprintf(r.Out, "\n## API surface\n\n")
	if s.DryRun {
		fmt.Fprintf(r.Out, "A dry run: nothing was sent, so this is what a run would send a request to.\n\n")
		fmt.Fprintf(r.Out, "| Phase | Operations |\n| --- | --- |\n")
	} else {
		fmt.Fprintf(r.Out, "| Phase | Operations | Documented statuses |\n| --- | --- | --- |\n")
	}
	for _, row := range s.Rows() {
		if s.DryRun {
			fmt.Fprintf(r.Out, "| %s | %s |\n", cell(row.Label), cell(row.Operations))
			continue
		}
		fmt.Fprintf(r.Out, "| %s | %s | %s |\n", cell(row.Label), cell(row.Operations), cell(row.Statuses))
	}

	if missed := s.OperationMisses(); len(missed) > 0 {
		fmt.Fprintf(r.Out, "\n### Operations not reached\n\n| Operation | Why |\n| --- | --- |\n")
		for _, miss := range missed {
			fmt.Fprintf(r.Out, "| %s | %s |\n", cell(miss.Operation), cell(miss.Reason))
		}
	}
	if missed := s.StatusMisses(); len(missed) > 0 {
		fmt.Fprintf(r.Out, "\n### Documented statuses not reached\n\n| Operation | Statuses | Why |\n| --- | --- | --- |\n")
		for _, miss := range missed {
			fmt.Fprintf(r.Out, "| %s | %s | %s |\n", cell(miss.Operation), cell(miss.StatusList()), cell(miss.Reason))
		}
	}
	if failed := s.NotPassed; s.CountsPassed() && len(failed) > 0 {
		fmt.Fprintf(r.Out, "\n### Operations reached that did not pass\n\n| Operation | Why |\n| --- | --- |\n")
		for _, miss := range failed {
			fmt.Fprintf(r.Out, "| %s | %s |\n", cell(miss.Operation), cell(miss.Reason))
		}
	}
	if len(s.Excluded) > 0 {
		fmt.Fprintf(r.Out, "\n### Excluded, and not counted\n\n| Operation | Reason | Written in |\n| --- | --- | --- |\n")
		for _, excluded := range s.Excluded {
			reason := excluded.Reason
			if note := excluded.Note(); note != "" {
				reason += "; " + note
			}
			fmt.Fprintf(r.Out, "| %s | %s | %s |\n", cell(excluded.Name()), cell(reason), cell(excluded.Source))
		}
	}
	if verdict := s.Verdict(); verdict != "" {
		fmt.Fprintf(r.Out, "\n**%s**\n", verdict)
	}
}

func (r Markdown) title() string {
	if r.Title != "" {
		return r.Title
	}
	return "vertrag report"
}

// table is the whole run at a glance, which is what a reader scanning a report
// they did not run wants before any individual failure.
func (r Markdown) table(doc document) {
	if len(doc.Cases) == 0 {
		return
	}

	fmt.Fprintf(r.Out, "\n| Result | Method | Transaction | Duration |\n")
	fmt.Fprintf(r.Out, "| --- | --- | --- | --- |\n")
	for _, entry := range doc.Cases {
		fmt.Fprintf(r.Out, "| %s | %s | %s | %s |\n",
			cell(entry.Status), cell(entry.Method), cell(entry.Name), cell(entry.Duration))
	}
}

// section is one transaction in full. Only transactions with something to show
// get one, so a passing run without --details is the table and nothing else.
func (r Markdown) section(entry documentCase) {
	if !entry.Detailed() {
		return
	}

	fmt.Fprintf(r.Out, "\n## %s: %s\n", entry.Status, entry.Name)

	if len(entry.Errors) > 0 {
		fmt.Fprintf(r.Out, "\n### Messages\n")
		for _, message := range entry.Errors {
			fmt.Fprintf(r.Out, "\n%s\n", block(message))
		}
	}
	// Findings Dredd would not have raised get their own heading, so an upgrade
	// is not mistaken for a regression.
	if len(entry.Beyond) > 0 {
		fmt.Fprintf(r.Out, "\n### Additional checks\n\nThese are checks Dredd does not make.\n")
		for _, message := range entry.Beyond {
			fmt.Fprintf(r.Out, "\n%s\n", block(message))
		}
	}

	if entry.Request != "" {
		fmt.Fprintf(r.Out, "\n### Request\n\n%s\n", block(entry.Request))
	}
	if entry.Response != "" {
		fmt.Fprintf(r.Out, "\n### Response\n\n%s\n", block(entry.Response))
	}
}

// block puts text in a fence long enough to survive it.
//
// A response body is arbitrary text and may well contain a fence of its own —
// an API that documents itself in Markdown returns one in every payload. A
// fixed three backticks would end the block early there and let the rest of the
// body render as prose, silently corrupting the report from that point on.
func block(text string) string {
	longest, run := 0, 0
	for _, character := range text {
		if character != '`' {
			run = 0
			continue
		}
		run++
		if run > longest {
			longest = run
		}
	}

	fence := strings.Repeat("`", max(3, longest+1))
	return fmt.Sprintf("%s\n%s\n%s", fence, text, fence)
}

// cell makes text safe to put in a table row.
//
// An unescaped pipe in a transaction name — a query string with an `|` in it is
// enough — would split the row into extra columns and shift every later cell,
// so the table would misreport which transaction failed rather than merely look
// wrong. Newlines end the row outright.
func cell(text string) string {
	replacer := strings.NewReplacer(`\`, `\\`, "|", `\|`, "\n", " ", "\r", " ")
	return replacer.Replace(text)
}
