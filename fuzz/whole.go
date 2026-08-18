package fuzz

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strconv"

	"github.com/antimatter-studios/vertrag/generate"
	"github.com/antimatter-studios/vertrag/validate"
	"pgregory.net/rapid"
)

// Whole-request probing draws every varying part of a request together —
// the body and each parameter — instead of one part at a time.
//
// Per-part probing is the honest default: when a finding names one parameter
// the reader knows where to look. But some bugs live only in the interaction
// — a handler that reads `limit` correctly and `offset` correctly and
// overflows on their product — and no single-part probe reaches them, since
// every other part stays at its documented example. Whole-request mode is the
// second pass that does, at the price of a finding that names the request
// rather than one field.

// Part is one varying part of a whole request: where it goes and how.
type Part struct {
	Subject Subject
	Schema  generate.Schema
	// Media is the body's media type; empty for a parameter.
	Media string
}

// WholeSender applies a set of drawn values — one per part, keyed by the
// part's label — to the request and sends it.
type WholeSender func(ctx context.Context, values map[string]any) (validate.Message, error)

// WholeFinding is a request the server mishandled, with every part's value.
type WholeFinding struct {
	Mode    generate.Mode
	Values  map[string]any
	Status  string
	Message string
	// Culprit names the part drawn invalid, in Invalid mode; empty when
	// every part was valid.
	Culprit string
}

// ProbeWhole draws all parts together and reports the first request the
// server mishandled, shrunk to the smallest such set of values.
//
// In Valid mode every part is valid and any 4xx is a disagreement. In Invalid
// mode ONE part per case is drawn invalid and the rest valid, so a bypass can
// be attributed to that part — the finding names it — while every other part
// is exercised at the same time rather than held at its example.
func ProbeWhole(ctx context.Context, parts []Part, mode generate.Mode, send WholeSender, opts Options) (WholeFinding, bool) {
	if len(parts) == 0 {
		return WholeFinding{}, false
	}
	if opts.Cases <= 0 {
		opts.Cases = 20
	}

	labels := make([]string, 0, len(parts))
	byLabel := make(map[string]Part, len(parts))
	for _, part := range parts {
		label := PartLabel(part.Subject)
		labels = append(labels, label)
		byLabel[label] = part
	}
	sort.Strings(labels)

	// Every part's schema, marshalled once for the validity check.
	raw := make(map[string]json.RawMessage, len(parts))
	for label, part := range byLabel {
		encoded, err := json.Marshal(map[string]any(part.Schema))
		if err != nil {
			return WholeFinding{}, false
		}
		raw[label] = encoded
	}

	enableOutsideTests()
	probeMu.Lock()
	defer probeMu.Unlock()
	_ = flag.Set("rapid.checks", strconv.Itoa(opts.Cases))
	_ = flag.Set("rapid.seed", strconv.FormatUint(opts.Seed, 10))

	collector := &collector{}
	var found WholeFinding
	usable := 0
	passed := map[string]bool{}

	rapid.Check(collector, func(t *rapid.T) {
		if opts.OutOfTime() {
			return
		}
		culprit := ""
		if mode == generate.Invalid {
			culprit = rapid.SampledFrom(labels).Draw(t, "culprit")
		}

		values := make(map[string]any, len(labels))
		for _, label := range labels {
			part := byLabel[label]
			partMode := generate.Valid
			if label == culprit {
				partMode = generate.Invalid
			}
			value := generate.Value(part.Schema, partMode).Draw(t, label)

			// Pinned here, between the draw and the render, exactly as in the
			// per-part loop. It was not, and that was a hole rather than an
			// omission of no consequence: `fuzz --whole-request` draws a body
			// of its own and sends it, so a run with `dry_run` pinned held it
			// on every per-part probe and released it on every whole-request
			// one — the single arrangement in which a caller believes the
			// interlock is on and it is off. Pins' own documentation claims no
			// path exists from a draw to the wire that skips it, and this is
			// what makes that true.
			value, engaged := opts.Pin.ApplyTo(part.Subject, part.Schema, value)
			for _, name := range engaged {
				if opts.Engaged != nil {
					opts.Engaged[name]++
				}
			}

			// Each part must have the wire form its location allows and the
			// validity it was drawn for — the same discipline per-part
			// probing applies, or a generator limitation on one part would
			// be reported as a server bug about the whole request.
			form := formFor(part)
			rendered, ok := form.render(value)
			if !ok {
				return
			}
			valid, decided := intendedValidity(raw[label], form.interpret(rendered))
			if !decided || valid != (partMode == generate.Valid) {
				return
			}
			values[label] = rendered
		}
		key := wireKey(values)
		if passed[key] {
			return
		}
		usable++

		reply, err := send(ctx, values)
		if errors.Is(err, ErrSkipped) {
			return
		}
		if err != nil {
			found = WholeFinding{Mode: mode, Values: values, Culprit: culprit,
				Message: fmt.Sprintf("the request could not be completed: %v", err)}
			t.Fatalf("request failed: %v", err)
		}

		// The subject judged is the culprit part in Invalid mode — a bypass
		// is attributed to it — and the whole request otherwise, which the
		// message names as such rather than as any one part.
		subject := wholeSubject(parts)
		if culprit != "" {
			subject = byLabel[culprit].Subject
		}
		if message, bad := judgeWhole(mode, subject, reply); bad {
			found = WholeFinding{Mode: mode, Values: values, Status: reply.StatusCode,
				Message: message, Culprit: culprit}
			t.Fatalf("%s", message)
		}
		passed[key] = true
	})

	if !collector.failed {
		if usable == 0 {
			return WholeFinding{Mode: mode, Message: "no whole request could be drawn with every part in the validity asked for, so none was sent"}, false
		}
		return WholeFinding{}, false
	}
	return found, true
}

