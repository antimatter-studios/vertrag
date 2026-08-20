package link

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/validate"
)

// Checking a link is checking the description, and needs nothing else.
//
// A Link Object is the only thing in OpenAPI that asserts a relation between
// two operations: the value AT THIS PLACE in my response IS that operation's
// parameter. It is a written claim, so testing it is the same job as testing
// that a response matches its schema — and, unlike a lifecycle, it needs no
// fixture database, no create-and-delete pair and no assumption about what the
// server stores. It needs one response the description already promises.
//
// It was reachable only through the stateful phase, which needs all of that.
// So the check that reads the document's own words sat behind the check that
// invents claims the document does not make (see statefulcmd.go), and a
// project that wanted the first had to accept the second and a server that
// could survive it.
//
// What it is worth, from the run that prompted this: a project's `POST /roles`
// declared a link to renameRole, and the link could not resolve. The fault was
// not in the link. Create role answered 400 where its own description promises
// success, because the schema says `name: string` while the handler demands
// alphanumeric — a genuine defect in the description, found by nothing but an
// unfollowable link. That is the shape this reports: a claim that did not
// hold, said in terms of the claim rather than of the response.

// Finding is a claim a Link Object makes that did not hold.
//
// Message is a whole sentence naming the link, because it is what a report
// carries and a report line has no room for context. The other fields are for
// a terminal, which prints them as its own lines.
type Finding struct {
	// Source is the transaction whose response declared the link.
	Source string
	// Link is the name the description gave it, which is the only handle a
	// reader has on it.
	Link string
	// Target is the transaction the link leads to, empty when it names none
	// this description defines.
	Target  string
	Message string
}

// Unchecked is a link nothing could be said about, and why.
//
// It is deliberately not a Finding. A link whose source operation never
// produced the response it is declared on has not been shown to be wrong; it
// has not been tested, and reporting the two as one thing would put the blame
// for a failed operation on the link that follows it. Saying so out loud is
// the point: a link check that silently tested nothing reads exactly like one
// that found nothing.
type Unchecked struct {
	Source string
	Link   string
	// Reason completes "could not be checked: …" and is about the source
	// operation, not about the link.
	Reason string
}

// Report is what checking a description's links found.
type Report struct {
	// Checked is how many links were resolved against a real response, which
	// is the number the reader needs to know what the findings are out of.
	Checked   int
	Findings  []Finding
	Unchecked []Unchecked
}

// Observed is what a source operation's own transaction did.
type Observed struct {
	// Exchange is the completed request and response the link's runtime
	// expressions are resolved against.
	Exchange Exchange

	// Missing says why there is nothing to resolve against — the transaction
	// was skipped, could not be sent, or answered something other than the
	// response that carries the link. Empty means Exchange is usable.
	Missing string
}

// Check resolves every link a description declares against what its source
// operation actually answered.
//
// `running` is what this run holds and `observed` is keyed by index into it.
// `defined` is every transaction the description yields, which is not the same
// list: a run narrowed by --only or --tag tests some of them, and a link
// naming an operation the RUN left out is emphatically not a link naming an
// operation the DOCUMENT lacks. Reporting the first as the second would have
// `--tag orders` produce a page of findings about a description that is
// perfectly consistent. nil means the two lists are the same.
//
// Ordering is the document's: links are visited in transaction order and, for
// one transaction, in the order Values sorts them, so two runs of the same
// description report the same things in the same order. A report that shuffled
// would be undiffable against yesterday's.
func Check(running, defined []compile.Transaction, observed map[int]Observed) Report {
	if defined == nil {
		defined = running
	}
	byOperation, byPathMethod := targetIndex(defined)

	var report Report
	for source, transaction := range running {
		for _, l := range transaction.Links {
			targets, note := resolveTarget(l, byOperation, byPathMethod)
			if note != "" {
				// False on the document alone: no server was needed to know
				// that the operation named does not exist. It is reported as
				// a finding here and stays a note in Build, which is ordering
				// a run rather than judging a description.
				report.Findings = append(report.Findings, Finding{
					Source: transaction.Name, Link: l.Name, Message: note})
				continue
			}

			seen, ran := observed[source]
			if !ran || seen.Missing != "" {
				reason := seen.Missing
				if !ran {
					reason = "it did not run"
				}
				report.Unchecked = append(report.Unchecked, Unchecked{
					Source: transaction.Name, Link: l.Name, Reason: reason})
				continue
			}

			report.Checked++
			report.Findings = append(report.Findings,
				checkLink(l, transaction, defined[targets[0]], seen.Exchange)...)
		}
	}
	return report
}

// checkLink asks the two questions a link's own words permit: does the value
// it names exist, and is it something the target would accept.
//
// One target, though resolveTarget may name several. An operation compiles to
// one transaction per documented response, and a link names the OPERATION — so
// its targets differ in what they answer and agree in every part a link fills
// in, which is the request. Judging each of them would say the same thing
// twice about one claim.
func checkLink(l compile.Link, source compile.Transaction, target compile.Transaction, exchange Exchange) []Finding {
	var findings []Finding
	report := func(format string, arguments ...any) {
		findings = append(findings, Finding{
			Source: source.Name, Link: l.Name, Target: target.Name,
			Message: "link " + l.Name + " " + fmt.Sprintf(format, arguments...),
		})
	}

	values, unresolved := Values(l, exchange)
	sort.Strings(unresolved)
	for _, name := range unresolved {
		report("claims %s is %q, and the response of %s carries no such value",
			name, l.Parameters[name], source.Name)
	}

	if l.RequestBody != "" {
		if body, resolved := Evaluate(l.RequestBody, exchange); !resolved {
			report("claims the body of %s is %q, and the response of %s carries no such value",
				target.Name, l.RequestBody, source.Name)
		} else if len(target.Request.Schema) > 0 {
			// The body the link supplies is judged against the schema the
			// target declares for its own body, which is the same comparison
			// the runner makes of a response — a claim about a value against
			// the description of that value.
			if encoded, err := json.Marshal(body); err == nil {
				field := validate.AgainstSchema(json.RawMessage(target.Request.Schema), string(encoded))
				for _, reason := range field.Errors {
					report("supplies a body %s would refuse: %s", target.Name, reason)
				}
			}
		}
	}

	for _, name := range sortedKeys(values) {
		matched := false
		for _, parameter := range target.Request.Parameters {
			if !Match(name, parameter.In, parameter.Name) {
				continue
			}
			matched = true
			if parameter.Schema == "" {
				continue
			}
			// The value is judged as the target would receive it: as text.
			// stringify is what the sequencer puts into the request, so the
			// two cannot disagree about what a resolved 42 is.
			text := stringify(values[name])
			for _, reason := range validate.AgainstTextValue(json.RawMessage(parameter.Schema), text) {
				report("supplies %s's %s parameter %s, which %s",
					target.Name, parameter.In, parameter.Name, reason)
			}
		}
		if !matched {
			// The same condition the sequencer stops a step on, reported as
			// what it is: the link names a parameter of an operation that has
			// no such parameter, which is a contradiction between two parts of
			// one document.
			report("supplies %s, and %s declares no parameter of that name", name, target.Name)
		}
	}
	return findings
}

func sortedKeys(values map[string]any) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
