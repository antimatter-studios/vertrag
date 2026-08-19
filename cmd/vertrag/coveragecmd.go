package main

import (
	"context"
	"fmt"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/fuzz"
	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
	"io"
	"strings"
	"sync"
)

// coverAll sends every probe of every target of every operation, and reports.
func coverAll(
	ctx context.Context,
	engine *runner.Runner,
	transactions []compile.Transaction,
	wanted map[generate.Mode]bool,
	skipped int,
	color bool,
	refused *refusals,
	options fuzz.Options,
) ([]runner.Result, error) {
	// The same interlock the fuzz phase applies, checked the same way and
	// before the same first request. See probeAll: a pin that held on one
	// probing phase and not the other would not be a pin.
	if len(options.Pin) > 0 {
		bodies, arguments := pinnable(transactions)
		if err := fuzz.CheckPins(options.Pin, bodies, arguments); err != nil {
			return nil, err
		}
		options.Engaged = map[string]int{}
		fmt.Printf("pinned in every generated %s: %s\n", pinScope(bodies, arguments), options.Pin.Describe())
	}
	if len(options.Accept) > 0 {
		options.Suppression = &fuzz.Suppression{}
	}

	// The coverage phase parallelises where the fuzz phase cannot.
	//
	// fuzz is serialised by a mutex because rapid takes its case count and seed
	// from process-global flags, so two probes at once would overwrite each
	// other's seed. Coverage uses no rapid at all — it sends the boundaries a
	// schema implies, which are computed, not drawn — so nothing is shared
	// between two operations being probed at the same time, and against a real
	// API almost all of the wall clock is spent waiting for the server.
	//
	// What concurrency costs here is order, and order is not negotiable: a
	// report whose findings arrive in a different sequence each run cannot be
	// diffed between runs, and interleaved output from four workers is
	// unreadable. So each operation gets its own accumulator — results, counts,
	// buffered output, and its own copy of the mutable options — and they are
	// merged afterwards in the order the description lists them. The run is
	// then identical whatever --workers says, which is the property that makes
	// turning it up safe.
	type covered struct {
		results                                             []runner.Result
		findings, sent, probed, unattributable, loginExempt int
		output                                              strings.Builder
		engaged                                             map[string]int
		suppression                                         *fuzz.Suppression
		err                                                 error
	}

	workers := options.Workers
	if workers < 1 {
		workers = 1
	}
	if workers > len(transactions) {
		workers = len(transactions)
	}

	each := make([]covered, len(transactions))
	// refused is shared and written from every worker, so it is the one thing
	// that needs a lock rather than an accumulator: it deduplicates across
	// operations, which per-worker copies could not do.
	var refusedMu sync.Mutex

	work := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range work {
				transaction := transactions[index]
				got := &each[index]
				got.engaged = map[string]int{}
				if options.Suppression != nil {
					got.suppression = &fuzz.Suppression{}
				}

				// Its own copy, so two workers cannot write one map.
				local := options
				local.Engaged = got.engaged
				local.Suppression = got.suppression

				targets, unreadable := probeTargets(transaction.Request)
				for _, subject := range unreadable {
					fmt.Fprintf(&got.output, "%s: the %s schema could not be read, so it was not probed\n",
						transaction.Name, subject.Describe())
				}

				// The same attribution rule as fuzz: valid probes are only
				// meaningful against an operation that works as documented.
				base := baselineWorks(ctx, engine, transaction, local.Pin)
				refusedMu.Lock()
				if base.refused {
					refused.note(transaction)
				}
				isLogin := refused.isLogin(transaction)
				refusedMu.Unlock()

				for _, target := range targets {
					if ctx.Err() != nil {
						got.err = ctx.Err()
						break
					}
					got.probed++

					send := func(ctx context.Context, value any) (validate.Message, error) {
						request, err := target.apply(transaction.Request, value)
						if err != nil {
							return validate.Message{}, err
						}
						attempt := transaction
						attempt.Request = request
						got.sent++
						return skipAware(engine.Send(ctx, attempt))
					}

					for _, outcome := range fuzz.Cover(ctx, target.subject, target.media, target.schema, send, local) {
						if !wanted[outcome.Probe.Mode] {
							continue
						}
						if outcome.Probe.Mode == generate.Valid && (!base.ok || isLogin) {
							if isLogin {
								got.loginExempt++
							} else {
								got.unattributable++
							}
							continue
						}
						if !outcome.Sent {
							// No wire form for this probe on this subject; not
							// a finding and not a probe that ran.
							continue
						}
						name := transaction.Name + " · " + target.subject.Describe() + " · " + outcome.Probe.Why
						if outcome.Finding == nil {
							got.results = append(got.results, runner.Result{
								Name: name, Status: runner.StatusPass,
								Request: sentAs(engine, transaction.Request),
							})
							continue
						}
						got.findings++
						printCoverageFinding(&got.output, engine, transaction, target, *outcome.Finding, color)
						failed := transaction.Request
						if r, err := target.apply(failed, outcome.Finding.Value); err == nil {
							failed = r
						}
						got.results = append(got.results, runner.Result{
							Name: name, Status: runner.StatusFail,
							Request: sentAs(engine, failed), Errors: []string{outcome.Finding.Message},
						})
					}
				}
			}
		}()
	}
	for index := range transactions {
		work <- index
	}
	close(work)
	wg.Wait()

	findings, sent, probed, unattributable, loginExempt := 0, 0, 0, 0, 0
	var results []runner.Result
	for index := range each {
		got := &each[index]
		fmt.Print(got.output.String())
		results = append(results, got.results...)
		findings += got.findings
		sent += got.sent
		probed += got.probed
		unattributable += got.unattributable
		loginExempt += got.loginExempt
		for name, count := range got.engaged {
			options.Engaged[name] += count
		}
		if got.suppression != nil {
			for code, count := range got.suppression.ByStatus {
				for range count {
					options.Suppression.Record(code)
				}
			}
		}
		if got.err != nil {
			return results, got.err
		}
	}

	fmt.Printf("\n%d operation(s) covered over %d body, parameter and argument target(s), %d probe(s) sent, %d finding(s)",
		len(transactions), probed, sent, findings)
	if unattributable > 0 {
		fmt.Printf(", %d valid probe(s) skipped because the operation fails as documented", unattributable)
	}
	if loginExempt > 0 {
		fmt.Printf(", %d skipped on the login operation", loginExempt)
	}
	if skipped > 0 {
		fmt.Printf(", %d transaction(s) skipped for having no schema to probe", skipped)
	}
	refused.report()
	fmt.Println()

	if findings > 0 {
		return results, errFailed
	}
	return results, nil
}

