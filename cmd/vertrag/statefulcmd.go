package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/link"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
	"os"
)

// The stateful phase runs each documented chain of operations end to end —
// create, read what was created, delete it — and asks the two questions no
// single request can.
//
//   - Resource availability: a create that answered success, followed by the
//     link the description declares to read it back, must find it. A server
//     that answers 201 and stores nothing passes both operations separately
//     and fails only here.
//   - Use after free: once a delete has succeeded, following a read link to
//     the same resource must NOT find it. A server that keeps serving a
//     deleted resource passes every request it is sent and is wrong about
//     the one thing the sequence establishes.
//
// Both are properties of the sequence rather than of any request in it, which
// is why the ordinary phases cannot reach them however many values they draw.
//
// # Both are inferred, and now say so
//
// Everything else vertrag reports is a claim the description itself makes: a
// status, a schema, a content type, a link. These two are not. OpenAPI has no
// grammar for "a resource stops being readable once it has been deleted" — no
// keyword, no extension, nothing an author can write to affirm it or to deny
// it — so the assertion belongs to vertrag, drawn from what DELETE and GET
// mean in RFC 9110, and it cannot be falsified from the document under test.
//
// That matters because this codebase refuses exactly that move everywhere
// else. The possessed-ID exemption in fuzz/ and the login exemption in
// refusals.go both turn on one sentence: a description promises which values
// are WELL FORMED, not which resources exist. A phase asserting what the
// server must be HOLDING is on the far side of that line, and it was reporting
// its conclusions in the same voice as a schema violation.
//
// They are kept, because they find defects nothing else can — a resource that
// outlives its delete passes every documented transaction there is — and an
// assertion deleted finds nothing at all. What has changed is that each
// finding now carries the assumption it rests on, in the report as well as on
// the terminal, so a reader who disputes the inference can say which inference
// they dispute instead of hunting for the rule that produced it. The defect
// was never the check; it was an unfalsifiable claim printed as though the
// description had made it.
//
// The other half of what this phase used to be the only route to has been
// taken out of it. Resolving a link's runtime expressions is reading the
// description's own words back to it, so it now runs on every `vertrag run`,
// with no lifecycle, no fixtures and no server that has to survive being
// knocked about — see linkcheck.go. That it lived here is how a check with no
// assumptions in it came to be reachable only behind two.

// statefulOutcome is one chain, run.
type statefulOutcome struct {
	Chain    link.Chain
	Findings []statefulFinding
	// Steps is how many steps actually ran; a chain stops at the first step
	// that could not be prepared or did not answer as documented.
	Steps int
	// Stopped explains why the chain ended early, empty when it ran through.
	Stopped string
}

// statefulFinding is a property of a sequence that did not hold.
type statefulFinding struct {
	Step int
	Name string
	// Message is what happened. Basis is the assumption that makes it a
	// finding, which is not in the description and has to travel with it —
	// see the note above.
	Message string
	Basis   string
	Status  string
	Request runner.Request
}

// The assumptions the two findings rest on, written out for the reader rather
// than left implicit in the code that decided.
//
// They are phrased as what vertrag assumed and where the assumption came from,
// because that is what someone disputing a finding needs: an author who
// intends a soft delete that keeps serving reads has not violated their
// description, and should be able to see that in one line rather than deduce
// it. Neither sentence can be written in OpenAPI, which is the whole of why
// they are here.
const (
	deletedStaysGone = "inferred, not documented: OpenAPI cannot state that a deleted resource stops " +
		"being readable, so this rests on what DELETE means in RFC 9110 rather than on anything this " +
		"description says"
	createdIsThere = "inferred, not documented: the description declares the link, not that following " +
		"it must find something, so this rests on what a successful create means rather than on " +
		"anything this description says"
)

