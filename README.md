# vertrag

[![CI](https://github.com/antimatter-studios/vertrag/actions/workflows/ci.yml/badge.svg)](https://github.com/antimatter-studios/vertrag/actions/workflows/ci.yml)
[![Release](https://github.com/antimatter-studios/vertrag/actions/workflows/release.yml/badge.svg)](https://github.com/antimatter-studios/vertrag/actions/workflows/release.yml)
[![Latest release](https://img.shields.io/github/v/release/antimatter-studios/vertrag)](https://github.com/antimatter-studios/vertrag/releases/latest)
[![Coverage](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2Fantimatter-studios%2Fvertrag%2Fbadges%2Fcoverage.json)](https://github.com/antimatter-studios/vertrag/actions/workflows/ci.yml)
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

### Sequences

A description lists operations in whatever order reads well, which is frequently
the read before the create. Run flat, the read asks for something nothing has
made and gets a 404 that says nothing about the server.

OpenAPI's Link Object describes the dependency, and `--sequence` follows it:

```console
vertrag run --sequence openapi.yml http://localhost:4000
```

Links do not add tests. They reorder the ones that exist and fill in the values
the description could not have known, so a run sends exactly as many requests as
it did before. A step whose dependency failed is skipped rather than run and
failed a second time — one root cause, one finding.

### Generated input

A description shows one request per operation, so a run establishes the happy
path and asks nothing else. `vertrag fuzz` draws values from the schema instead:

```console
vertrag fuzz openapi.yml http://localhost:4000
vertrag fuzz --cases 100 --seed 42 openapi.yml    # replay a previous run
```

Two properties, failing in opposite directions. A value the schema permits
should be accepted, and a 4xx means the server and its description disagree
about what is valid. A value it forbids should be rejected with a 4xx, and a
2xx is a validation bypass. A 5xx is a finding either way, because refusing
input is a decision and failing on it is not.

Findings are shrunk to the smallest input that still fails, and the seed is
reported so any finding replays exactly.

It is a separate command rather than a flag on `run` because the two answer
different questions. `run` is deterministic and belongs in CI as a regression
gate; generation is exploratory, and a run that discovers something new on a
Tuesday is a feature there and a broken build here.

## Configuration

vertrag reads `vertrag.yml`. Every setting it takes is in
[`vertrag.example.yml`](vertrag.example.yml), with the reasoning next to it.

```yaml
spec: ./openapi.json               # the API description: OpenAPI 2 or 3
endpoint: http://localhost:4000
hookfiles: ./hooks.js

reporter: [cli, junit]             # a readable log and a machine-readable file
output: ["", report.xml]

checks:
  server-error: true
  content-type: true
  header-schema: false             # off by default; see below
```

Coming from Dredd, a `dredd.yml` is read when no vertrag file is present and
every key it understands means the same thing here — so the upgrade is to change
nothing, and the migration is to rename the file. That fallback is a
convenience with an expected end rather than a second supported format: as the
two diverge it will be removed. vertrag's own settings are read only from a
vertrag file.

`header-schema` validates a response header's value against the JSON Schema the
description gave it — so an `X-Rate-Limit` documented as a non-negative integer
fails when the server answers `banana`.

Dredd compares values for exactly five headers — `content-type`, `accept`, and
the three `accept-*` — and checks only presence for every other one it declares.
It never reads a Header Object's schema at all. So no description has ever had
this enforced, and yours may well carry a header schema that was never true. It
is therefore the one check that starts off; turn it on here or with
`--check-header-schema` when you are ready to read what it finds.

### Setup without a hook file

Logging in, setting a header, and skipping a transaction are what most hook
files spend their lines on, and almost none of it is specific to a project.
Dredd cannot express any of it, so each suite writes it again — and pays a
worker process and a language runtime to run steps that never vary.

```yaml
auth:
  login:
    path: /api/v1/auth/login       # method defaults to POST
    body: {username: admin, password: secret}
  cookie: jwt_token                # keep this one out of Set-Cookie
  except:                          # must go out unauthenticated
    - '/api/v1/auth/login > Login > 401 > application/json'

header:
  - 'X-Trace: on'                  # Dredd's form: every transaction
  - name: X-Mock-Scenario          # vertrag's: only where it applies
    value: absent
    when: {status: 404}

skip:
  - name: '/api/v1/jobs/{id} > Get job > 200 > application/json'
    reason: sends a literal "id"; covered by unit tests
```

A credential can equally come from the response body — `header: 'Authorization:
Bearer {$response.body#/token}'` — because the value is a **runtime expression**,
the same language OpenAPI links use. A static API key needs no `login` at all.

`except` exists because a login endpoint that documents its own 401 cannot
produce one while holding a valid credential.

The conditional `header` form is really there for one job: telling a mock which
failure to simulate, so the error responses a description promises can be
reached at all. Which failure to ask for follows from which response is
expected, which is why the condition is the expected status.

A skip's `reason` is printed with the skip, so a report says *why* forty
transactions did not run rather than only that they did not — a skip list is
where a suite's debt collects, and one that states its reasons is one somebody
can work through. An entry matching no transaction is reported rather than
ignored; it usually means something was renamed.

These keys change what is sent or run, so they are read only from a
`vertrag.yml`. Dredd ignores keys it does not recognise **without a word**, so
honouring them from a `dredd.yml` would leave vertrag authenticated and Dredd
not — two testers disagreeing about what they tested, from one file that looks
shared. A `dredd.yml` carrying them says where to move them.

Anything that must look at a *response* is still a hook, and hooks are
unchanged. The line is deliberate: config covers what does not vary, and no
condition here can grow into a programming language.

## Why

Dredd works, and this project exists to keep it working rather than to replace
it with something subtly different. The reasons for a Go port are operational:

- **One binary.** Dredd needs a Node runtime and a dependency tree; `brew
  install vertrag` needs neither.
- **Testing a Go service shouldn't need a JavaScript toolchain.** A Go project
  whose only reason to carry a `package.json` is its API tests can drop it.
- **Speed.** No interpreter start-up per run.

## How correctness was established

vertrag was not reimplemented from Dredd's documentation. It was built as a port
whose agreement with Dredd was mechanically checked, fixture by fixture, because
the requirement at the time was *the same behaviour*: real projects already
depend on the details, and a hook file addresses transactions by a name built
from the description document, so renaming anything silently disables the hook
rather than failing loudly.

That has been achieved, and it is now history rather than a constraint. **The
differential no longer runs on every commit.** Agreeing with Dredd is not what
makes vertrag right, and a required job saying otherwise shapes design decisions
it should not — vertrag already does things Dredd cannot, and each of those is a
place where the comparison has nothing to say.

What the oracle was protecting is held instead by
[`compile/testdata/golden`](compile/testdata/golden): the transactions every
fixture yields, recorded while the differential passed over all of them, so the
verification is carried forward rather than discarded. Those recordings are the
only thing that can catch a parser regression — the corpus below cannot, because
its server is built from vertrag's own reading of a document, so a misparse
produces a server and a tester wrong in exactly the same way.

The differential is still there for when a second opinion is wanted: run the
`CI` workflow manually, or `make oracle` locally.

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

The reference is *executed*, not snapshotted — its output is not checked into
this repository — so when the differential is run, a behaviour change in either
implementation shows up as a failing diff rather than quietly redefining what
"correct" means.

```console
$ make oracle
ok  github.com/antimatter-studios/vertrag/internal/oracle
```

Agreeing, field for field, as of the last full run:

| Suite | Compared | Covers |
| --- | --- | --- |
| Compile | 59 fixtures | API Blueprint, OpenAPI 2 and OpenAPI 3 — transaction naming, URI expansion, bodies, diagnostics |
| Parse | 40 documents | OpenAPI 3 and OpenAPI 2 end to end: source document to transactions |
| Validate | 27 cases | Pass/fail verdicts and their error text, against Gavel — both JSON Schema dialects |

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

### What faithfulness cost, and what it no longer costs

Faithfulness was paid for deliberately while it was the goal. Dredd's URI
template library is not RFC 6570 — it percent-encodes without zero-padding
(`%A`, not `%0A`) and double-escapes an already-escaped sequence — and vertrag
reproduced both, on the grounds that a "corrected" port would disagree with the
tool people were already running against their servers.

Both are fixed. Each sends the request to a *different URL* than the one the
description names — `%2F` and `%252F` are different paths, and a server given
the second does not have the resource — which makes the result meaningless
rather than merely different, and no amount of compatibility justifies that.

Neither was caught by the differential, and could not have been: Dredd's corpus
contains no already-escaped parameter value and no control character, so the
comparison had nothing to disagree about. An oracle can only difference
behaviour the reference's own fixtures exercise, which is the clearest argument
for not treating agreement as the definition of correct.

Validation still follows Gavel closely, including its error wording, and that is
not sentimentality: the text appears in reports people read and grep, and there
is no better phrasing waiting to replace it. Where a deviation is still kept on
purpose it is marked in the source with why.

## Architecture

Dredd's pipeline is `parse → API Elements → compile → run`, and the split is
load-bearing: only `parse` is format-specific. Everything downstream sees API
Elements, so the rules for naming a transaction, expanding a URI and reporting a
diagnostic are written once and serve every input format.

vertrag keeps that shape:

| Package | Role | State |
| --- | --- | --- |
| `refract` | The API Elements object model | Done |
| `compile` | API Elements → HTTP transactions | Done, oracle-verified |
| `uritemplate` | URI template expansion | Done, oracle-verified |
| `apidesc/openapi3` | OpenAPI 3 → API Elements | Done, oracle-verified |
| `apidesc/openapi2` | Swagger 2.0 → API Elements | Done, oracle-verified |
| `validate` | Response validation (Gavel) | Done, oracle-verified |
| `runner` | Sending requests, judging responses | Done |
| `hooks` | Running Node.js hook files | Done |
| `config` | Reading `vertrag.yml`, and `dredd.yml` while that lasts | Done |
| `reporter` | cli, dot, markdown, html, JUnit output | Done |

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
8. ~~Adversarial input generation, with shrinking~~ — done; `vertrag fuzz`
9. ~~Stateful sequences from OpenAPI links~~ — done; `vertrag run --sequence`
10. Generated input across a sequence, rather than one operation at a time

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
