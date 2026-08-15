# vertrag

Contract-test your HTTP API against its description document — a single static
binary, no Node runtime, no `node_modules`.

vertrag is a Go implementation of [Dredd](https://github.com/antimatter-studios/dredd).
It reads an API description (OpenAPI 3, OpenAPI 2, API Blueprint), derives the
HTTP requests that description promises, sends them to a running server, and
checks the responses against what was promised.

> **Status: early.** Reading an OpenAPI 3 document and deriving the transactions
> to test is complete and verified against Dredd, byte for byte. Executing those
> transactions — the runner, response validation, hooks and reporters — is not
> written yet, so vertrag cannot test a live API today. See
> [Roadmap](#roadmap).

## Why

Dredd works, and this project exists to keep it working rather than to replace
it with something subtly different. The reasons for a Go port are operational:

- **One binary.** Dredd needs a Node runtime and a dependency tree; `brew
  install vertrag` needs neither.
- **Testing a Go service shouldn't need a JavaScript toolchain.** A Go project
  whose only reason to carry a `package.json` is its API tests can drop it.
- **Speed.** No interpreter start-up per run.

## Dredd is the oracle

The hard requirement is not "a tool that tests APIs" — it is *the same
behaviour*, because real projects already depend on the details. A hook file
addresses a transaction by a name built from the description document; renaming
anything silently disables the hook rather than failing loudly. So this is not a
reimplementation from the documentation. It is a port whose agreement with Dredd
is mechanically checked.

For every fixture, both implementations run over the same input and their output
must match:

```
                  ┌────────────────────────────┐
  API Elements ──►│  Dredd (Node, reference)   │──► transactions ┐
      fixture     └────────────────────────────┘                 │  compared
                  ┌────────────────────────────┐                 │  field by
              └──►│  vertrag (Go)              │──► transactions ┘  field
                  └────────────────────────────┘
```

The reference is *executed*, not snapshotted. Its expected output is not checked
into this repository, so bumping the pinned Dredd version turns any behaviour
change into a failing diff instead of quietly redefining what "correct" means.

```console
$ make oracle
ok  github.com/antimatter-studios/vertrag/internal/oracle
```

Currently agreeing, field for field:

| Suite | Compared | Covers |
| --- | --- | --- |
| Compile | 59 fixtures | API Blueprint, OpenAPI 2 and OpenAPI 3 — transaction naming, URI expansion, bodies, diagnostics |
| Parse | 5 documents | OpenAPI 3 end to end: source document to transactions |

Agreement includes the generated request and response bodies, the JSON Schemas
attached to responses, and the parser diagnostics down to their exact wording,
source positions and occurrence counts.

The suites are separate on purpose. The compile stage is format-agnostic, so it
can be checked for all three formats using the API Elements Dredd ships — which
is why it was verified long before any parser existed. A parse failure is
therefore always a parser failure, never an ambiguous end-to-end one.

Formats with no parser yet do not fail the suite; they report a skip naming what
is uncovered:

```console
--- SKIP: TestParseMatchesReference/apib
--- SKIP: TestParseMatchesReference/openapi2
```

### What faithfulness costs

Faithfulness costs something, and the port pays it deliberately. The reference's
URI template library is not RFC 6570 — it percent-encodes without zero-padding
(`%A`, not `%0A`), and double-escapes an already-escaped sequence. Those are
defects, and they are reproduced here on purpose, because they are visible in
the URIs Dredd requests. A "corrected" port would disagree with the tool people
are already running against their servers. Each such deviation is marked in the
source with why it is kept.

## Architecture

Dredd's pipeline is `parse → API Elements → compile → run`, and the split is
load-bearing: only `parse` is format-specific. Everything downstream sees API
Elements, so the rules for naming a transaction, expanding a URI and reporting a
diagnostic are written once and serve every input format.

vertrag keeps that shape:

| Package | Role | State |
| --- | --- | --- |
| `internal/refract` | The API Elements object model | Done |
| `internal/compile` | API Elements → HTTP transactions | Done, oracle-verified |
| `internal/uritemplate` | URI template expansion | Done, oracle-verified |
| `internal/apidesc/openapi3` | OpenAPI 3 → API Elements | Done, oracle-verified |
| `internal/apidesc` (OpenAPI 2) | Swagger → API Elements | Not started |
| `internal/apidesc` (API Blueprint) | Blueprint → API Elements | Not started |
| Runner, hooks, reporters | Executing transactions and reporting | Not started |

Porting `compile` first was the cheap move: it is format-agnostic, so it covers
API Blueprint, OpenAPI 2 and OpenAPI 3 at once, and Dredd ships pre-parsed API
Elements fixtures for all three — meaning it could be fully verified before a
single parser existed.

That also defines the parsers' target precisely. Each corpus entry pairs a
source document with the API Elements Dredd parses it into; a parser is finished
when it reproduces its pair.

## Roadmap

1. ~~API Elements model and the transaction compiler~~ — done, oracle-verified
2. ~~OpenAPI 3 parser~~ — done, oracle-verified; the format
   [inpace](https://github.com/semdatex/inpace) uses
3. Transaction runner and response validation
4. Hooks, over Dredd's existing worker protocol, so current hook files and
   `dredd.yml` keep working unchanged
5. Reporters (CLI, dot, markdown, xunit, HTML)
6. OpenAPI 2 parser
7. API Blueprint parser — pure Go, so the binary stays static

The end-to-end acceptance test is inpace: vertrag must run its existing
`dredd.yml` and its 431-line Node hook file, and reach the same verdict as
Dredd.

## Development

```console
make build        # build the binary
make test         # unit tests, no Node required
make oracle       # differential test against the real Dredd
```

The oracle needs Node to run the reference. `make oracle-deps` installs it; the
suite skips rather than fails when it is missing, so a fresh checkout does not
report a wall of failures that are really one missing install step.

## Licence

MIT. Dredd is MIT, originally by Apiary Czech Republic, s.r.o.; the test corpus
in `oracle/corpus/` is copied from it under that licence — see
[`oracle/corpus/NOTICE.md`](oracle/corpus/NOTICE.md).
