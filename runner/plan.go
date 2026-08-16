package runner

// Plan decides the order transactions run in, and may rewrite one just before
// it is sent using what earlier ones returned.
//
// It exists so the runner can be sequenced without knowing what a link is. The
// runner's job is to send requests and judge replies; deciding that one request
// must follow another, and that a value from the first belongs in the second,
// is a property of the description, and the description model has no business
// in here.
//
// An implementation lives in the link package. Nothing in the runner imports
// it, which is what keeps this dependency pointing one way.
type Plan interface {
	// Order returns the indexes of the given transactions in the order they
	// should run. Every index appears exactly once — a plan reorders a run, it
	// does not add to or remove from it.
	Order(count int) []int

	// Prepare is called immediately before a transaction is sent, with the
	// results of everything already run. It may rewrite the transaction, and
	// may report that the transaction cannot run at all.
	//
	// It runs BEFORE hooks, deliberately. A hook is a project's own code and
	// has to be able to override anything derived from the description — a
	// suite that stashes an identifier by hand should keep working exactly as
	// it did, and would not if this ran second and overwrote it.
	Prepare(index int, transaction *Transaction, completed map[int]Result) (skip string, ok bool)

	// Record is called after a transaction completes, so the plan can keep
	// whatever a later step will need.
	Record(index int, transaction *Transaction, result Result)
}

// sequence returns the order to run in, and whether a plan is driving it.
func (r *Runner) sequence(count int) []int {
	if r.Plan == nil {
		order := make([]int, count)
		for i := range order {
			order[i] = i
		}
		return order
	}

	order := r.Plan.Order(count)

	// A plan that loses or duplicates a transaction would silently change what
	// a run covers, which is the one thing sequencing must not do. Falling back
	// to document order is safer than running a plan that cannot be trusted,
	// and the guard costs one pass.
	if !isPermutation(order, count) {
		order = make([]int, count)
		for i := range order {
			order[i] = i
		}
	}
	return order
}

func isPermutation(order []int, count int) bool {
	if len(order) != count {
		return false
	}
	seen := make([]bool, count)
	for _, index := range order {
		if index < 0 || index >= count || seen[index] {
			return false
		}
		seen[index] = true
	}
	return true
}
