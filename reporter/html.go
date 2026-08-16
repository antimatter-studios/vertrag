package reporter

import (
	"html/template"
	"io"

	"github.com/antimatter-studios/vertrag/runner"
)

// HTML writes a run as a self-contained page: the counts, a table of every
// transaction, and a section per failure.
//
// Self-contained means one file with its styling inside it and nothing fetched
// at open time. A report is read from a CI artefact store, an email attachment
// or a laptop with no network, and one that needs a stylesheet from somewhere
// else is unreadable in all three.
//
// Dredd builds its HTML by rendering the Markdown report through markdown-it.
// That is not done here: a Markdown renderer passes embedded HTML through by
// design, so a response body containing a <script> tag — which is an ordinary
// thing for an API to return — would end up live in the report. Rendering from
// the same model through html/template instead escapes everything by
// construction, and costs a dependency less.
type HTML struct {
	Out io.Writer
	// Title heads the page. It defaults to the tool's name.
	Title string
	// Details includes the exchange of passing transactions too, matching the
	// CLI reporter's flag.
	Details bool
}

// Report writes the page and returns true when the run passed.
func (r HTML) Report(results []runner.Result) bool {
	doc := newDocument(r.title(), results, r.Details)
	htmlTemplate.Execute(r.Out, doc)
	return doc.Summary.Passed()
}

func (r HTML) title() string {
	if r.Title != "" {
		return r.Title
	}
	return "vertrag report"
}

// htmlTemplate is parsed once, at start-up, so a mistake in it is a panic on
// the first run rather than a report that comes out half-written after a suite
// has already been executed.
var htmlTemplate = template.Must(template.New("report").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root { color-scheme: light dark; --line: #d6d9de; --quiet: #6b7280; --ground: #ffffff; --ink: #1f2328; --panel: #f6f7f9; }
@media (prefers-color-scheme: dark) {
  :root { --line: #33383f; --quiet: #9aa2ad; --ground: #16181c; --ink: #e6e8eb; --panel: #1e2126; }
}
body { margin: 0 auto; padding: 2rem 1.25rem 4rem; max-width: 60rem; background: var(--ground); color: var(--ink);
       font: 16px/1.55 -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif; }
h1 { font-size: 1.6rem; margin: 0 0 1rem; }
h2 { font-size: 1.1rem; margin: 2rem 0 0.5rem; padding-top: 1.25rem; border-top: 1px solid var(--line); }
h3 { font-size: 0.85rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--quiet); margin: 1.25rem 0 0.4rem; }
.summary { font-weight: 600; }
.summary.fail { color: #b42318; }
.summary.pass { color: #0a7d3f; }
table { border-collapse: collapse; width: 100%; margin: 1.5rem 0; font-size: 0.9rem; }
th, td { text-align: left; padding: 0.4rem 0.6rem; border-bottom: 1px solid var(--line); vertical-align: top; }
th { font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--quiet); }
td.duration { text-align: right; white-space: nowrap; color: var(--quiet); }
.badge { display: inline-block; min-width: 3.4rem; text-align: center; padding: 0.05rem 0.4rem; border-radius: 0.25rem;
         font-size: 0.75rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em; color: #fff; background: var(--quiet); }
.badge.pass { background: #0a7d3f; }
.badge.fail, .badge.error { background: #b42318; }
.badge.skip { background: #b45309; }
code, pre { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 0.83rem; }
pre { background: var(--panel); border: 1px solid var(--line); border-radius: 0.35rem;
      padding: 0.7rem 0.85rem; overflow-x: auto; white-space: pre-wrap; word-break: break-word; }
pre.message { background: none; border: none; border-left: 3px solid #b42318; border-radius: 0; padding: 0.1rem 0 0.1rem 0.85rem; }
.label { color: var(--quiet); }
</style>
</head>
<body>
<h1>{{.Title}}</h1>
<p class="summary {{if .Summary.Passed}}pass{{else}}fail{{end}}">{{.Summary.Line}}</p>
{{if .Cases}}
<table>
<thead><tr><th>Result</th><th>Method</th><th>Transaction</th><th>Duration</th></tr></thead>
<tbody>
{{range .Cases}}<tr><td><span class="badge {{.Status}}">{{.Status}}</span></td><td>{{.Method}}</td><td>{{.Name}}</td><td class="duration">{{.Duration}}</td></tr>
{{end}}</tbody>
</table>
{{end}}
{{range .Cases}}{{if .Detailed}}
<section>
<h2><span class="badge {{.Status}}">{{.Status}}</span> {{.Name}}</h2>
{{range .Errors}}<pre class="message">{{.}}</pre>
{{end}}{{range .Beyond}}<pre class="message"><span class="label">[additional check]</span> {{.}}</pre>
{{end}}{{with .Request}}<h3>Request</h3>
<pre>{{.}}</pre>
{{end}}{{with .Response}}<h3>Response</h3>
<pre>{{.}}</pre>
{{end}}</section>
{{end}}{{end}}
</body>
</html>
`))
