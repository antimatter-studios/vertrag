package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/fuzz"
	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// runCoverage is `vertrag coverage`: send every boundary probe each schema
// implies — the maximum, one past it, the required property missing — and
// judge the answers.
//
// It sits between `run` and `fuzz`. Like `run` it is deterministic: the same
// description produces the same probes in the same order, so it can gate a
// pipeline. Like `fuzz` it sends what the description never showed: the
// values at and past every bound, which are exactly the ones an example
// avoids and a handler gets wrong.
func runCoverage(args []string) error {
	fs := flag.NewFlagSet("coverage", flag.ContinueOnError)
	var shared probeFlags
	addProbeFlags(fs, &shared, "cover")
	mode := fs.String("mode", "both", "which probes to send: valid (on the bound), invalid (past it), or both")

	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	modes, err := parseModes(*mode)
	if err != nil {
		return err
	}
	wanted := map[generate.Mode]bool{}
	for _, m := range modes {
		wanted[m] = true
	}

	set, err := prepareProbes(&shared, fs, positional)
	if err != nil || set == nil {
		return err
	}
	defer set.stop()

	results, runErr := coverAll(set.ctx, set.engine, set.probeable, wanted, set.skipped,
		set.settings.Color, newRefusals(set.settings))
	if err := emitThrough(&shared, set.settings, results); err != nil {
		return err
	}
	return runErr
}

// coverAll sends every probe of every target of every operation, and reports.
func coverAll(
	ctx context.Context,
	engine *runner.Runner,
	transactions []compile.Transaction,
	wanted map[generate.Mode]bool,
	skipped int,
	color bool,
	refused *refusals,
) ([]runner.Result, error) {
	findings, sent, probed, unattributable, loginExempt := 0, 0, 0, 0, 0
	var results []runner.Result

	for _, transaction := range transactions {
		targets, unreadable := probeTargets(transaction.Request)
		for _, subject := range unreadable {
			fmt.Printf("%s: the %s schema could not be read, so it was not probed\n",
				transaction.Name, subject.Describe())
		}

		// The same attribution rule as fuzz: valid probes are only meaningful
		// against an operation that works as documented.
		base := baselineWorks(ctx, engine, transaction)
		if base.refused {
			refused.note(transaction)
		}
		isLogin := refused.isLogin(transaction)

		for _, target := range targets {
			if ctx.Err() != nil {
				return results, ctx.Err()
			}
			probed++

			send := func(ctx context.Context, value any) (validate.Message, error) {
				request, err := target.apply(transaction.Request, value)
				if err != nil {
					return validate.Message{}, err
				}
				attempt := transaction
				attempt.Request = request
				sent++
				return skipAware(engine.Send(ctx, attempt))
			}

			for _, outcome := range fuzz.Cover(ctx, target.subject, target.media, target.schema, send) {
				if !wanted[outcome.Probe.Mode] {
					continue
				}
				if outcome.Probe.Mode == generate.Valid && (!base.ok || isLogin) {
					if isLogin {
						loginExempt++
					} else {
						unattributable++
					}
					continue
				}
				if !outcome.Sent {
					// No wire form for this probe on this subject; not a
					// finding and not a probe that ran.
					continue
				}
				name := transaction.Name + " · " + target.subject.Describe() + " · " + outcome.Probe.Why
				if outcome.Finding == nil {
					results = append(results, runner.Result{
						Name: name, Status: runner.StatusPass,
						Request: sentAs(engine, transaction.Request),
					})
					continue
				}
				findings++
				printCoverageFinding(engine, transaction, target, *outcome.Finding, color)
				failed := transaction.Request
				if r, err := target.apply(failed, outcome.Finding.Value); err == nil {
					failed = r
				}
				results = append(results, runner.Result{
					Name: name, Status: runner.StatusFail,
					Request: sentAs(engine, failed), Errors: []string{outcome.Finding.Message},
				})
			}
		}
	}

	fmt.Printf("\n%d operation(s) covered over %d body and parameter target(s), %d probe(s) sent, %d finding(s)",
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

func printCoverageFinding(engine *runner.Runner, transaction compile.Transaction, target target, finding fuzz.CoverageFinding, color bool) {
	paint := func(code, text string) string { return reporter.Paint(color, code, text) }

	request := transaction.Request
	if sent, err := target.apply(request, finding.Value); err == nil {
		request = sent
	}

	fmt.Printf("\n%s %s\n", paint(reporter.Red, "finding:"), transaction.Name)
	fmt.Printf("  %s\n", finding.Message)
	fmt.Printf("  %s %s\n", paint(reporter.Dim, "probe:  "), finding.Probe.Why)
	fmt.Printf("  %s %s %s\n", paint(reporter.Dim, "request:"), request.Method, request.URI)
	fmt.Printf("  %s %v\n", paint(reporter.Dim, finding.Subject.Describe()+":"), finding.Value)
	fmt.Printf("  %s %s\n", paint(reporter.Dim, "status: "), finding.Status)
	if repro := reporter.Curl(sentAs(engine, request)); repro != "" {
		fmt.Printf("  %s %s\n", paint(reporter.Dim, "repro:  "), repro)
	}
}