// runStateful executes every chain and reports.
func runStateful(
	ctx context.Context,
	engine *runner.Runner,
	transactions []compile.Transaction,
	color bool,
) ([]runner.Result, error) {
	chains := link.Chains(transactions)
	if len(chains) == 0 {
		// Why there are no chains matters, and this used to assert the wrong
		// reason.
		//
		// "No operation links to another" is a claim about the description, and
		// it is false when the description declares a link that could not be
		// resolved — a target naming an operationId nothing defines, say. The
		// reader was then told to go and declare the links they had already
		// declared, while the sequencer's own explanation sat in a slice
		// nobody read. A peer was about to walk into exactly that: their
		// generator emits links from an annotation, and until recently not one
		// of their 77 operations carried an operationId, so every link they
		// declared would have dangled and this would have said they had none.
		notes := link.NewSequencer(transactions).Notes()
		for _, note := range notes {
			fmt.Fprintf(os.Stderr, "vertrag: %s\n", note)
		}
		if len(notes) > 0 {
			fmt.Printf("\nNo sequence could be built: every link this description declares " +
				"names something it does not define. The lines above say which.\n")
			return nil, nil
		}
		fmt.Printf("\nNo operation links to another, so there is no sequence to run. " +
			"Declare `links` on a response to describe a lifecycle.\n")
		return nil, nil
	}

	var results []runner.Result
	findings := 0

	for _, chain := range chains {
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		outcome := runChain(ctx, engine, transactions, chain)
		name := "chain: " + chain.Names(transactions)

		switch {
		case len(outcome.Findings) > 0:
			for _, finding := range outcome.Findings {
				findings++
				printStatefulFinding(engine, chain, transactions, finding, color)
				results = append(results, runner.Result{
					Name: name + " · " + finding.Name, Status: runner.StatusFail,
					Request: sentAs(engine, compile.Transaction{}, compile.Request{}),
					Errors:  []string{finding.Message, finding.Basis},
				})
			}
		case outcome.Stopped != "":
			// A chain that could not be run through is not a finding: the
			// step that stopped it has already been judged by the phase that
			// sends documented transactions.
			results = append(results, runner.Result{
				Name: name, Status: runner.StatusSkip, Errors: []string{outcome.Stopped},
			})
		default:
			results = append(results, runner.Result{Name: name, Status: runner.StatusPass})
		}
	}

	fmt.Printf("\n%d chain(s) run, %d finding(s)\n", len(chains), findings)
	if findings > 0 {
		return results, errFailed
	}
	return results, nil
}

// runChain sends one chain, threading each response's values into the next
// request, and judges the sequence.
func runChain(ctx context.Context, engine *runner.Runner, transactions []compile.Transaction, chain link.Chain) statefulOutcome {
	outcome := statefulOutcome{Chain: chain}

	// The sequencer already knows how to resolve a link's expressions against
	// a completed exchange and put the values into the next request. Reusing
	// it is what keeps the stateful phase and `--sequence` agreeing about what
	// a link means.
	sequencer := link.NewSequencer(transactions)

	// Its notes are printed here too, which `--sequence` already did and this
	// phase did not. Some chains forming does not mean all of them did, and a
	// link that dangled is exactly as worth naming when its neighbours worked.
	for _, note := range sequencer.Notes() {
		fmt.Fprintf(os.Stderr, "vertrag: %s\n", note)
	}

	// deleted records the addresses this chain has removed, so a later step
	// reading one can say the server should no longer have it.
	var deleted []string

	// reads remembers the read steps and what they addressed, so that once a
	// delete succeeds they can be sent AGAIN. That replay is the whole of the
	// use-after-free check and the reason this phase can see what `--sequence`
	// cannot: a documented transaction runs once, and a resource outliving
	// its delete is only visible to a request nobody documented — the same
	// read, after.
	type readStep struct {
		index int
		url   string
	}
	var reads []readStep
	completed := map[int]runner.Result{}

	for position, index := range chain.Steps {
		source := transactions[index]
		prepared := engine.Prepare(source)

		if position > 0 {
			if reason, ok := sequencer.Prepare(index, prepared, completed); !ok {
				outcome.Stopped = reason
				return outcome
			}
		}

		reply, err := engine.Deliver(ctx, prepared)
		if err != nil {
			outcome.Stopped = fmt.Sprintf("%s could not be sent: %v", source.Name, err)
			return outcome
		}
		outcome.Steps++

		result := runner.Result{
			Name:    source.Name,
			Request: prepared.SentRequest(),
			Actual:  reply,
			Status:  runner.StatusPass,
		}
		status, _ := strconv.Atoi(strings.TrimSpace(reply.StatusCode))
		// The band's lowest code stands in for a documented range, so the
		// "was this step meant to succeed" questions below can be asked of an
		// operation whose author wrote `2XX` rather than `201`.
		documented := validate.StatusBandBase(source.Response.Status)

		// The sequence's own questions, asked before the ordinary verdict:
		// they are about what the server should now hold, not about whether
		// this response matched its schema.
		switch {
		case position > 0 && wasDeleted(deleted, prepared.FullURL()) && isRead(source) && status >= 200 && status < 300:
			// A documented read that follows a delete within the chain
			// itself. Rare — a description usually deletes last — but the
			// replay below covers the usual shape.
			outcome.Findings = append(outcome.Findings, statefulFinding{
				Step: position, Name: "use after free", Status: reply.StatusCode,
				Request: prepared.SentRequest(),
				Message: fmt.Sprintf("%s answered %d for a resource the sequence had already deleted; "+
					"a read after a successful DELETE must not find it", source.Name, status),
				Basis: deletedStaysGone,
			})
		case position > 0 && status == http.StatusNotFound && documented >= 200 && documented < 300 && createdEarlier(transactions, chain, position):
			outcome.Findings = append(outcome.Findings, statefulFinding{
				Step: position, Name: "resource availability", Status: reply.StatusCode,
				Request: prepared.SentRequest(),
				Message: fmt.Sprintf("%s answered 404 for a resource the sequence had just created; "+
					"the create reported success, so following its own link must find what it made", source.Name),
				Basis: createdIsThere,
			})
		}

		if status != documented {
			// Judged by the phase that sends documented transactions; here it
			// only means the chain cannot continue, since later steps take
			// their values from this response.
			result.Status = runner.StatusFail
			completed[index] = result
			sequencer.Record(index, prepared, result)
			if len(outcome.Findings) == 0 {
				outcome.Stopped = fmt.Sprintf("%s answered %d where the description promises %d, so the chain stopped",
					source.Name, status, documented)
			}
			return outcome
		}

		if isRead(source) {
			reads = append(reads, readStep{index: index, url: prepared.FullURL()})
		}
		if strings.EqualFold(source.Request.Method, http.MethodDelete) && status >= 200 && status < 300 {
			deleted = append(deleted, prepared.FullURL())
		}

		completed[index] = result
		sequencer.Record(index, prepared, result)
	}

	// The chain ran through. Now the question only a sequence can ask: send
	// each read again, at an address the sequence deleted, and require the
	// server not to find it.
	for _, read := range reads {
		if !wasDeleted(deleted, read.url) {
			continue
		}
		if ctx.Err() != nil {
			return outcome
		}
		source := transactions[read.index]
		replay := engine.Prepare(source)
		if reason, ok := sequencer.Prepare(read.index, replay, completed); !ok {
			// The values that addressed the resource are no longer
			// resolvable, so the replay would ask about something else.
			outcome.Stopped = reason
			continue
		}
		if replay.FullURL() != read.url {
			continue
		}

		reply, err := engine.Deliver(ctx, replay)
		if err != nil {
			// A transport failure here is not a use-after-free; it is the
			// server refusing to talk, which the chain's own steps would
			// already have reported.
			continue
		}
		status, _ := strconv.Atoi(strings.TrimSpace(reply.StatusCode))
		if status >= 200 && status < 300 {
			outcome.Findings = append(outcome.Findings, statefulFinding{
				Name: "use after free", Status: reply.StatusCode, Request: replay.SentRequest(),
				Message: fmt.Sprintf("%s still answered %d after the sequence deleted the resource; "+
					"a read repeated after a successful DELETE must not find it", source.Name, status),
				Basis: deletedStaysGone,
			})
		}
	}
	return outcome
}

