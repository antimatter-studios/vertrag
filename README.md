# vertrag

[![CI](https://github.com/antimatter-studios/vertrag/actions/workflows/ci.yml/badge.svg)](https://github.com/antimatter-studios/vertrag/actions/workflows/ci.yml)
[![Release](https://github.com/antimatter-studios/vertrag/actions/workflows/release.yml/badge.svg)](https://github.com/antimatter-studios/vertrag/actions/workflows/release.yml)
[![Latest release](https://img.shields.io/github/v/release/antimatter-studios/vertrag)](https://github.com/antimatter-studios/vertrag/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/antimatter-studios/vertrag/total)](https://github.com/antimatter-studios/vertrag/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/antimatter-studios/vertrag)](go.mod)
[![License](https://img.shields.io/github/license/antimatter-studios/vertrag)](LICENSE)

Contract-test your HTTP API against its description document — a single static
binary, no Node runtime, no `node_modules`.

vertrag reads an API description (OpenAPI 3 or OpenAPI 2), derives the HTTP
requests that description promises, sends them to a running server, and checks
the responses against what was promised.

It began as a Go implementation of [Dredd](https://github.com/antimatter-studios/dredd),
whose upstream is archived, and stays compatible with the things a project
depends on — `dredd.yml`, hook files, and the names hooks address transactions
by. Where Dredd is simply wrong, vertrag is not: see
[Where vertrag differs](#where-vertrag-differs-from-dredd).

> **Status: usable for OpenAPI 3 and OpenAPI 2.** vertrag reads a description,
> sends the requests it promises, validates the responses and exits non-zero on
> failure — with an existing `dredd.yml` and Node.js hook files working
> unchanged. See [Roadmap](#roadmap).

## Install

```console
brew install antimatter-studios/tap/vertrag
```

Or download a binary from [releases](https://github.com/antimatter-studios/vertrag/releases).
Node.js is needed only if you use hook files.

## Use

A project already configured for Dredd needs no arguments — vertrag reads the
same `dredd.yml`:

```console
$ vertrag run
pass: GET Machines > /machines > List machines > 200 > application/json (3ms)
fail: GET Machines > /machines/{id} > Read a machine > 200 > application/json (2ms)

FAIL: Machines > /machines/{id} > Read a machine > 200 > application/json
  body: At '/name' Missing required property: name
  ...

2 total, 1 passing, 1 failing, 0 errors, 0 skipped
```

Or point it at a description and an endpoint:

```console
vertrag run openapi.yml http://localhost:4000
vertrag run --dry-run openapi.yml   # what would be sent, without sending it
vertrag compile openapi.yml         # the transactions, as JSON

# JUnit XML for CI — Jenkins, GitLab, GitHub Actions
vertrag run --reporter junit --output report.xml

vertrag run --reporter dot                          # one character per transaction
vertrag run --reporter html --output report.html    # a page to publish
```

## Configuration

vertrag reads `vertrag.yml`, which is a **superset of `dredd.yml`**: every key
Dredd understands means the same thing, and vertrag's own settings are added
alongside. It looks for `vertrag.yml`, `vertrag.yaml`, then `dredd.yml`.

So the upgrade path is: change nothing, and vertrag reads what you have. Rename
the file when you want vertrag's own settings. See
[`vertrag.example.yml`](vertrag.example.yml).

```yaml
blueprint: ./openapi.json          # Dredd's keys, unchanged
endpoint: http://localhost:4000
hookfiles: ./dredd-hooks.js

reporter: [cli, junit]             # a readable log and a machine-readable file
output: ["", report.xml]

checks:                            # vertrag's own
  server-error: true
  content-type: true
  header-schema: false             # off by default; see below
```

`header-schema` validates a response header's value against the JSON Schema the
description gave it — so a `X-Rate-Limit` documented as a non-negative integer
fails when the server answers `banana`. Dredd only checks that a declared header
is *present*, so no description has ever had this enforced and yours may well
contain a header schema that was never true. It is therefore the one check that
starts off; turn it on here or with `--check-header-schema` when you are ready
to read what it finds.

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
| Parse | 38 documents | OpenAPI 3 and OpenAPI 2 end to end: source document to transactions |
| Validate | 20 cases | Pass/fail verdicts and their error text, against Gavel |

Agreement includes the generated request and response bodies, the JSON Schemas
attached to responses, and the parser diagnostics down to their exact wording,
source positions and occurrence counts.

The suites are separate on purpose. The compile stage is format-agnostic, so it
can be checked for all three formats using the API Elements Dredd ships — which
is why it was verified long before any parser existed. A parse failure is
therefore always a parser failure, never an ambiguous end-to-end one.

It has also been run against a real API: 51 paths and 140 transactions of a
production OpenAPI 3 service, with its own `dredd.yml` and a 431-line hook file,
against a live server. vertrag and Dredd reported the same 35 passing, 15
failing, 90 skipped — the same fifteen failures, not merely the same count.

### What faithfulness costs

Faithfulness costs something, and the port pays it deliberately.

Validation error text is reproduced down to the JavaScript engine's own JSON
parse messages, because a body that fails to parse is reported to the user with
that wording. Dredd's URI template library is not RFC 6570 — it percent-encodes without zero-padding
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
| `internal/apidesc/openapi2` | Swagger 2.0 → API Elements | Done, oracle-verified |
| `internal/validate` | Response validation (Gavel) | Done, oracle-verified |
| `internal/runner` | Sending requests, judging responses | Done |
| `internal/hooks` | Running Node.js hook files | Done |
| `internal/config` | Reading `dredd.yml` | Done |
| `internal/reporter` | cli, dot, markdown, html, JUnit output | Done |

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
3. ~~Response validation~~ — done, oracle-verified against Gavel
4. ~~Transaction runner~~ — done
5. ~~Hooks and `dredd.yml`~~ — done; hook files run unchanged
6. ~~OpenAPI 2 parser~~ — done, oracle-verified
7. ~~Reporters~~ — done; cli, dot, markdown, html and JUnit XML
8. Adversarial input generation, with shrinking

## Not supported: API Blueprint

Dredd also reads API Blueprint. vertrag does not, and will not.

The format is finished: its repository is archived, its parser (drafter) is
archived, and Apiary — who created it — was bought in 2017. Its parser draws
about a tenth of the downloads the OpenAPI ones do. Supporting it would mean
either linking 2 MB of Emscripten-compiled C++, which ends the static binary, or
writing a Markdown-based format parser from its specification.

A Blueprint document therefore reports that it is unsupported rather than being
parsed badly. The compiler still handles Blueprint semantics — its transaction
example numbering is covered by 29 fixtures — so if that decision is ever
revisited, only the parser is missing and its expected output is already
recorded.

## Where vertrag differs from Dredd

Compatibility is kept where it is convention and dropped where Dredd is wrong.

**Kept**, because breaking it breaks working projects and improves nothing:
`dredd.yml`, the hook API and its wire protocol, and the names hooks address
transactions by. There is no better transaction name — only the one already
written in someone's hook file.

**Dropped**, because reproducing a defect makes the tests wrong:

| | Dredd | vertrag |
| --- | --- | --- |
| Percent-encoding | `%A` for a newline; `%2F` re-escaped to `%252F` | RFC 3986 |
| Body validation | Full JSON Schema | Full JSON Schema, drafts 4 to 2020-12 |
| Diagnostics | Two validators, two wordings for one problem | One wording |

A run against a live API will therefore report failures Dredd would not. Those
are contract violations that were previously going unnoticed, not regressions,
and vertrag labels them as checks Dredd does not make so an upgrade is not
mistaken for one.

## Hooks

Dredd loads Node.js hook files into its own process. vertrag is a Go program and
cannot, so it ships a small Node worker — embedded in the binary — which runs
the hook files for real and exchanges transactions over a socket using Dredd's
own worker protocol. Hook files are unchanged.

`beforeAll`, `beforeEach`, `before(name)`, `beforeEachValidation`,
`after(name)`, `afterEach` and `afterAll` all work, as do `transaction.skip`,
`transaction.fail`, and rewriting the request or the expectation.

One inherited behaviour is worth knowing, because it surprises people: Dredd
builds the request URL from `transaction.fullPath`, which is computed *before*
hooks run. A hook that rewrites `transaction.request.uri` — or assigns
`transaction.fullUrl` — changes nothing. vertrag does the same. Being quietly
more helpful would make the same hook file behave differently under each tool.

Hooks address transactions by name, and that name includes the API title:

```
inPACE > /api/v1/auth/login > Login > 200 > application/json
^^^^^^ info.title
```

`info.title` is required by the specification, so the prefix is always there. A
hook registered against the name without it silently never runs.

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
