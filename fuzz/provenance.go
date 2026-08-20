package fuzz

import (
	"context"

	"github.com/antimatter-studios/vertrag/generate"
)

// The mode a request was drawn in travels on its context, from the probe that
// drew it to whatever is watching the wire.
//
// Nothing in this package reads it back — it is written for a caller that
// observes responses at the point they arrive rather than at the point they
// are judged, and the reason it is carried rather than passed is the same
// lesson `skip` taught the runner. A Sender is one function value per probing
// loop, and there are five of them across two phases plus the whole-request
// pass; a new one is written every time a phase gains a shape of probe. Adding
// the mode to Sender's signature would put the answer in the hands of whoever
// writes the next closure, and the closure that forgot would silently claim
// its requests were sent as documented — the strongest thing a report can say
// about a status. The context is the one thing every send already threads
// through untouched.
//
// It is deliberately narrow: only generation writes it, so a context WITHOUT a
// mode means the request carried content the description itself supplied, and
// that is the distinction a reader most needs.
type modeKey struct{}

// WithMode marks a context as belonging to a request whose value was generated
// in this mode.
func WithMode(ctx context.Context, mode generate.Mode) context.Context {
	return context.WithValue(ctx, modeKey{}, mode)
}

// ModeOf reports the mode a request's value was generated in, and whether it
// was generated at all.
//
// The second return is not decoration. generate.Valid is the zero value of
// generate.Mode, so a caller reading the absent case as a value would read
// every documented request as one drawn valid — the two situations this exists
// to tell apart.
func ModeOf(ctx context.Context) (generate.Mode, bool) {
	mode, generated := ctx.Value(modeKey{}).(generate.Mode)
	return mode, generated
}
