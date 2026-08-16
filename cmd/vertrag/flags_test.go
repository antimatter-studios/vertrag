package main

import (
	"slices"
	"testing"
)

// Flags written after the positional arguments must be honoured.
//
// Go's flag package stops at the first non-flag argument, so
// `vertrag run api.yml http://host --details` put `--details` in the positional
// list and ignored it without a word. Dredd accepts flags anywhere and its own
// documentation writes them last, so this is the order people actually type —
// and a flag quietly dropped looks exactly like a flag that does nothing.
func TestFlagsAreHonouredAfterPositionalArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"flags first", []string{"--details", "--sorted", "api.yml", "http://host"}},
		{"flags last", []string{"api.yml", "http://host", "--details", "--sorted"}},
		{"flags on both sides", []string{"--details", "api.yml", "http://host", "--sorted"}},
		{"flags between", []string{"api.yml", "--details", "--sorted", "http://host"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			flags, err := parseRunFlags(test.args)
			if err != nil {
				t.Fatalf("parseRunFlags(%v): %v", test.args, err)
			}
			if !flags.details {
				t.Error("--details was dropped")
			}
			if !flags.sorted {
				t.Error("--sorted was dropped")
			}
			if want := []string{"api.yml", "http://host"}; !slices.Equal(flags.positional, want) {
				t.Errorf("positional = %v, want %v", flags.positional, want)
			}
		})
	}
}

// A flag taking a value must keep it, rather than the value becoming a
// positional argument — which is the way an interspersed parse most easily goes
// wrong.
func TestValueFlagsKeepTheirArgumentAfterPositionals(t *testing.T) {
	flags, err := parseRunFlags([]string{
		"api.yml", "http://host",
		"--only", "GET /a > op > 200 > application/json",
		"--header", "X-A: 1",
		"--reporter", "junit",
	})
	if err != nil {
		t.Fatalf("parseRunFlags: %v", err)
	}

	if want := []string{"api.yml", "http://host"}; !slices.Equal(flags.positional, want) {
		t.Errorf("positional = %v, want %v — a flag's value leaked into it", flags.positional, want)
	}
	if len(flags.only) != 1 || flags.only[0] != "GET /a > op > 200 > application/json" {
		t.Errorf("only = %v", flags.only)
	}
	if len(flags.headers) != 1 || flags.headers[0] != "X-A: 1" {
		t.Errorf("headers = %v", flags.headers)
	}
	if flags.reporterName != "junit" {
		t.Errorf("reporter = %q", flags.reporterName)
	}
}

// Repeating a flag on both sides of a positional must accumulate, not replace:
// the repeatable flags are how a run is narrowed, and losing one silently
// widens what gets tested.
func TestRepeatableFlagsAccumulateAcrossPositionals(t *testing.T) {
	flags, err := parseRunFlags([]string{
		"--header", "X-A: 1", "api.yml", "--header", "X-B: 2", "http://host", "--header", "X-C: 3",
	})
	if err != nil {
		t.Fatalf("parseRunFlags: %v", err)
	}
	if want := []string{"X-A: 1", "X-B: 2", "X-C: 3"}; !slices.Equal([]string(flags.headers), want) {
		t.Errorf("headers = %v, want %v", flags.headers, want)
	}
	if want := []string{"api.yml", "http://host"}; !slices.Equal(flags.positional, want) {
		t.Errorf("positional = %v, want %v", flags.positional, want)
	}
}
