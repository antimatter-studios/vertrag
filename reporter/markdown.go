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
}

// Report writes the document and returns true when the run passed.
func (r Markdown) Report(results []runner.Result) bool {
	doc := newDocument(r.title(), results, r.Details)

	fmt.Fprintf(r.Out, "# %s\n\n", doc.Title)
	fmt.Fprintf(r.Out, "**%s**\n", doc.Summary.Line())

	r.table(doc)
	for _, entry := range doc.Cases {
		r.section(entry)
	}

	return doc.Summary.Passed()
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
