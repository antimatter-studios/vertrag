package link

import (
	"fmt"
	"sort"
	"strings"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/runner"
)

// Sequencer drives a run from a plan, implementing runner.Plan.
//
// It holds the transactions it was built from, because a link's parameters name
// the target's parameters and only the compiled transaction knows what those
// are.
type Sequencer struct {
	plan         Plan
	transactions []compile.Transaction

	// sources maps a step to the step it takes its values from, so Prepare can
	// find the exchange to evaluate against without walking the plan again.
	sources map[int]int
	links   map[int]compile.Link

	// exchanges records what each completed step actually sent and received.
	exchanges map[int]Exchange

	// substituted holds the parameter values a link put into a step, by
	// location and name.
	//
	// Without it, an exchange was recorded from the COMPILED transaction, whose
	// parameters still carry the description's example — so a third step
	// reading `$request.path.id` from the second got the example rather than
	// the identifier the first step minted, and went looking for a resource
	// nobody created.
	substituted map[int]map[string]string
}

// NewSequencer builds a plan-driven runner from compiled transactions.
func NewSequencer(transactions []compile.Transaction) *Sequencer {
	plan := Build(transactions)

	sources := map[int]int{}
	links := map[int]compile.Link{}
	for _, step := range plan.Steps {
		if step.After == -1 {
			continue
		}
		sources[step.Index] = step.After
		for _, l := range transactions[step.After].Links {
			if l.Name == step.Via {
				links[step.Index] = l
				break
			}
		}
	}

	return &Sequencer{
		plan:         plan,
		transactions: transactions,
		sources:      sources,
		links:        links,
		exchanges:    map[int]Exchange{},
		substituted:  map[int]map[string]string{},
	}
}

// Notes are the diagnostics the plan raised about the description.
func (s *Sequencer) Notes() []string { return s.plan.Notes }

// Order returns the plan's order.
func (s *Sequencer) Order(count int) []int {
	order := make([]int, 0, count)
	for _, step := range s.plan.Steps {
		order = append(order, step.Index)
	}
	return order
}

// Prepare fills a transaction's parameters from the exchange it depends on.
func (s *Sequencer) Prepare(index int, transaction *runner.Transaction, completed map[int]runner.Result) (string, bool) {
	source, dependent := s.sources[index]
	if !dependent {
		return "", true
	}

	previous, ran := completed[source]
	if !ran {
		// The plan guarantees a dependency runs first, so this cannot happen
		// unless the order was overridden. Skipping is the safe answer either
		// way: the values are not there.
		return fmt.Sprintf("the step it follows (%s) did not run", s.transactions[source].Name), false
	}

	if previous.Status != runner.StatusPass {
		return fmt.Sprintf("%s did not pass, so there is no response to take its values from",
			s.transactions[source].Name), false
	}

	l := s.links[index]
	values, unresolved := Values(l, s.exchanges[source])
	if len(unresolved) > 0 {
		sort.Strings(unresolved)
		return fmt.Sprintf("link %s could not resolve %s from the response of %s",
			l.Name, strings.Join(unresolved, ", "), s.transactions[source].Name), false
	}

	applied, used, failure := apply(transaction, s.transactions[index], values)
	if failure != "" {
		return failure, false
	}
	s.substituted[index] = used
	if applied == 0 && len(values) > 0 {
		return fmt.Sprintf("link %s supplied values for no parameter this operation declares", l.Name), false
	}
	return "", true
}

