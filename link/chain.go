package link

import (
	"sort"
	"strings"

	"github.com/antimatter-studios/vertrag/compile"
)

// A chain is a path through the link graph: create, then read what was
// created, then delete it. It is what makes the questions no single request
// can ask askable at all.
//
// A run in document order already follows links — `--sequence` — but it runs
// each transaction once and judges each against its own description. That
// catches "the read failed" and cannot catch "the read succeeded, but on a
// resource the create said it had made and hadn't", or "the delete succeeded
// and the resource is still there". Those are properties OF THE SEQUENCE, and
// they are where state bugs live.

// Chain is a sequence of transaction indices to run in order, each linked
// from the one before.
type Chain struct {
	// Steps are indices into the transaction list, in the order to send them.
	Steps []int

	// Via names the link followed into each step; Via[0] is empty because the
	// first step is followed into from nowhere.
	Via []string
}

// Names renders the chain for a report.
func (c Chain) Names(transactions []compile.Transaction) string {
	var parts []string
	for _, index := range c.Steps {
		parts = append(parts, transactions[index].Name)
	}
	return strings.Join(parts, " → ")
}

// Chains finds every maximal path through the links a description declares.
//
// A path starts at a transaction nothing links to — the root of some
// lifecycle — and follows one link at a time until there is nowhere further
// to go. Where an operation offers several links, each is its own chain: the
// point is to exercise a documented route end to end, not to invent a
// traversal the description never claims.
//
// Cycles are broken by refusing to visit a transaction twice within one
// chain. A description may legitimately link A → B → A (read leads to update
// leads to read); following that forever would be a hang, and following it
// once is the useful part.
func Chains(transactions []compile.Transaction) []Chain {
	plan := Build(transactions)

	// The link graph, forwards: which steps each transaction leads to, and by
	// which link. Build already resolved the targets and rejected what it
	// could not follow, so this is its answer read in the other direction.
	next := map[int][]Step{}
	hasParent := map[int]bool{}
	for _, step := range plan.Steps {
		if step.After < 0 {
			continue
		}
		next[step.After] = append(next[step.After], step)
		hasParent[step.Index] = true
	}
	for source := range next {
		sort.Slice(next[source], func(i, j int) bool {
			if next[source][i].Via != next[source][j].Via {
				return next[source][i].Via < next[source][j].Via
			}
			return next[source][i].Index < next[source][j].Index
		})
	}

	var chains []Chain
	// Roots in document order, so the chains a report lists are in the order
	// the description declares them.
	for index := range transactions {
		if hasParent[index] || len(next[index]) == 0 {
			// Nothing links here and nothing leads away: a lone transaction,
			// which the ordinary phases already cover.
			continue
		}
		walk(index, Chain{Steps: []int{index}, Via: []string{""}}, next, &chains)
	}
	return chains
}

// walk extends a chain along every link out of its last step.
func walk(at int, sofar Chain, next map[int][]Step, chains *[]Chain) {
	onward := next[at]
	extended := false
	for _, step := range onward {
		if contains(sofar.Steps, step.Index) {
			// A cycle. The chain so far is still worth running; going round
			// again is not.
			continue
		}
		extended = true
		grown := Chain{
			Steps: append(append([]int{}, sofar.Steps...), step.Index),
			Via:   append(append([]string{}, sofar.Via...), step.Via),
		}
		walk(step.Index, grown, next, chains)
	}
	if !extended && len(sofar.Steps) > 1 {
		*chains = append(*chains, sofar)
	}
}

func contains(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
