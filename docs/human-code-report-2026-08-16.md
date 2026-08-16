# Human Code review — 16 August 2026

**Scope:** full codebase (`vertrag`, ~14,900 non-test Go lines across 17 packages)
**Found:** 6 items · **Fixed:** 5 · **Skipped:** 1 (with reasons below)

---

## Changes Made

### 1. `runRun` did eight jobs in 190 lines

**Files:** [cmd/vertrag/run.go](../cmd/vertrag/run.go)

The command's entry point parsed flags, resolved a config file, merged the two,
validated the result, built reporters, read and compiled the description,
filtered and sorted transactions, started a hook worker, ran the suite and
reported — in one function, with the flag variables as a row of eighteen
pointer locals threaded through all of it.

**Before**

```go
func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := fs.String("config", "", "...")
	endpoint := fs.String("endpoint", "", "...")
	// ...sixteen more...
	if *endpoint != "" {
		settings.Endpoint = *endpoint
	}
	// ...190 lines total...
```

**After**

```go
type runFlags struct { /* named fields */ }

func parseRunFlags(args []string) (runFlags, error)
func settingsFor(f runFlags) (config.Config, error)

func runRun(args []string) error {
	flags, err := parseRunFlags(args)
	settings, err := settingsFor(flags)
	// ...123 lines, one job per section...
```

**Why it's better:** the merge rule — a flag that was given wins over the file —
is now stated once, in a function whose whole subject is that rule, rather than
inferred from eighteen scattered `if *x != ""` blocks. It is also testable
without a server, a description or a subprocess, and three tests now pin it:
flags overriding, a named reporter replacing the configured list, and the
repeatable flags accumulating rather than replacing.

### 2. Two truncation limits for one report

**Files:** [reporter/junit.go](../reporter/junit.go), [reporter/cli.go](../reporter/cli.go)

`const limit = 2000` appeared in both files, each with its own copy of the
"… (N bytes truncated)" logic.

**Before**

```go
// junit.go
func truncate(body string) string {
	const limit = 2000
	...
}

// cli.go
const limit = 2000
if len(body) > limit {
	body = body[:limit] + fmt.Sprintf("… (%d bytes truncated)", len(body)-limit)
}
```

**After**

```go
// junit.go — one home, one name, one rule
const maxReportedBodyBytes = 2000
func truncate(body string) string { ... }

// cli.go
body = truncate(body)
```

**Why it's better:** two reports of the same failure truncating at different
points cannot be compared, and a constant declared twice drifts the first time
anyone tunes it. The name says what the number is for, which the bare `2000`
did not.

### 3. ANSI escape codes declared twice

**Files:** [cmd/vertrag/fuzzcmd.go](../cmd/vertrag/fuzzcmd.go), [reporter/cli.go](../reporter/cli.go)

`printFinding` declared its own `red`, `dim` and `reset` and its own `paint`
closure, duplicating the reporter package's.

**Before**

```go
const (
	red   = "\033[31m"
	dim   = "\033[2m"
	reset = "\033[0m"
)
paint := func(code, text string) string {
	if !color { return text }
	return code + text + reset
}
```

**After**

```go
paint := func(code, text string) string { return reporter.Paint(color, code, text) }
```

**Why it's better:** two sets of escape codes for one tool drift the first time
anyone changes a colour, and the result is output that looks like it came from
two programs. `reporter.Paint` also carries the `--no-color` rule, so the
command cannot forget it.

### 4. `1e6` where a duration was meant

**Files:** [reporter/cli.go](../reporter/cli.go)

**Before** `result.Duration.Round(1e6)`
**After** `result.Duration.Round(time.Millisecond)`

**Why it's better:** the reader no longer has to know that a `time.Duration` is
nanoseconds and divide in their head to discover the intent was milliseconds.

### 5. Report and tests

Three new tests in [cmd/vertrag/settings_test.go](../cmd/vertrag/settings_test.go)
cover the extracted merge, which had no direct coverage before — it was reachable
only through a subprocess running a whole suite.

---

## Items Skipped

| Item | Reason |
| --- | --- |
| `generateValue` (128 lines), `convertSubSchema` (119), `probe` (109) | *Acceptable pattern.* Each is a single dispatch — one case per JSON Schema type, or one pass of a fixed pipeline. Splitting them would scatter a decision that reads as a table, and the skill's own rule against unjustified abstraction applies: there is no duplication to extract, only length. |
| `runFuzz` (105 lines) | *Below threshold this pass.* It has the same shape as `runRun` and would benefit from the same treatment, but it is half the size and the two commands' flag sets are not yet similar enough to share a type. Worth revisiting when a third command appears. |
| `corpus/server.go` literals (`4096`, `200`) | *Acceptable pattern.* Test-support code, both used once, both adjacent to a comment saying what they are for. |

---

## Test Results

| | Before | After |
| --- | --- | --- |
| Packages passing | 17 | 17 |
| Tests failing | 0 | 0 |
| Coverage | 80.4% | 80.4% |
| `go vet` | clean | clean |
| `gofmt -l` | clean | clean |
| Race detector | clean | clean |

No behaviour changed. The extraction is pure movement plus three new tests over
code that previously had none of its own.
