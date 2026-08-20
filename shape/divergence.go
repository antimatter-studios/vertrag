package shape

import (
	"sort"
	"strings"
	"sync"
)

// Compatible reports whether two bodies could be read by one client parser.
//
// The rule is narrow on purpose, because a false report is expensive here: to
// dismiss one the reader has to fetch two responses and reason about both, and
// a check that costs that and is usually wrong gets ignored — which is worse
// than not making the check. So a difference is only found where a path present
// in BOTH bodies holds a different JSON type in each: a string where an array
// was.
//
// What is deliberately NOT a difference:
//
//   - A field present in one body and absent from the other. Optional fields
//     are what optional means; every API has them, and a client already writes
//     that branch.
//   - A field that is null in one body. null is read as absence, for the same
//     reason: `if x is None` and `if "x" not in body` are one branch in every
//     client anyone writes, and calling them two shapes would fire on the
//     majority of real APIs — which is the failure this rule exists to avoid.
//     The cost of that judgement is that a genuine null-versus-array confusion
//     goes unreported; that is the direction to err in.
//   - An array holding more elements, fewer, or none. Only the sort of element
//     is compared, and only where both bodies have one.
//   - `1` against `1.5`, or `true` against `false`. A client parser branches on
//     the type, not on the value.
//
// It is exported because it is the definition, and a definition that can be
// asserted on directly is one a reader can check against the paragraph above.
func Compatible(a, b string) bool {
	first, firstIsJSON := outlineOf(a)
	second, secondIsJSON := outlineOf(b)
	if !firstIsJSON || !secondIsJSON {
		return true
	}

	seen := tally{}
	seen.add("", first)
	seen.add("", second)
	return len(seen.conflicts()) == 0
}

// Sighting is one JSON type seen at a path, and the phases that saw it, in the
// order they first did.
type Sighting struct {
	Kind   string
	Phases []string
}

// tally is what was seen at each path: the kinds, in first-seen order.
//
// A slice rather than a map because there are five kinds in the whole
// vocabulary, so a linear scan is free, and because the ORDER is half the
// report — "the examples phase got a string, the probing phases got an array"
// localises the cause, and sorting the kinds alphabetically would throw that
// away.
type tally map[string][]Sighting

func (t tally) add(phase string, out outline) {
	for path, kind := range out {
		// Bounded for the reason maxNodes is: a probing phase provokes
		// thousands of responses, and a server that answers with generated keys
		// would otherwise have a run accumulate a path per key per response.
		// Paths already recorded still take their kinds, so what a full tally
		// stops learning is new paths rather than new disagreements.
		if _, known := t[path]; !known && len(t) >= maxNodes {
			continue
		}
		t[path] = addSighting(t[path], kind, phase)
	}
}

func addSighting(seen []Sighting, kind, phase string) []Sighting {
	for i := range seen {
		if seen[i].Kind != kind {
			continue
		}
		for _, already := range seen[i].Phases {
			if already == phase {
				return seen
			}
		}
		seen[i].Phases = append(seen[i].Phases, phase)
		return seen
	}
	return append(seen, Sighting{Kind: kind, Phases: []string{phase}})
}

// conflicts are the paths that held more than one JSON type across the bodies
// tallied, in path order.
//
// The sightings are copied out rather than handed over. A run may still be
// sending — nothing stops a caller asking early — and the phase lists are
// appended to in place, so returning the tally's own slices would put a reader
// and a writer on the same memory with the lock already released.
func (t tally) conflicts() []Conflict {
	var out []Conflict
	for path, kinds := range t {
		if len(kinds) < 2 {
			continue
		}
		copied := make([]Sighting, 0, len(kinds))
		for _, kind := range kinds {
			copied = append(copied, Sighting{Kind: kind.Kind, Phases: append([]string(nil), kind.Phases...)})
		}
		out = append(out, Conflict{Path: path, Kinds: copied})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Conflict is one path that held two JSON types, and which phase saw each.
type Conflict struct {
	// Path is where in the body the disagreement is, as slash-separated key
	// names with `[]` standing for "an element of this array". Empty is the
	// whole body.
	Path  string
	Kinds []Sighting
}

// Divergence is one (operation, status, media type) that answered with bodies
// no single client parser could read.
type Divergence struct {
	Operation string
	Status    string
	Media     string
	Conflicts []Conflict
}

// Recorder collects the outline of every response a run received.
//
// The key is (operation, status, MEDIA TYPE), and the media type is not
// decoration. Content negotiation is a legitimate two-shapes case — one status,
// two content types, two bodies, exactly as designed — so a check keyed on the
// status alone would report it, and a check that reports the intended behaviour
// of ordinary APIs teaches people to distrust it. Requiring the media types to
// match is what makes a report indefensible rather than merely surprising: the
// FastAPI case is `application/json` both times.
type Recorder struct {
	mu   sync.Mutex
	seen map[key]tally
}

type key struct{ operation, status, media string }

// Record takes one response, under the phase that provoked it.
//
// The operation is the caller's notion of "which operation is this" and must be
// stable across phases: a probing phase varies path parameters, so the URI a
// response came back from is not it.
func (r *Recorder) Record(phase, operation, status string, headers map[string]string, body string) {
	media := baseMediaType(contentType(headers))
	if !isJSON(media) {
		return
	}
	out, isJSONBody := outlineOf(body)
	if !isJSONBody {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[key]tally{}
	}
	k := key{operation: operation, status: strings.TrimSpace(status), media: media}
	if r.seen[k] == nil {
		r.seen[k] = tally{}
	}
	r.seen[k].add(phase, out)
}

// Divergences are every (operation, status, media type) that answered with
// incompatible bodies, ordered so two runs of one suite report them the same
// way round.
func (r *Recorder) Divergences() []Divergence {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []Divergence
	for k, seen := range r.seen {
		conflicts := seen.conflicts()
		if len(conflicts) == 0 {
			continue
		}
		out = append(out, Divergence{
			Operation: k.operation,
			Status:    k.status,
			Media:     k.media,
			Conflicts: conflicts,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Operation != out[j].Operation {
			return out[i].Operation < out[j].Operation
		}
		if out[i].Status != out[j].Status {
			return out[i].Status < out[j].Status
		}
		return out[i].Media < out[j].Media
	})
	return out
}