// judgeWhole is judge, worded for a whole request. A path-parameter 404 is
// forgiven only when the path parameter is the culprit or every part is
// valid — the same exemption per-part probing gives, for the same reason.
func judgeWhole(mode generate.Mode, subject Subject, reply validate.Message) (string, bool) {
	message, bad := judge(mode, subject, reply)
	if !bad {
		return "", false
	}
	if mode == generate.Valid {
		return "with every part valid, " + message, true
	}
	return message, true
}

// wholeSubject names the request as a whole, and remembers when every part of
// it is a GraphQL argument.
//
// Without that the valid-mode pass over a GraphQL operation would be judged on
// its status, which is 200 whatever the server thought of the request — so a
// server refusing every one of them would pass, silently, which is the exact
// failure the GraphQL body check exists to prevent. The possession exemption
// carries over the same way: a whole request one of whose parts is a made-up
// identifier is as exempt as that part alone.
func wholeSubject(parts []Part) Subject {
	subject := Subject{In: InWhole}
	for _, part := range parts {
		if part.Subject.In != InArgument {
			return Subject{In: InWhole}
		}
		subject.byBody = true
		subject.Possessed = subject.Possessed || part.Subject.Possessed
	}
	return subject
}

// formFor picks the wire form for a part.
func formFor(part Part) wire {
	switch part.Subject.In {
	case InBody:
		form, ok := BodyForm(part.Media, part.Schema)
		if !ok {
			return bodyForm()
		}
		return form
	case InArgument:
		return argumentForm()
	}
	return parameterForm(part.Subject, part.Schema)
}

// PartLabel is the stable key a part's value travels under: "body", or
// "<in>.<name>" for a parameter or an argument, so a finding can be read
// without the parts.
func PartLabel(subject Subject) string {
	if subject.In == InBody || subject.In == "" {
		return "body"
	}
	if subject.In == InArgument && subject.Where != "" {
		// Two fields of one query can declare the same argument name, so the
		// field it belongs to is part of what identifies it.
		return subject.In + "." + subject.Where + "." + subject.Name
	}
	return subject.In + "." + subject.Name
}
