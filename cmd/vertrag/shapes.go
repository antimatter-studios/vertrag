package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/reporter"
	"github.com/antimatter-studios/vertrag/shape"
	"github.com/antimatter-studios/vertrag/validate"
)

// observing hands every response of one phase to the recorder.
//
// Which PHASE saw a shape is the whole of the diagnosis, which is why it is
// baked into the observer rather than left to be worked out later. "The
// examples phase got a string and the probing phases got an array" points
// straight at the cause — a framework's automatic validation error meeting a
// handler's own — where "this status has two shapes" leaves someone reading
// their handlers to find out which is which. The run knows the phase at the
// moment it installs the observer, so it costs nothing to carry.
//
// The operation is `operationKey` with the method in front of it, which is what
// successVariants already means by "the same operation". It has to be the
// template rather than the URI: a probing phase varies path parameters, so
// keying on the address a response came back from would file every generated
// request under an operation of its own and see one shape each.
// The phase arrives as a function rather than a value because one observer now
// serves the whole run: observers are a list that is appended to, so this
// cannot be re-registered per phase without recording every later response
// once per phase already added. The caller sets a variable between phases,
// which is safe for the reason written there — phases are sequential and every
// worker of one has finished before the next begins.
func observing(into *shape.Recorder, phase func() string) func(context.Context, compile.Transaction, validate.Message) {
	return func(_ context.Context, source compile.Transaction, reply validate.Message) {
		operation := source.Request.Method + " " + operationKey(source)
		into.Record(phase(), operation, reply.StatusCode, reply.Headers, reply.Body)
	}
}

// reportDivergences writes the run-level summary of statuses that answered with
// two shapes.
//
// It is a summary rather than a failure, and it does not touch the exit code.
// What it reports is a property of the DESCRIPTION — a status documented with
// one body that the server answers with two — so there is no response here that
// is wrong, and failing a run over it would block a merge on a document nobody
// changed. A pipeline that treats this as red would have people delete the
// check rather than fix the description.
func reportDivergences(out io.Writer, divergences []shape.Divergence, colour bool) {
	if len(divergences) == 0 {
		return
	}

	fmt.Fprint(out, "\ntwo shapes for one status:\n")
	for _, divergence := range divergences {
		fmt.Fprintf(out, "  %s · %s · %s\n", divergence.Operation, divergence.Status, divergence.Media)
		for _, conflict := range divergence.Conflicts {
			fmt.Fprintf(out, "    at %s: %s\n", describePath(conflict.Path), describeKinds(conflict.Kinds))
		}
	}

	// Wrapped by hand rather than left to the terminal, because a paragraph
	// reflowed to eighty columns loses the indent that ties it to the list
	// above it.
	for _, line := range []string{
		"One operation, one status, one media type, and bodies a client cannot read with",
		"one parser. Nothing above fails the run: what it describes is the description,",
		"which promises a single body per status and has nowhere to say which one.",
	} {
		fmt.Fprintf(out, "  %s\n", reporter.Paint(colour, reporter.Dim, line))
	}
}

// describePath names where in a body the disagreement is. The empty path is the
// body itself, which happens when one response is an object and another an
// array — and "at : object, array" would read as a missing field name.
func describePath(path string) string {
	if path == "" {
		return "the whole body"
	}
	return strings.TrimPrefix(path, "/")
}

// describeKinds writes each JSON type with the phases that saw it, in the order
// the run met them.
func describeKinds(kinds []shape.Sighting) string {
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		parts = append(parts, fmt.Sprintf("%s (%s)", kind.Kind, strings.Join(kind.Phases, ", ")))
	}
	return strings.Join(parts, ", ")
}