// isRead reports whether an operation reads rather than changes — the only
// kind whose success after a delete is a use-after-free.
func isRead(transaction compile.Transaction) bool {
	switch strings.ToUpper(transaction.Request.Method) {
	case http.MethodGet, http.MethodHead:
		return true
	}
	return false
}

// wasDeleted reports whether this exact address was deleted earlier in the
// chain. Comparing the whole URL rather than an identifier is deliberate: it
// is the address the sequence removed, and no inference about which part of
// it names the resource is needed or safe.
func wasDeleted(deleted []string, url string) bool {
	for _, address := range deleted {
		if address == url {
			return true
		}
	}
	return false
}

// createdEarlier reports whether an earlier step in the chain created
// something — a 2xx from a method that makes resources. Without one, a 404
// here says the identifier named nothing, which is a fact about the fixture
// rather than about the server.
func createdEarlier(transactions []compile.Transaction, chain link.Chain, before int) bool {
	for position := 0; position < before; position++ {
		switch strings.ToUpper(transactions[chain.Steps[position]].Request.Method) {
		case http.MethodPost, http.MethodPut:
			return true
		}
	}
	return false
}

func printStatefulFinding(engine *runner.Runner, chain link.Chain, transactions []compile.Transaction, finding statefulFinding, color bool) {
	paint := func(code, text string) string { return reporter.Paint(color, code, text) }

	fmt.Printf("\n%s %s\n", paint(reporter.Red, "finding:"), finding.Name)
	fmt.Printf("  %s\n", finding.Message)
	fmt.Printf("  %s %s\n", paint(reporter.Dim, "chain:  "), chain.Names(transactions))
	fmt.Printf("  %s %s %s\n", paint(reporter.Dim, "request:"), finding.Request.Method, finding.Request.URI)
	fmt.Printf("  %s %s\n", paint(reporter.Dim, "status: "), finding.Status)
	fmt.Printf("  %s %s\n", paint(reporter.Dim, "basis:  "), finding.Basis)
	if repro := reporter.Curl(finding.Request); repro != "" {
		fmt.Printf("  %s %s\n", paint(reporter.Dim, "repro:  "), repro)
	}
}
