package compile

import (
	"sort"

	"github.com/antimatter-studios/vertrag/refract"
)

// detectTransactionExampleNumbers recovers API Blueprint's transaction example
// numbering from source-map positions.
//
// API Blueprint lets one transition hold several numbered examples, each a run
// of requests followed by responses. API Elements dropped that concept, but
// Dredd still needs the numbers because they appear in transaction names, which
// hook files address transactions by. Renumbering would silently break every
// hook targeting a multi-example blueprint.
//
// The numbering is reconstructed by reading the requests and responses back in
// document order: a request that follows a response starts a new example.
//
// The returned slice is parallel to the transition's httpTransaction children.
func detectTransactionExampleNumbers(transition *refract.Element) []int {
	type indexEntry struct {
		position    int
		transaction *refract.Element
		isRequest   bool
	}

	var index []indexEntry
	for _, transaction := range childrenNamed(transition, "httpTransaction") {
		for _, child := range []*refract.Element{
			transaction.Child("httpRequest"),
			transaction.Child("httpResponse"),
		} {
			if child == nil {
				continue
			}
			position, ok := sourceMapPosition(child)
			if !ok {
				continue
			}
			index = append(index, indexEntry{
				position:    position,
				transaction: transaction,
				isRequest:   child.Name == "httpRequest",
			})
		}
	}

	// A stable sort keeps entries that share a position in the order they were
	// collected, which is the order JavaScript's sort leaves them in.
	sort.SliceStable(index, func(i, j int) bool {
		return index[i].position < index[j].position
	})

	// Insertion order is the order transactions are first seen; the number
	// recorded for each is the last one assigned to it.
	var order []*refract.Element
	numbers := map[*refract.Element]int{}

	currentNo := 1
	previousWasResponse := false
	for _, entry := range index {
		if entry.isRequest {
			if previousWasResponse {
				currentNo++
			}
			previousWasResponse = false
		} else {
			previousWasResponse = true
		}
		if _, seen := numbers[entry.transaction]; !seen {
			order = append(order, entry.transaction)
		}
		numbers[entry.transaction] = currentNo
	}

	out := make([]int, 0, len(order))
	for _, transaction := range order {
		out = append(out, numbers[transaction])
	}
	return out
}

// sourceMapPosition reduces an element's source map to a single number.
//
// The reference takes the maximum over every number in the first source map,
// which mixes offsets with lengths. That is arguably wrong, but the value is
// only ever compared against other values derived the same way, so the ordering
// it produces is what matters — and changing it would renumber examples.
func sourceMapPosition(element *refract.Element) (int, bool) {
	sourceMap := element.Attr("sourceMap")
	if sourceMap == nil {
		return 0, false
	}
	children := sourceMap.ContentChildren()
	if len(children) == 0 {
		return 0, false
	}

	value, ok := children[0].ToValue().([]any)
	if !ok {
		return 0, false
	}

	max := 0
	found := false
	for _, group := range value {
		numbers, ok := group.([]any)
		if !ok {
			continue
		}
		for _, n := range numbers {
			f, ok := n.(float64)
			if !ok {
				continue
			}
			if !found || int(f) > max {
				max = int(f)
				found = true
			}
		}
	}
	return max, found
}