// printCoverageFinding writes to w rather than stdout so the phase can run
// transactions concurrently and still print each one's findings together, in
// the order the description lists them. Interleaved findings from four workers
// would be unreadable, and worse, would not reproduce.
func printCoverageFinding(w io.Writer, engine *runner.Runner, transaction compile.Transaction, target target, finding fuzz.CoverageFinding, color bool) {
	paint := func(code, text string) string { return reporter.Paint(color, code, text) }

	request := transaction.Request
	if sent, err := target.apply(request, finding.Value); err == nil {
		request = sent
	}

	fmt.Fprintf(w, "\n%s %s\n", paint(reporter.Red, "finding:"), transaction.Name)
	fmt.Fprintf(w, "  %s\n", finding.Message)
	fmt.Fprintf(w, "  %s %s\n", paint(reporter.Dim, "probe:  "), finding.Probe.Why)
	fmt.Fprintf(w, "  %s %s %s\n", paint(reporter.Dim, "request:"), request.Method, request.URI)
	fmt.Fprintf(w, "  %s %v\n", paint(reporter.Dim, finding.Subject.Describe()+":"), finding.Value)
	fmt.Fprintf(w, "  %s %s\n", paint(reporter.Dim, "status: "), finding.Status)
	if repro := reporter.Curl(sentAs(engine, request)); repro != "" {
		fmt.Fprintf(w, "  %s %s\n", paint(reporter.Dim, "repro:  "), repro)
	}
}

// coverageWorkers resolves how many operations to probe at once.
//
// A flag that was given wins over the file, the same rule the rest of the
// settings follow. `workers` in the file is the run-wide setting the examples
// phase already reads, and it means the same thing here rather than needing a
// second key: someone who has decided their API tolerates four requests at once
// has decided it for every phase.
func coverageWorkers(flag, configured int) int {
	if flag > 1 {
		return flag
	}
	if configured > 1 {
		return configured
	}
	return 1
}