// apply puts resolved values into the transaction about to be sent.
//
// A path or query value cannot be edited into the URI as text — the value has
// to go back through the same template expansion that built the rest of it, or
// it is encoded by different rules than its neighbours. compile.Request knows
// how; the result is copied across.
//
// FullPath is written as well as URI, and that is not belt and braces. The
// runner derives FullPath once and sends to it precisely so that a late hook
// editing Request.URI cannot silently redirect a request; the same protection
// means editing URI alone here would change nothing at all.
func apply(target *runner.Transaction, source compile.Transaction, values map[string]any) (int, map[string]string, string) {
	applied := 0
	used := map[string]string{}
	request := source.Request

	for _, parameter := range source.Request.Parameters {
		for key, value := range values {
			if !Match(key, parameter.In, parameter.Name) {
				continue
			}

			text := stringify(value)
			used[parameter.In+"."+parameter.Name] = text

			if parameter.In == compile.InHeader {
				target.Request.Headers[parameter.Name] = text
				applied++
				continue
			}

			rebuilt, err := request.SetParameter(parameter, text)
			if err != nil {
				return applied, used, fmt.Sprintf("could not put %s into the %s parameter %q: %v",
					key, parameter.In, parameter.Name, err)
			}
			request = rebuilt
			applied++
		}
	}

	if request.URI != source.Request.URI {
		target.Request.URI = request.URI
		target.FullPath = request.URI
		// And the URL the request is actually sent to. The runner builds that
		// once from the endpoint and the original URI, and sends to it — so
		// setting only the two fields above rewrote what the REPORT said while
		// the request still went to the old path. That is the worst possible
		// failure for this feature: a report showing /widgets/42 while
		// /widgets/1 was fetched, and a 404 nobody can explain.
		target.SetFullURL(target.Endpoint() + request.URI)
	}
	return applied, used, ""
}

// Record keeps what a completed step sent and received, so a later step can
// read values out of it.
func (s *Sequencer) Record(index int, transaction *runner.Transaction, result runner.Result) {
	s.exchanges[index] = exchangeOf(
		s.transactions[index], transaction.FullURL(), result, s.substituted[index])
}

// Exchange is what a step sent and received, for a caller that wants to resolve
// expressions against a run this sequencer drove.
//
// Check would otherwise rebuild it from the compiled transaction and get the
// path parameters wrong for exactly the runs where they matter: in a sequenced
// run the value that addressed a resource came from an earlier response, and
// only this holds it. See substituted.
func (s *Sequencer) Exchange(index int) (Exchange, bool) {
	exchange, recorded := s.exchanges[index]
	return exchange, recorded
}

// exchangeOf assembles the exchange a runtime expression reads from.
//
// substituted, which may be nil, holds the parameter values a link put into
// the request, keyed by location and name. Without them the request side of
// the exchange would carry the description's example rather than what was
// sent — see substituted for what that cost.
func exchangeOf(
	source compile.Transaction,
	url string,
	result runner.Result,
	substituted map[string]string,
) Exchange {
	exchange := Exchange{
		URL:            url,
		Method:         result.Request.Method,
		StatusCode:     result.Actual.StatusCode,
		ResponseHeader: result.Actual.Headers,
		ResponseBody:   result.Actual.Body,
		RequestHeader:  map[string]string{},
		RequestPath:    map[string]string{},
		RequestQuery:   map[string]string{},
		RequestBody:    result.Request.Body,
	}

	for name, value := range result.Request.Headers {
		exchange.RequestHeader[name] = value
	}
	for _, parameter := range source.Request.Parameters {
		if !parameter.HasValue {
			continue
		}

		// What was actually sent, which is the description's example only when
		// no link replaced it.
		value := stringify(parameter.Value)
		if replaced, ok := substituted[parameter.In+"."+parameter.Name]; ok {
			value = replaced
		}

		switch parameter.In {
		case compile.InPath:
			exchange.RequestPath[parameter.Name] = value
		case compile.InQuery:
			exchange.RequestQuery[parameter.Name] = value
		}
	}
	return exchange
}

// ExchangeFrom builds the exchange a link's expressions resolve against from a
// transaction the ordinary run already completed.
//
// This is what makes link resolution cost nothing: the examples phase has
// already sent every documented request and kept what came back, so checking
// what a link claims about those responses needs no request of its own. A
// check that doubled a run's traffic would be one people switch off.
func ExchangeFrom(source compile.Transaction, result runner.Result) Exchange {
	return exchangeOf(source, result.Request.URL, result, nil)
}
