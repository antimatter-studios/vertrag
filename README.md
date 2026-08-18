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
depends on — the configuration keys, hook files, and the names hooks address
transactions by. Where Dredd is simply wrong, vertrag is not: see
[Where vertrag differs](#where-vertrag-differs-from-dredd).

> **Status: usable for OpenAPI 3 and OpenAPI 2.** vertrag reads a description,
> sends the requests it promises, validates the responses and exits non-zero on
> failure — with an existing Dredd configuration and Node.js hook files
> working unchanged. See [Roadmap](#roadmap).

## Install

```console
brew install antimatter-studios/tap/vertrag
```

Or download a binary from [releases](https://github.com/antimatter-studios/vertrag/releases).
Node.js is needed only if you use hook files.

### In CI, a VM or a machine image

Install it the same way in every environment that runs the suite, from one
script both your local provisioning and your pipeline call. Installing it in CI
alone gives the pipeline a tester that a developer's machine does not have, and
the suite then passes in CI and fails locally with `command not found` — which
is worse than not having it, because it looks like the change broke something.

Pin the version *and* the checksum. Every release publishes `checksums.txt` over
the archives and `binaries.txt` over the binaries inside them:

```sh
VERTRAG_VERSION=0.4.0
arch=$(uname -m); [ "$arch" = "aarch64" ] && arch=arm64
base="https://github.com/antimatter-studios/vertrag/releases/download/v${VERTRAG_VERSION}"
file="vertrag-${VERTRAG_VERSION}-linux-${arch}.tar.gz"

curl -fsSL "${base}/${file}" -o "${file}"
curl -fsSL "${base}/checksums.txt" | grep " ${file}\$" | sha256sum -c -
tar -xzf "${file}" -C /usr/local/bin vertrag
```

Run it somewhere it can write `/usr/local/bin`: a Dockerfile or a provisioning
script is already root, and anywhere else needs `sudo`. That is worth a sentence
because of how it fails. A first install refuses the directory, which is clear
enough. An **upgrade** fails at the `tar` line with

```
tar: vertrag: Cannot open: File exists
```

which points at the wrong thing entirely. Nothing is wrong with the file that is
already there — `tar` replaces one happily, even a read-only one. Replacing it
means unlinking it, and unlinking is permission on the *directory*.

The checksum matters more than it looks in a pipeline whose image tag is a
content hash over its build scripts: a release re-pointed at different bytes
would not change that tag, so two images could share a tag and hold different
testers. A pinned checksum makes that impossible rather than unlikely.

## Use

A configured project needs no arguments — vertrag reads `./vertrag.yml`, whose
keys are a superset of Dredd's, so migrating is `mv dredd.yml vertrag.yml`:

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

# JUnit XML for CI — Jenkins, GitLab, GitHub Actions. The suite's <properties>
# say which vertrag, description, endpoint and config file produced the report.
vertrag run --reporter junit --output report.xml

vertrag run --reporter dot                          # one character per transaction
vertrag run --reporter html --output report.html    # a page to publish

# The traffic itself, rather than a verdict about it.
vertrag run --reporter har --output run.har         # open it in devtools
vertrag run --reporter vcr --output cassette.yml    # replay it from a suite
```

### Narrowing a run

A transaction is addressed by name, method, tag or operationId, and each of
those can either select what runs or leave it out:

```console
vertrag run --tag orders --exclude-method DELETE    # the orders API, read-only
vertrag run --only-matching '^/api/v1/'            # by name, as a pattern
vertrag run --exclude-matching '> 5[0-9][0-9] >'   # not the error variants
```

An exclude wins over every include, because the two are meant to be written
together and the exclude is the more specific half of the pair. The same keys go
in `vertrag.yml`, where a suite records the operation it must never send.

Both halves are checked rather than trusted. A pattern that does not compile
stops the run and names itself, and a value matching no transaction is reported
— an include matching nothing tests an API that looks like it has nothing to
test, and an exclude matching nothing sends every request it was written to
prevent.

### Pointing a probing phase at an API that can do something irreversible

`coverage` and `fuzz` generate bodies, which is the point of them and also the
reason a real project often cannot use them: a trading API documents
`POST /orders` with a `dry_run` flag, the schema permits `false`, so generation
draws `false`, and the probe meant to test input handling places a real order.
Nothing in OpenAPI marks that field as the one that matters.

```yaml
fuzz:
  pin:
    dry_run: true      # held in every generated body, in both probing phases
  accept: [409, 422]   # refusals that are business rules, not contract breaches
```

**A pin that names a field no request body declares stops the run.** That is the
failure worth designing against — a typo, or a field renamed in the description
since the pin was written, otherwise leaves a configuration that reads exactly
like a safety control and holds nothing. The run also reports how many bodies
each pin actually held, because *configured* and *engaged* are different facts
and only the second one is safety.

`accept` exists because a description does not carry business rules, so a
generated body can satisfy every documented constraint and still be refused.
**Every excused answer is counted and the count is printed** — that is the
condition on which suppressing findings is worth offering at all, since a suite
that quietly stopped testing anything then shows up as a number somebody can
read. A 5xx can never be accepted: that is the server breaking rather than
refusing, and it is the finding these phases exist to produce.

Neither of these makes an unsafe API safe. They make the decision expressible.

### Recordings

Every other reporter says whether the API agreed with its description. A
recording says what actually went over the wire, which is what somebody
reproducing a CI failure on their own machine needs — and the alternative was
rerunning the suite behind a proxy, which is exactly what they cannot do.

`--reporter har` writes an HTTP Archive: the file a browser's network panel
exports, and one that devtools, Insomnia, Postman and every HAR viewer imports.
Each entry is named after the transaction it belongs to, so a wall of
similar-looking requests reads as the run you remember.

`--reporter vcr` writes a VCR cassette, the YAML format Ruby's VCR wrote first
and vcrpy and Betamax copied. That is the one to commit: a suite can play the
API's real answers back without the API being up.

Both record only what was sent. A transaction a hook removed, or a sequenced
step whose dependency failed, produces no entry — there was no traffic — so a
recording holds fewer entries than the JUnit file beside it has test cases. A
request that got no answer at all is still recorded, since that is usually the
one worth resending.

Credential header values are replaced with `<redacted>` in both, on by default,
because a recording is committed and shared far more readily than a terminal log
is. **Bodies are not touched**, the same as in every other reporter: there is no
way to know which field of a payload is a secret, and guessing would hide the
payload a failure exists to show. So a recording of a login exchange still holds
the password that was posted — worth knowing before committing one.

There is no `vertrag replay` yet. Recording is the half that pays for itself
immediately; replaying needs a decision about how a recorded response is matched
to a request, which is the whole of what makes a replay library opinionated.

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
  max-response-time: 750ms         # unset by default; nothing is timed
```

Coming from Dredd, every key a `dredd.yml` understands means the same thing
here, so the migration is `mv dredd.yml vertrag.yml` and nothing else. vertrag
used to read a `dredd.yml` where no vertrag file was present, which made
adoption a no-op; that fallback is gone. It meant a key's meaning depended on
the name of the file holding it — vertrag's own settings had to be refused from
one of the two names, or two testers reading one shared-looking file would have
silently tested different things. A directory holding only a `dredd.yml` is now
told to rename it rather than run with no configuration at all.

A file named with `--config` is read in full whatever it is called, including
`--config dredd.yml`: naming a file says what finding one cannot.

`header-schema` validates a response header's value against the JSON Schema the
description gave it — so an `X-Rate-Limit` documented as a non-negative integer
fails when the server answers `banana`.

Dredd compares values for exactly five headers — `content-type`, `accept`, and
the three `accept-*` — and checks only presence for every other one it declares.
It never reads a Header Object's schema at all. So no description has ever had
this enforced, and yours may well carry a header schema that was never true. It
is therefore the one check that starts off; turn it on here or with
`--check-header-schema` when you are ready to read what it finds.

`max-response-time` reports a transaction that took longer than the bound you
give it — `--max-response-time 750ms` for a single run. It is the one check the
description cannot ask for: OpenAPI has no way to write "this endpoint answers
within 750ms", so the number can only come from you, and there is no default,
because a bound vertrag invented would be green on the machine that mattered and
red on somebody's laptop.

Its finding is labelled like the others rather than reported as a contract error,
since nothing the document promised was contradicted — the status, the headers
and the body are all what it said they would be. It is measured over the whole
transaction, which is why it does not mix with `transport.delay`: a run pausing
between requests to spare a throttled server spends that pause against the bound
it is then judged by.

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
production OpenAPI 3 service, with its own Dredd configuration and a 431-line
hook file,
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
| `apidesc/openapi3` | OpenAPI 3 → API Elements | Done, oracle-verified; 3.0, 3.1 and 3.2 |
| `apidesc/openapi2` | Swagger 2.0 → API Elements | Done, oracle-verified |
| `validate` | Response validation (Gavel) | Done, oracle-verified |
| `runner` | Sending requests, judging responses | Done |
| `hooks` | Running Node.js hook files | Done |
| `config` | Reading `vertrag.yml` | Done |
| `reporter` | cli, dot, markdown, html, JUnit, HAR and VCR output | Done |

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
5. ~~Hooks and configuration~~ — done; hook files run unchanged
6. ~~OpenAPI 2 parser~~ — done, oracle-verified
7. ~~Reporters~~ — done; cli, dot, markdown, html, JUnit XML, and HAR and
   VCR recordings of the traffic
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
every configuration key, the hook API and its wire protocol, and the names hooks
address transactions by. There is no better transaction name — only the one
already written in someone's hook file. The *filename* is not on this list — a
`dredd.yml` is no longer discovered, and renaming it is the migration.

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

Some setup cannot be written as configuration: deriving a request field from an
earlier response, seeding a fixture, pinning a value the generator must not
touch. That is what hooks are for.

vertrag is a Go program and cannot load a JavaScript or Python file into
itself, so it ships a small worker for each — embedded in the binary, so
there is nothing to install and the worker can never be a version out of step
with the binary driving it. The worker runs your hook files in a real
interpreter and exchanges transactions over a socket.

```yaml
language: python                   # or nodejs, the default
hookfiles: ./hooks.py
```

Both languages expose the same API and are tested against each other, so a
hook that works in one behaves identically in the other.

```python
import vertrag_hooks as hooks

@hooks.before_each
def pin_safety(transaction):
    body = hooks.get_json(transaction)
    body["dry_run"] = True          # whatever the generator drew
    hooks.set_json(transaction, body)

@hooks.before("createOrder")        # by operationId, or by name, or a glob
def authenticate(transaction):
    transaction["request"]["headers"]["Authorization"] = hooks.stash["token"]

@hooks.after("/api/session > Log in > 200 > application/json")
def keep_token(transaction):
    hooks.stash["token"] = hooks.get_json(transaction, "real")["token"]
```

```javascript
const hooks = require('vertrag_hooks');   // the same name Python imports

hooks.beforeEach((transaction) => {
  transaction.request.headers['X-Tenant'] = 'acme';
});
```

One name in both languages, and the underscore is what allows that: a hyphen
is a syntax error in a Python import, where it parses as subtraction. Node
accepts `vertrag-hooks` too, for the muscle memory.

Python needs `python3` on PATH and uses nothing but the standard library — no
virtualenv, no pip install. Node hook files may be TypeScript if the project
has `tsx` or `ts-node`; without one, vertrag says so rather than letting Node
fail on syntax it cannot parse.

`beforeAll`, `beforeEach`, `before(name)`, `beforeEachValidation`,
`after(name)`, `afterEach` and `afterAll` all work, as do `transaction.skip`,
`transaction.fail`, and rewriting the request or the expectation.

Python adds two things Node does not have, because Python hook files are new
and had nothing to stay compatible with: a named hook may select by
`operationId` or by a glob (`@hooks.before("/api/*")`) as well as by the full
transaction name, and `hooks.stash` is a dictionary shared between hooks, for
the value one hook reads out of a response and another needs in a request.

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
