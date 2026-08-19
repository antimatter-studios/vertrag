package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/antimatter-studios/vertrag/config"
	"github.com/antimatter-studios/vertrag/fuzz"
	"github.com/antimatter-studios/vertrag/hooks"
	"github.com/antimatter-studios/vertrag/runner"
	"github.com/antimatter-studios/vertrag/validate"
)

// This file was probesetup.go, and held the flag set, config resolution and
// reporting that `vertrag fuzz` and `vertrag coverage` shared with each other
// but not with `run`. Sharing between two of three entry points is what let
// them drift — the seed keys, `server:`, the exit code for a finding — so the
// commands became phases and everything they had needed from here is now on
// the one path through run.go. What is left is the two helpers a run and its
// phases genuinely share, both about hooks.

// startHooks loads the hook files, if there are any, and attaches the worker
// to the engine.
//
// Starting is a hard failure rather than a warning: a suite whose hooks did
// not load would authenticate nothing, pin nothing and skip nothing, and
// report a wall of failures that say nothing about the API. That reasoning is
// stronger still for the probing phases, where a hook may be the thing holding
// a dangerous field at a safe value.
func startHooks(ctx context.Context, engine *runner.Runner, settings config.Config) (func(), error) {
	if len(settings.Hookfiles) == 0 {
		return func() {}, nil
	}

	client, err := hooks.Start(ctx, hooks.Options{
		Language:    settings.Language,
		Hookfiles:   settings.Hookfiles,
		Host:        settings.HooksWorkerHandlerHost,
		Port:        settings.HooksWorkerHandlerPort,
		Timeout:     settings.HooksWorkerTimeout,
		ConnectWait: settings.HooksWorkerConnectWait,
		Stderr:      os.Stderr,
	})
	if err != nil {
		return func() {}, fmt.Errorf("loading hooks: %w", err)
	}
	engine.Hooks = client
	return client.Stop, nil
}

// skipAware translates the runner's "a hook took this out of the run" into
// the one the generation packages understand.
//
// The two sentinels are separate on purpose: the runner reports what it did,
// and fuzz states what a Sender may tell it, without either package having to
// know the other exists.
func skipAware(reply validate.Message, err error) (validate.Message, error) {
	if errors.Is(err, runner.ErrSkippedByHook) {
		return reply, fuzz.ErrSkipped
	}
	// A hook that replaced the generated body ends the case the same way a
	// skip does: nothing was learned about the value that was drawn, so there
	// is nothing to judge. Counted and reported by the caller rather than
	// silently dropped — a probe that did not test what it drew must not look
	// like one that passed.
	if errors.Is(err, runner.ErrChangedByHook) {
		return reply, fuzz.ErrSkipped
	}
	return reply, err
}
