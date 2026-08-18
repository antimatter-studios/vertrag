package fuzz

import (
	"fmt"
	"sort"
	"strings"

	"github.com/antimatter-studios/vertrag/generate"
)

// Pins hold a generated body field at a fixed value.
//
// This exists because generation is only safe against an API where every
// endpoint is safe, and that is not the API most people have. A trading system
// documents `POST /orders` with a `dry_run` flag; the schema permits false, so
// generation will draw false, and the probe that was meant to test input
// handling places a real order. Nothing in the description marks that field as
// the one that matters — `dry_run: false` is as valid as `dry_run: true`, which
// is exactly why the caller has to be able to say so.
//
// A pin is therefore not a generation hint, it is a safety interlock, and it is
// built like one:
//
//   - It is applied AFTER the value is drawn and BEFORE the request is
//     rendered, so no path exists from a draw to the wire that skips it.
//   - It is checked against the description at startup, and a pin naming a
//     field that appears in no body schema anywhere refuses to run. A safety
//     interlock wired to nothing is worse than none, because it is believed.
//   - It reports how many operations it engaged on, because "0 of 47" and
//     "3 of 47" look identical from the outside and only one of them is safe.
//
// What a pin cannot do is make an unsafe API safe. It holds one field of one
// generated body; it does not know what the server does with it. Pointing a
// probing phase at production is the caller's decision and this does not change
// that — it only makes the decision expressible.
type Pins map[string]any

// Names lists the pinned fields in a stable order, for reporting.
func (p Pins) Names() []string {
	names := make([]string, 0, len(p))
	for name := range p {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Describe renders the pins the way they would be written, for a run to state
// what it is holding fixed before it sends anything.
func (p Pins) Describe() string {
	parts := make([]string, 0, len(p))
	for _, name := range p.Names() {
		parts = append(parts, fmt.Sprintf("%s=%v", name, p[name]))
	}
	return strings.Join(parts, ", ")
}

// Covers reports whether a schema declares every field this pin names, which
// is what decides whether the pin engages on a given operation.
//
// A pin engages per-property rather than all-or-nothing: an object declaring
// `dry_run` gets it held, and one that does not is left alone. Requiring every
// operation to carry every pinned field would make a single pin unusable on any
// real description, where `dry_run` belongs to the ordering endpoints and
// nothing else.
func (p Pins) Covers(schema generate.Schema, name string) bool {
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return false
	}
	_, declared := properties[name]
	return declared
}

// ApplyTo holds the pinned values for one subject, and reports which pins
// engaged.
//
// A body and a GraphQL argument are pinned differently because the pinned thing
// sits in a different place. A pin names a FIELD of a generated body, so it is
// held inside the object. A GraphQL argument IS the value — `dryRun: Boolean`
// is an argument of the field, not a property of some object — so the pin
// replaces the whole of what was drawn.
//
// Both are then applied, in that order, and the second is not redundant: an
// input-object argument can itself carry a `dryRun` field, and a caller who
// wrote one pin means it about the argument and about the field alike.
//
// Replacing the whole value has a consequence worth stating, because it looks
// like a bug from the outside: an invalid-mode probe of a pinned argument never
// sends anything. The pinned value is by definition the one the caller wants,
// so it satisfies the schema, so the case fails the validity check and is
// abandoned. That is the right outcome — the argument the caller pinned is the
// one they do not want varied — and it is why the engagement count is reported.
func (p Pins) ApplyTo(subject Subject, schema generate.Schema, value any) (any, []string) {
	if len(p) == 0 {
		return value, nil
	}

	var engaged []string
	if subject.In == InArgument {
		if pinned, held := p[subject.Name]; held {
			value = pinned
			engaged = append(engaged, subject.Name)
		}
	}

	value, inside := p.Apply(schema, value)
	for _, name := range inside {
		if !contains(engaged, name) {
			engaged = append(engaged, name)
		}
	}
	return value, engaged
}

// Apply holds the pinned fields at their values, and reports which ones it
// actually set.
//
// A value that is not an object is returned untouched: a pin names a body
// field, and a schema producing a scalar or a list has no field to name. That
// is not an error here — the startup check is what catches a pin that matches
// nothing anywhere, and it has already run by this point.
func (p Pins) Apply(schema generate.Schema, value any) (any, []string) {
	if len(p) == 0 {
		return value, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return value, nil
	}

	var engaged []string
	for _, name := range p.Names() {
		if !p.Covers(schema, name) {
			continue
		}
		object[name] = p[name]
		engaged = append(engaged, name)
	}
	return object, engaged
}

// CheckPins refuses a pin that names a field no body schema in the run
// declares.
//
// The failure mode this prevents is the whole reason pins are worth building
// carefully: a typo in `dry_run`, or a field renamed in the description since
// the pin was written, leaves a configuration that reads exactly like a safety
// control and holds nothing. The run would go out with generated values in the
// field the caller believed was fixed, and the only evidence would be whatever
// the server then did.
//
// It is checked against every body schema in the run rather than each one
// separately, because a pin legitimately applies to a subset — `dry_run`
// belongs to the ordering endpoints, not to `GET /health`. Matching nowhere is
// the error; matching somewhere is the feature.
//
// arguments are the GraphQL argument names the run will generate for, and they
// are checked on exactly the same terms. A GraphQL schema has no request body
// to declare a field in — the dangerous flag is an ARGUMENT, `createOrder(input:
// ..., dryRun: Boolean)` — so a check that looked only at bodies would refuse
// every pin written for a schema, and the caller who worked around it by
// removing the pin would have removed the interlock.
func CheckPins(pins Pins, schemas []generate.Schema, arguments []string) error {
	if len(pins) == 0 {
		return nil
	}

	var unmatched []string
	for _, name := range pins.Names() {
		matched := contains(arguments, name)
		for _, schema := range schemas {
			if matched {
				break
			}
			matched = pins.Covers(schema, name)
		}
		if !matched {
			unmatched = append(unmatched, name)
		}
	}
	if len(unmatched) == 0 {
		return nil
	}

	subject := "a field or argument"
	if len(unmatched) > 1 {
		subject = "fields or arguments"
	}
	return fmt.Errorf(
		"fuzz.pin names %s nothing in this description declares: %s. "+
			"A pin that matches nothing is not a safety control, it only looks like one — "+
			"check the spelling against the schema, or remove it",
		subject, strings.Join(unmatched, ", "))
}
