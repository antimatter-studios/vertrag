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

vertrag reads an API description (OpenAPI 3, OpenAPI 2, or a GraphQL schema),
derives the HTTP requests that description promises, sends them to a running
server, and checks the responses against what was promised.

It began as a Go implementation of [Dredd](https://github.com/antimatter-studios/dredd),
whose upstream is archived, and stays compatible with the things a project
depends on — the configuration keys, the hook API, and the names hooks address
transactions by. Where Dredd is simply wrong, vertrag is not: see
[Where vertrag differs](#where-vertrag-differs-from-dredd).

> **Status: usable for OpenAPI 3, OpenAPI 2 and GraphQL.** vertrag reads a
> description, sends the requests it promises, validates the responses and exits
> non-zero on failure. A project coming from Dredd keeps its configuration keys
> and its Node.js hooks, after [two renames](#where-vertrag-differs-from-dredd).
> A GraphQL schema is tested from its query root, with
> [mutations withheld until they are asked for](#graphql). See
> [Roadmap](#roadmap).

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
VERTRAG_VERSION=0.27.0   # the release you have tested against
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
keys are a superset of Dredd's, so migrating the file is
`mv dredd.yml vertrag.yml`:

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

# A generated description can be read directly from its endpoint, too.
vertrag run https://api.example.test/openapi.json https://api.example.test
vertrag compile https://api.example.test/openapi.json
vertrag doctor https://api.example.test/openapi.json https://api.example.test

# JUnit XML for CI — Jenkins, GitLab, GitHub Actions. The suite's <properties>
# say which vertrag, description, endpoint and config file produced the report.
vertrag run --reporter junit --output report.xml

vertrag run --reporter dot                          # one character per transaction
vertrag run --reporter html --output report.html    # a page to publish
vertrag run --reporter markdown --output report.md  # a table and every failure
vertrag run --reporter json --output report.json    # every verdict, for a script to read

# The traffic itself, rather than a verdict about it.
vertrag run --reporter har --output run.har         # open it in devtools
vertrag run --reporter vcr --output cassette.yml    # replay it from a suite
```

HTTP and HTTPS description URLs also work in `spec:`. Remote descriptions use
a separate HTTP client with a 30-second timeout and a 32 MiB response limit;
the tested API's configured credentials and request headers are not forwarded
to the description host. Fetch errors fail the command before any API request.
The fetched bytes go through the same parser and compiler as a saved file.
Reports still record which source was used. `doctor` fetches a URL description
but sends no requests to the API under test.

### Narrowing a run

A transaction is addressed by name, method, tag or operationId, and each of
those can either select what runs or leave it out:

```console
vertrag run --tag orders --exclude-method DELETE    # the orders API, read-only
vertrag run --only-matching '^/api/v1/'            # by name, as a pattern
vertrag run --exclude-matching '> 5[0-9][0-9] >'   # not the error variants
vertrag run --exclude-example '_unavailable$'      # not these named exchanges
```

`--exclude-example` matches the example name an exchange was paired by (see
[Named examples are exchanges](#named-examples-are-exchanges)), not the
transaction name, which does not carry it: one documented status can hold
several named exchanges, and only that name tells them apart. An exchange no
name chose has an empty one.

An exclude wins over every include, because the two are meant to be written
together and the exclude is the more specific half of the pair. The same keys go
in `vertrag.yml`, where a suite records the operation it must never send.

Both halves are checked rather than trusted. A pattern that does not compile
stops the run and names itself, and a value matching no transaction is reported
— an include matching nothing tests an API that looks like it has nothing to
test, and an exclude matching nothing sends every request it was written to
prevent.

### Checking the configuration before trusting a result

```console
$ vertrag doctor
2 transaction(s) compiled from ./api.yml

5 setting(s) reach nothing:
  auth.login.path: "/login" matches no transaction, so the operation that grants
    the credential will be sent it like any other, and a server may read that as
    already authenticated
  fuzz.pin: "dry_runn" matches no transaction, so nothing is held and generation
    will send whatever the schema permits
  skip: "also not a transaction" matches no transaction, so the operation it
    names will run and be probed like any other
  …
```

A setting that **names** something — a transaction, a path, a field — can match
nothing, and matching nothing looks exactly like matching everything it was
meant to. That failure costs coverage rather than correctness: the run is green
because it stopped testing, and nothing says so. Every such bug this tool has
had was of that shape.

`doctor` answers it against the description, before a run, **sending nothing** —
so the question "is this configuration doing what it says" can be asked from a
laptop against an API that is not up. It exits non-zero when something reaches
nothing, so a pipeline can gate on it, and says nothing at all when the
configuration is sound.

A new setting cannot be added without being classified as one that names things
or one that does not, with its reason: `TestEveryConfigFieldIsClassified`
reflects over the configuration and fails on anything unaccounted for. A
registry somebody has to remember to update would be the same silent control it
exists to prevent.

### Probing several operations at once

`--workers` (or the run-wide `workers:` key) sends that many transactions at
once, and probes that many operations at once in the `coverage` phase. The
examples phase ignores it, and says so, under `--sequence` or with hook files
loaded: both are ordering contracts. The `fuzz` phase does not take it at all.
That is a library limit rather than a decision: `rapid` takes its case count
and seed from **process-global flags**, so two probes at
once would overwrite each other's seed and neither would replay. Coverage draws
nothing at random — it sends the boundaries a schema implies, which are computed
— so nothing is shared between two operations being probed at the same time.

**The report is identical whatever the worker count**, which is the property
that makes turning it up safe: each operation accumulates its own results and
output and they are merged in description order afterwards, so concurrency
changes how long the phase takes and nothing else. There is a test that runs the
same suite at 1, 2, 4 and 8 workers and requires byte-identical output.

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

**Isolate the state, not only the side effects.** A pin stops a generated value
turning a probe into a real order. It does nothing about what the run *records*:
a probing phase writes as many entries as it sends, and if the system under test
journals its decisions — into a file, a table, an event log that a scorecard or
a dashboard later reads — those entries are indistinguishable from real ones.
Nothing is destroyed and no order is placed; the *measurements* are quietly
poisoned, and a poisoned record outlives the run that wrote it. So a disposable
endpoint means disposable state as well: its own journal, its own database, its
own halt file. If the safe target has to be assembled by hand each time, it will
eventually not be — build it as a service definition with the isolation pinned
into it, and assert the isolation, including the parts that are *absences*,
since an absence is what a well-meaning edit removes.

A run that will generate values for operations that change something, with
nothing pinned, says so — naming how many. It is one line and only when all
three conditions hold, because a warning on every run is the same as none.

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
is.

**Bodies are not redacted by guesswork** — there is no way to know which field of
a payload is a secret, and guessing would hide the payload a failure exists to
show. But the credentials vertrag is itself holding are not a guess: the fields
of `auth.login.body` named in `auth.login.secrets`, the `auth.header` value, the
OAuth2 client secret and the token the login returns are all known exactly, and
those **values** are replaced wherever they appear in a reported body.

Which login field holds the credential is not guessed either: the configuration
says. Every field of `auth.login.body` is listed once, in `secrets` (redacted in
every report) or in `public` (shown as written, so a report can still say which
account signed in). A field in neither, a field in both, or a listed name that
is not in the body stops the run before anything is sent, and the message names
the field. So a field added to the login body later can't go out unredacted
without somebody deciding it should. The same holds for each principal's login.

That closes the one exchange header redaction could never reach. In the login
itself the password goes out in the body and the token comes back in the body,
so a cassette of a login used to hold both. Nothing is matched by field name or
by looking like a token — a value is redacted if and only if vertrag was given
it or received it as a credential, which is why a `password` field vertrag never
supplied still appears in full.

There is no `vertrag replay` yet. Recording is the half that pays for itself
immediately; replaying needs a decision about how a recorded response is matched
to a request, which is the whole of what makes a replay library opinionated.

### Named examples are exchanges

An example name says which request produces which response. A request body named `rejected` is sent with the `400` named `rejected`, not with the `200`. A response or request without a name pairs with everything on the other side.

Parameters can name their values in the same way. When a Parameter Object's `examples` map uses a name that a response also carries, that name is a request of its own, sent with that name's values:

```yaml
parameters:
  - name: id
    in: path
    examples:
      found:   {value: 1}
      missing: {value: 2147483647}
responses:
  "200": {description: OK}
  "404":
    content:
      application/json:
        examples:
          missing: {value: {error: {code: not_found}}}
```

This is two tests: `found` against the `200`, and `missing`, with id 2147483647, against the `404` and nothing else. A name that a response carries makes the value the exchange's own, so a [link](#sequences) does not overwrite it. Without that, the id a create just returned would replace the missing one. Such a request also does not wait for the link's source. A name that no response carries only illustrates, and a link fills it as usual. An optional query or header parameter whose examples all belong to other names is left out of a named request: an `X-Tenant` that names another tenant belongs to the `403`.

Parameter examples that no response is named after change nothing: the first one stands in, as it always has.

A description whose named values were written against fixtures the server under test does not have can say so with `named-values: illustrate` (or `--named-values illustrate`). The names still pair each request with its response, but a link fills their values as it fills any other, so a sequenced run reads the row it just created rather than fixture id 1. The default, `own`, keeps the behaviour above.

### What an example requires

An Example Object can say what proving its exchange needs, with `x-requires`: `{fault: database}`, `{principal: app_password}`. A run declares what it can set up:

```yaml
provides:
  fault: [database]
unproven: .cache/unproven.json
```

An exchange is sent only when the run provides every one of its requirements, and one with no `x-requires` always is. The rest are **unproven here**: not sent, not counted in the surface figures, and written to `unproven` as JSON (name, example, requires, missing). Several runs that share a description, a live one and a fault-injecting one, say, can then show together that each exchange was proven by one of them. A requirement is read from whichever side of the pair carries it, a parameter's example included, since a bodyless response has no Example Object to put it on. A `provides` value no exchange requires is reported.

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

Every link is also *checked*, on every run, whether or not you ask for the
ordering. A Link Object is a claim about two operations at once — the value at
this place in my response is that operation's parameter — and an ordinary run
already holds the response it is a claim about, so resolving it costs no
request. A link whose expression finds nothing, one naming an operation the
description does not define, one supplying a parameter the target does not
declare, or one supplying a value the target's schema refuses, is reported by
name against the response it was resolved against.

This is where it earns its place: a project's `POST /roles` declared a link to
`renameRole` that could not resolve, and the reason was not the link. The create
answered 400 where its description promises success, because the schema said
`name: string` while the handler demanded alphanumeric — a defect in the
description, found by nothing but a link that could not be followed. That case
is reported as *not checked* rather than as a false link, because the link was
never the problem and the reader has to be sent to the create.

`checks.link-resolution: false` turns it off. The lifecycle questions — does a
create's own link find what it made, does a deleted resource stop answering —
are a separate thing, in `--phases stateful`, and each of their findings says
which assumption it rests on, because OpenAPI cannot state either one.

### Generated input

A description shows one request per operation, so a run establishes the happy
path and asks nothing else. The `fuzz` phase draws values from the schema instead:

```console
vertrag run --phases fuzz openapi.yml http://localhost:4000
vertrag run --phases fuzz --cases 100 --seed 42 openapi.yml   # replay a previous run
```

Two properties, failing in opposite directions. A value the schema permits
should be accepted, and a 4xx means the server and its description disagree
about what is valid. A value it forbids should be rejected with a 4xx, and a
2xx is a validation bypass. A 5xx is a finding either way, because refusing
input is a decision and failing on it is not.

Findings are shrunk to the smallest input that still fails, and the seed is
reported so any finding replays exactly.

It is a phase you ask for rather than part of every run, because the two
answer different questions. The examples phase is deterministic and belongs in
CI as a regression gate; generation is exploratory, and a run that discovers
something new on a Tuesday is a feature there and a broken build here. The
same goes for `--phases coverage`, which sends every boundary a schema implies
— the maximum, one past it, the required property missing — and is the same on
every run. Choose the phases with `--phases` or the `phases:` key.

Findings from a probing phase exit 2 rather than 1, so a pipeline can gate on
the documented contract and still read what probing found.

### How much of the description a run reached

A report lists what ran. It cannot list what did not, and what did not run is
where a suite stops testing without anybody deciding it should: a filter
written for one afternoon and never removed, a skip whose reason went stale, a
hook that returns early, a link that stopped resolving when a field was
renamed. Each leaves a run that is green and smaller, and every line in it is
still true.

So every run ends by saying how much of the description it reached, phase by
phase:

```
API surface reached

            operations      documented statuses
  examples  3 of 5  60.0%   4 of 9  44.4%
  coverage  2 of 5  40.0%   3 of 9  33.3%
  the run   3 of 5  60.0%   5 of 9  55.5%

  Operations no phase reached:
    DELETE /widgets/{id}  skipped by configuration: destroys shared state
    POST /admin/reindex   left out by exclude-tag admin

  Documented statuses of reached operations that no phase reached:
    GET /widgets/{id}  404, 409  answered 200
    POST /widgets      422       a hook skipped it

  Excluded, and not counted:
    GET /reports  needs a reporting backend (surface.exclude)

  The run reached 3 of 5 operation(s), 60.0%, below the 100.0% it requires.
```

An operation is reached when a request to it was answered, whatever the answer:
a 500 is a failure, reported as one, and still an operation exercised. A
documented status is reached when the operation answered with it, judged by the
rule that decides pass and fail, so `2XX` is reached by a 201. The run's line is
what any phase reached, because a documented 422 is usually provoked only by
the coverage phase, and a run that provoked it has reached it.

The yardstick is the whole description, not the part a run selected. `--tag
orders` narrows what is sent; the operations it leaves out are listed as *not
selected by tag* rather than disappearing from the total. Measured against its
own selection, every run would reach a hundred percent of whatever it chose to
test.

Every miss carries its reason: the filter that dropped it, the skip and the
reason written beside it, a hook that skipped it, a link whose value could not
be resolved, the run stopping at `--max-failures`, no answer at all — or, for a
status, what the operation answered instead. The reasons come from the examples
phase, which is the one that sends every selected transaction as the
description wrote it. A share is rounded down, so 1999 of 2000 operations reads
99.9% and never a 100% that a gate of 100 then fails.

**A gate.** `--require-surface 100`, or `surface: {require: 100}`, fails a run
that reaches less of the description's operations than that, and names what it
missed. It exits 3: not 1, because "a response broke the contract" and "the run
did not cover enough of it" are fixed in different places, and never 2, because
exit 2 says the documented contract held and probing found something to file,
and a run that left part of the contract untested has not shown that it held. A
filter that selects nothing does not slip past it either: `--tag ordres` used
to leave saying only that there was nothing to run.

**Reached, or passed.** Reached is any answer, a 500 included, which says the
operation was exercised and nothing about whether it held. `--surface-count
passed`, or `surface: {count: passed}`, counts an operation only when every
documented transaction of it that counts was sent in the examples phase and
passed — none failed, none skipped by configuration, a hook or a link that
could not resolve, none left without an answer. An answer of 500 or above never
counts as passed, even where a documented `5XX` matched it; exclude that status
if it is not meant to count. `reached` is the default and reads exactly as it
did. The table gains an `operations passed` column, and every reached operation
that did not pass is listed with what kept it from passing:

```
              operations       documented statuses   operations passed
  examples    4 of 4  100.0%   4 of 5  80.0%         3 of 4  75.0%

  Operations reached that did not pass:
    GET /half  404 skipped by configuration: the example always exists

  The run passed 3 of 4 operation(s), 75.0%, below the 100.0% it requires.
```

**Exit codes.** The first that applies wins:

| Code | Means |
| --- | --- |
| 0 | Every documented transaction passed, nothing was found, and the surface gate, if any, was met. |
| 1 | A documented transaction failed, or the run could not be carried out: a configuration refused, a description that does not compile, a server that could not be reached. |
| 3 | Nothing failed, and the run reached — or, counting passed, passed — less of the description than its surface gate requires. |
| 2 | The documented contract held and the gate was met, and a probing phase or a declared link found something to file. |

So a run with a failed transaction exits 1 whatever its surface, and a run short
of its surface exits 3 however much probing found.

`--dry-run` answers the same question without a server. What the filters drop
and what `skip` takes out are decided before anything is sent, so a dry run
prints the most a run could reach, and fails a gate it could never meet:

```
API surface a run would reach (dry run: nothing was sent)

              operations
  would send  4 of 5  80.0%

  Operations a run would send nothing to:
    DELETE /widgets/{id}  skipped by configuration: destroys shared state

  A run would send a request to 4 of 5 operation(s), 80.0%, so it cannot reach the 100.0% it requires.
```

**Exclusions.** Some operations are not worth reaching, and a gate of 100
should not have to be lowered to make room for them:

```yaml
surface:
  require: 100
  exclude:
    - operation: POST /admin/reindex   # as the report names it, or an operationId
      reason: an operator task, not part of the public contract
```

Or on the operation itself, where the reason travels with the description:

```yaml
paths:
  /billing/charge:
    post:
      x-vertrag-exclude: "charges a real card"
```

An exclusion can name a documented status instead of a whole operation, for a
response a run cannot provoke yet — a 403 until an authorization phase exists:

```yaml
surface:
  count: passed
  require: 100
  exclude:
    - status: 4XX                      # every operation's documented 4xx
      reason: error responses, unreachable until the authorization phase
    - operation: GET /admin/*          # a `*` is any run of characters
      status: "403"
      reason: needs a second account
```

`status` is a documented status exactly (`403`, `4XX`, `default`), or a
pattern in which `X` and `?` are one character and `*` any run of them, so
`4XX` takes out a documented 403 as well as a documented 4XX, and `403` only
403. Either key may be left out, not both. A status exclusion takes the status
out of both sides of every figure — out of the statuses documented and out of
those reached, and out of the transactions an operation must pass — and each
operation and status it took out is listed with its reason. An operation whose
every status is excluded has nothing left to count, and is left out of the
figures rather than called passed; when that leaves nothing at all, a `passed`
gate is not met, where a `reached` gate keeps reading nothing as all of it
reached. A status that is not a status (`600`, `forbidden`) is refused at load,
and one no operation documents is reported, by the run and by `vertrag doctor`.

An excluded operation is taken out of the figures — out of the total, with the
statuses it documents, rather than counted as reached — and listed apart with
its reason and where the reason was written. The configuration's entry wins
when both name one operation. The reason is required: an exclusion is a claim
that an operation does not need reaching, and the claim is worth exactly what
its reason says, so an entry without one is refused and an
`x-vertrag-exclude: true` is a warning about the description. An exclusion
naming no operation is reported, by the run and by `vertrag doctor`, because
one that excludes nothing reads exactly like one that works.

**An exclusion decides what counts, not what is sent.** An excluded operation
runs like any other; `skip` is what keeps a request back. They are kept apart
because they are different decisions, and a suite that only wanted to stop
being held to an operation should not stop testing it as well. When an excluded
operation is reached anyway the report says so, for whoever wrote "charges a
real card" believing it had stopped the charge.

The written reports carry the same figures: a section of the Markdown and HTML
documents, the `surface` object of `--reporter json`, and properties of the
JUnit suite — `vertrag.surface.operations` is `3/5`, `vertrag.surface.passed`
the operations passed, `vertrag.surface.count` what the gate counts — where a gate also becomes
a test case of its own, so a job that is red shows its reason in the CI
interface rather than a list of green cases.

Two things are outside the surface, and each is said elsewhere. Webhooks and
withheld GraphQL mutations are not sent at all, and are named in warnings of
their own. An operation whose required parameter has no example cannot be
compiled into a request, and the description error that says so stops the run
before anything is sent.

### Who may call what: the authorization phase

`--phases authorization` sends every selected operation's documented request
once as every configured principal and checks the answer against the
operation's `security`: a principal satisfying one requirement gets the
documented success (judged as any response is, body and all), one with no
credential gets 401, one whose credential lacks the roles or scopes gets 403.
Role names are matched literally — `*` is a role called `*`, not a wildcard.

```yaml
phases: [authorization]
principals:
  - name: anonymous                 # no credential
  - name: reader
    auth: {header: "Authorization: Bearer ${READER_TOKEN}"}   # read from the environment
    roles: [mail:read]
  - name: admin                     # any `auth` setup: header, cookie, login, oauth2
    auth: {oauth2: {token-url: https://id.example/token, client-id: ci, client-secret-env: ADMIN_SECRET}}
    roles: [mail:read, admin]
```

Each principal obtains its credential once, before any cell is sent; the
run's own `auth` and `--header` never travel with a principal's request.
Supported carriers are the `Authorization` header (bearer, basic), a named
header (an apiKey in the header) and one cookie. A credential that arrives
somewhere else than planned — an OAuth2 `token_type` other than Bearer, say —
leaves that principal's cells unsent with the reason.

The terminal ends with the matrix of operations × principals; the JSON report
carries it under `authorization`, Markdown and HTML as a table, JUnit as
`vertrag.authorization.*` suite properties. Every cell is also an ordinary
result named `authorization: <operation> as <principal>`. A failing cell has a
replay with every credential `<redacted>`; the matrix is redacted even under
`--no-sanitize`.

A cell is **unsent** when the run did not send it: a `skip` entry, an
operation without a documented success, a security the description leaves
undecided, an unsupported scheme or carrier, a hook that skipped the request or
changed its credential, or a dry run. An unsent cell is counted and shown and
never counts as a pass; it does not fail the run, so a matrix with unsent cells
is not proof of complete coverage. Any failing cell — an authorization bypass
(success where 401/403 is required), a different refusal, a 5xx, a broken
success body — exits 1, as a failed documented transaction does.
`--dry-run` prints the planned matrix with what each cell would require, and
sends nothing: no cell, no login, no token request.

### Statuses the description never mentions

The commonest real defect in the APIs vertrag is pointed at is not a broken
handler: it is a document that does not describe reality. An operation
documenting `200` and nothing else, whose handler answers 401, 404 and 409,
passes every check there is — and a client reading the description has no idea
those answers exist or what shape they arrive in.

A run already provokes them, so every run now keeps an account of what each
operation returned and prints, at the end, whatever the description never
mentions:

```
Statuses returned that the description does not document

  Reached by a request the description describes, or by input its own schema
  permits. The document is wrong about its own operation:
    GET /widgets/{id}  404  x5    coverage, fuzz

  Reached only by deliberately invalid input. Refusing it may well be correct,
  but the description never says what a refusal looks like:
    GET /widgets/{id}  400  x10   coverage, fuzz

  This is a report about the description and does not affect the exit code.
```

The two groups are the whole point. A probing run provokes hundreds of correct
refusals and a handful of statuses somebody should act on, and printed as one
list the first buries the second. A 5xx appears in neither: it is already a
finding in its own right, and one event with two severities gets acted on at
the lower one.

Nothing here changes the exit code. It is a defect in the document, and a
pipeline that fails over one teaches its owners to stop reading the output.

One gap worth knowing about: an OpenAPI `default` response is dropped when the
document is read, and the compiler treats it as a `200`, so an operation whose
only error entry is `default` will have its errors listed here.

### Two shapes for one status

A run remembers the outline of every response body it received, and says so
when one operation answered a single status, in a single media type, with
bodies no one client parser could read:

```
two shapes for one status:
  GET /items/{itemId} · 422 · application/json
    at detail: string (examples, fuzz), array (fuzz)
```

That is FastAPI's, and it is not exotic. A handler translating a domain
`ValueError` raises 422 with `detail` as a string; the framework's own request
validation raises 422 — same operation, same media type — with `detail` as an
array of error objects. Every response conforms to the description, which
promises one body per status and has nowhere to say which one a caller will
get. Only a contract tester meets both, and only within one run: the examples
phase provokes the handler's version and the probing phases provoke the
framework's. Naming the phase is what turns "two shapes" into a place to look.

It is a summary and never an exit code. What it reports is a fact about the
DESCRIPTION rather than a response that broke a promise, and a pipeline turning
red over it would block a merge on a document nobody edited.

The rule is deliberately narrow, because a false report costs the reader two
responses and an argument. Fields present in one body and absent from the other
are not a difference — that is what optional means — and neither is a `null`
against an absent field, an array of a different length, or `1` against `1.5`.
A different JSON type at a path both bodies have is. The media type is part of
the key for the same reason: content negotiation is one status answering with
two shapes on purpose, and a check that reports the intended behaviour of
ordinary APIs is one people learn to scroll past.

### Empty query examples

An explicitly empty query example is sent as `?address=`, including when the
parameter is required. Named examples, a single example, schema examples and
schema defaults preserve the value the description gives. This lets a contract
exercise an API's blank-value refusal alongside ordinary requests. A required
parameter without an example or default still produces a diagnostic. Required
path parameters retain their nonempty-value requirement.

### Webhooks are read, checked, and not sent

`webhooks` is OpenAPI 3.1's, and it points the other way: a path is a request
you send to the API, a webhook is a request the API sends to a receiver of
yours. vertrag is a client, and the description carries no address for that
receiver, so sending one would mean POSTing `accountDeleted` at the endpoint
under test — an API being sent requests it never said it would answer, with
whatever came back reported as a contract result.

So they are read rather than run. Every Path Item under `webhooks` is validated
exactly as one under `paths` is, and every operation it declares is named and
counted in a warning saying it was not sent — the same reasoning as [mutations
in a GraphQL schema](#graphql). A description whose entire surface is webhooks
used to produce an empty run that read as a pass; now it produces a run that
says what it did not cover.

`jsonSchemaDialect` is read too. A 3.1 document may say which JSON Schema
dialect its schemas are written in, and vertrag validates them under the
dialect they claim — draft-04 through 2020-12, plus the OAS base dialect the
specification defaults to. A dialect it cannot honour is reported rather than
passed through, because an unrecognised `$schema` is read as draft-04 and would
quietly under-enforce every constraint in the file.

### Explicit empty response examples

An explicit text or XML response example of `""` promises zero body bytes.
Unexpected bytes fail, just as a nonempty text or XML example fails when its
bytes differ. Single `example` values and named `examples` values follow the
same rule, including through hook round trips. A response with no body example
or schema leaves the body unspecified. JSON responses with schemas continue to
be validated against those schemas; example bytes do not replace the schema.
HEAD, 204 and 304 responses retain their bodyless HTTP semantics.

## GraphQL

A GraphQL schema is a description too, so vertrag tests one the same way:

```console
vertrag run ./schema.graphql http://localhost:4000
```

It builds one transaction per field of the query root, POSTs each as a query
document, and judges what comes back. The transactions are named `Query >
viewer` and `Mutation > createUser` — the same names `only`, `skip` and hook
files address for an OpenAPI run, and they do not move when an unrelated part
of the schema changes.

**Mutations are not sent unless you ask.** A mutation is by definition the
operation that changes something, and a schema offers `deleteAccount` on
exactly the same terms as `viewer`: same path, same method, same shape. Nothing
on the wire distinguishes them, so nothing but a setting can. A run that
withholds them says how many it withheld, names them, and says what to write to
include them — `graphql: {mutations: true}`, or `--graphql-mutations` for one
run. This is the same reasoning as [`fuzz.pin`](#pointing-a-probing-phase-at-an-api-that-can-do-something-irreversible).

**A GraphQL endpoint answers 200 to almost everything**, errors included, so a
tester that checked the status would pass against a server answering every
query with an error. What vertrag checks is the body: a non-empty `errors`
array is a failure and its messages, paths and error codes go into the report;
a response carrying neither `data` nor `errors` is not a GraphQL response at
all; and `data` has to answer the question that was put — every field the
selection asked for present, and non-null wherever the schema promised
non-null.

**The selection set is bounded.** `User.friends: [User!]!` is a cycle and every
real schema has one, so an unbounded walk writes a query that never ends. The
bound is `graphql: {max-depth: 4}`. A field the bound cuts is dropped from the
selection rather than left bare, because an object field with no sub-selection
is a syntax error that would take the whole request with it — and the run
reports how many were dropped, so a bound that is costing coverage is visible
rather than assumed. Interfaces and unions are selected with inline fragments,
and a field that came from one is not required in the response: it is there
only when the object turned out to be that type.

**Argument values are generated from the argument's own type.** A GraphQL
schema states no example request, so `user(id: ID!)` cannot be asked without
vertrag composing one. Each argument becomes a JSON Schema — `Int` with its
32-bit bounds, `ID` as a string or an integer, an enum as its members, an input
object as an object — and the value travels in the query's `variables`, never
written into the document. That is what makes `coverage` and `fuzz` mean
something against a schema: they vary one argument at a time and read the
verdict out of the reply's `errors`, because a GraphQL endpoint answers 200 to
its own refusals.

Two things are still withheld, and both are named and counted. An argument
typed as a **custom scalar** has no value space the schema describes, so
anything generated for one is a guess and its rejection would be a finding
about vertrag. And a **generated identifier names nothing**: vertrag can shape
an id but cannot possess one, so a GraphQL error from an operation carrying one
is not reported as a failure — the same exemption a path parameter's 404 gets.
The run says which operations are in that position before it prints a result.

`fuzz.pin` holds an argument by name, exactly as it holds a body field:
`pin: {dryRun: true}` fixes that argument in every generated query, in the
baseline request as well, and a pin naming an argument no field declares stops
the run before anything is sent.

## Configuration

vertrag reads `vertrag.yml`. Every setting it takes is in
[`vertrag.example.yml`](vertrag.example.yml), with the reasoning next to it.

```yaml
spec: ./openapi.json               # OpenAPI 2 or 3, or a GraphQL schema
endpoint: http://localhost:4000
hookfiles: ./hooks.js

server: npm run test:api           # started before the run, stopped after it
server-wait: 30                    # seconds it may take to answer

reporter: [cli, junit]             # a readable log and a machine-readable file
output: ["", report.xml]

transport:
  timeout: 30s                     # per request; the default
  retries: 2                       # network failures only, never a response
  delay: 200ms                     # pace the run for a server that throttles

checks:
  server-error: true
  content-type: true
  header-schema: false             # off by default; see below
  ignored-auth: false              # off by default; it doubles the requests
  link-resolution: true            # on by default; it costs no request
  max-response-time: 750ms         # unset by default; nothing is timed

surface:
  require: 100                     # percent of operations a run must reach
  exclude:                         # not counted, each with its reason
    - operation: POST /admin/reindex
      reason: an operator task, not part of the public contract
```

Coming from Dredd, every key a `dredd.yml` understands means the same thing
here, so migrating the file is `mv dredd.yml vertrag.yml` and nothing else.
vertrag used to read a `dredd.yml` where no vertrag file was present, which made
adoption a no-op; that fallback is gone. It meant a key's meaning depended on
the name of the file holding it — vertrag's own settings had to be refused from
one of the two names, or two testers reading one shared-looking file would have
silently tested different things. A directory holding only a `dredd.yml` is now
told to rename it rather than run with no configuration at all.

A file named with `--config` is read in full whatever it is called, including
`--config dredd.yml`: naming a file says what finding one cannot.

`server` starts the API under test before the suite runs and stops it after,
and `server-wait` — in seconds, as Dredd wrote it — bounds how long it may take
to come up. The endpoint is polled until it accepts a connection rather than
slept on, so the bound costs its full length only when something is wrong, and
a server that never comes up is reported by name with whatever it printed
instead of becoming a screenful of connection errors that name no cause. What
the server prints while it is up is kept rather than interleaved through the
report, and shown if the command dies partway through the run.

The command runs through `sh -c`, so pipes and `&&` work, and it is trusted the
way `hookfiles` is already trusted. It is started in a process group of its own
and the whole group is stopped — SIGTERM, then SIGKILL for anything left — on
every way out of a run: a pass, a failure, `--max-failures` stopping early, and
Ctrl-C. The group rather than the process, because `npm run test:api` is npm
spawning node: killing npm leaves node holding the port, and the next run's
"address already in use" gets blamed on anything but the test suite.

`header-schema` validates a response header's value against the JSON Schema the
description gave it — so an `X-Rate-Limit` documented as a non-negative integer
fails when the server answers `banana`.

In OpenAPI 3, only response headers marked `required: true` are demanded.
Optional response headers are schema-checked when present. The content map still
requires its media type; a Header Object named `Content-Type` is ignored as the
specification requires. Optional request header parameters with no example or
default are omitted from example requests, but remain available to generated
probes. An explicitly empty example or default is sent as an empty header.

Dredd compares values for exactly five headers — `content-type`, `accept`, and
the three `accept-*` — and checks only presence for every other one it declares.
It never reads a Header Object's schema at all. So no description has ever had
this enforced, and yours may well carry a header schema that was never true. It
is therefore the one check that starts off; turn it on here or with
`--check-header-schema` when you are ready to read what it finds.

`ignored-auth` re-sends every authenticated request without the credential and
reports each endpoint that answers it anyway. Nothing in an ordinary run can
tell you this: every request carries the credential, so every response is
correct, and an endpoint that would have answered without one looks exactly
like an endpoint that is guarded.

At one project it found 56 of 117 endpoints answering unauthenticated — both
rejection branches in their middleware had been commented out for months, in
the mock and in the live service alike, and every suite they had was green
throughout. They ran it because somebody happened to mention that it existed.

It stays off by default because it doubles the requests a run makes, which is a
real cost. So that the default is not also a secret, a run that has a credential
and is not making this check now says so in one line at the end, naming the flag
and what it costs — and says nothing at all when there is no credential, because
then there is nothing to withhold. `--check-ignored-auth` turns it on for a
single run.

`link-resolution` resolves every OpenAPI Link Object against the response it is
declared on, and reports a link that produced no value or a value the target
operation's own schema would refuse. A Link Object is a written claim — the
value at *this* place in my response is *that* operation's parameter — so
checking it is checking the description, exactly as validating a body against
its schema is.

It is on by default because it is the one check here that sends nothing: the
responses it reads have already arrived. Its findings exit 2 rather than 1,
since every documented transaction can pass while the description contradicts
itself. See [Sequences](#sequences) for what it catches.

`max-response-time` reports a response that took longer than the bound you give
it — `--max-response-time 750ms` for a single run. It is the one check the
description cannot ask for: OpenAPI has no way to write "this endpoint answers
within 750ms", so the number can only come from you, and there is no default,
because a bound vertrag invented would be green on the machine that mattered and
red on somebody's laptop.

Its finding is labelled like the others rather than reported as a contract error,
since nothing the document promised was contradicted — the status, the headers
and the body are all what it said they would be.

What it measures is the exchange alone: the request going out, the response
coming back, its body read. The waits a run takes on its own account are not in
it — `transport.delay`'s pacing, the backoff before a retried network failure,
the hooks — because a bound on a response is a statement about the server, and a
run that paced itself to spare a throttled one was being told it had found a slow
one. The two settings compose. How long the whole transaction took, waiting and
all, is the duration every reporter already prints.

### Reaching the server

`transport` is the network between vertrag and the API. None of it changes what
is sent or how a response is judged; it is what a CI job turns when the server
under test is slow, self-signed, behind a proxy, or shared with somebody else.
Every key has a `--flag` of the same name on `run`, whichever phases it runs,
for a single run. Only `timeout` has a default; leave the rest out and
vertrag sends the way it always has.

```yaml
transport:
  timeout: 30s                     # one request: connect, wait, read the body
  retries: 2                       # network failures only — never a response
  delay: 200ms                     # pace the run for a server that throttles
  insecure: false                  # certificate verification; see below
  ca-cert: ./ca.pem                # trusted IN ADDITION to the system roots
  cert: ./client.pem               # presented when the server wants mutual TLS
  cert-key: ./client.key           # only when the key is not in `cert` already
  proxy: http://proxy.internal:3128
```

`timeout` bounds one request — connecting, waiting, and reading the body —
rather than the run, and defaults to 30s. A hung endpoint otherwise hangs
everything queued behind it, and a suite that never finishes tells you less than
one that reports the timeout.

`retries` retries **network failures only**: connection refused, reset, a
timeout. A response is never retried, whatever its status. That distinction is
deliberate and worth stating plainly, because `retries: 3` reads as though it
would paper over a flaky 500 and it will not — a 5xx is the finding the run
exists to report, and retrying until the server happened to answer 200 would
hide exactly the thing somebody ran the suite to find. What it fixes is a link
that drops connections, so the report is a verdict about the API rather than
about the network. The wait is 250ms before the second attempt, doubling
thereafter, and none of it is charged to the server. There are none unless you
ask: a run that retried on its own would be one whose report depended on how
many times it happened to have tried.

`delay` paces the run, for a server that throttles or an environment shared with
somebody else. It bounds the request *stream* rather than each worker, so
`--delay 200ms --workers 8` still sends one request every 200ms rather than
eight; the first request never waits. Since `checks.max-response-time` times the
exchange alone, the pause costs nothing against it.

`ca-cert` adds a PEM bundle to the system roots, for a private authority the
machine has never heard of. Verification stays on — it simply learns who you
trust. A file that cannot be read, or that holds no certificates, stops the run
before the first request rather than turning every transaction into a connection
error that reads like an API being down.

`cert` and `cert-key` are the certificate vertrag *presents*, for an API that
requires mutual TLS. Without one such a server refuses the handshake, every
transaction is a network failure, and the run says nothing at all about the
contract. `cert-key` is only needed when the key is not in the certificate file
already, since openssl and the tools around it hand the pair over in one file
often enough that splitting it to satisfy vertrag would be a chore. A key given
with no certificate is refused before any request, because a handshake would
otherwise go out anonymous while whoever passed it believed otherwise.

`proxy` overrides the `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` environment, which is
otherwise honoured as usual — normally what a CI runner has already arranged.

`insecure` switches certificate verification off. It exists for the ordinary case
of a staging server with a self-signed certificate, where the alternative is not
testing it at all. What it costs is worth being explicit about: vertrag will then
talk to whatever answers on that address and cannot tell your API from something
sitting in front of it, so the credential in `auth` and every request body go to
whoever picks up. Wherever there is a private authority to point at, `ca-cert` is
the better answer; `insecure` is for when there is not.

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
    secrets: [password]            # redacted in every report
    public: [username]             # shown as written; every field is one or the other
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

A credential that must not be written into the config comes from the environment:
`${NAME}` in `auth.header`, or in a principal's, is replaced with that variable when the
config loads, and one that is not set stops the run before anything is sent. Only the
braced form is read, so a `$` elsewhere in a header is sent as written.

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

vertrag began as a port, to keep projects that depend on Dredd working once its
upstream was archived. The reasons for a Go port were operational:

- **One binary.** Dredd needs a Node runtime and a dependency tree; `brew
  install antimatter-studios/tap/vertrag` needs neither.
- **Testing a Go service shouldn't need a JavaScript toolchain.** A Go project
  whose only reason to carry a `package.json` is its API tests can drop it.
- **Speed.** No interpreter start-up per run.

It no longer stops at Dredd's behaviour: the probing phases, link checks,
GraphQL, `doctor` and recordings are all things Dredd never did.

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
`CI` workflow manually, or `chore oracle` locally.

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
$ chore oracle
ok  github.com/antimatter-studios/vertrag/internal/oracle
```

Compared, field for field, as of the last full run:

| Suite | Compared | Covers |
| --- | --- | --- |
| Compile | 59 fixtures | API Blueprint, OpenAPI 2 and OpenAPI 3 — transaction naming, URI expansion, bodies, diagnostics |
| Parse | 40 documents | OpenAPI 3 and OpenAPI 2 end to end: source document to transactions |
| Validate | 27 cases | Pass/fail verdicts and their error text, against Gavel — both JSON Schema dialects |

Agreement includes the generated request and response bodies, the JSON Schemas
attached to responses, and the parser diagnostics down to their exact wording,
source positions and occurrence counts.

Not every difference is a failure. Nine of the twelve OpenAPI 3 documents
differ from Dredd on purpose, in 24 places, each one listed with its reason in
[`internal/oracle/parse_test.go`](internal/oracle/parse_test.go) and logged by
`chore oracle`. The suite fails if a listed difference stops differing, so the
list cannot go stale.

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
| `uritemplate` | URI template expansion | Done, oracle-verified except the percent-encoding Dredd gets wrong |
| `apidesc` | Recognising a document's format and choosing its parser | Done |
| `apidesc/openapi3` | OpenAPI 3 → API Elements | Done, oracle-verified; 3.0, 3.1 and 3.2 |
| `apidesc/openapi2` | Swagger 2.0 → API Elements | Done, oracle-verified |
| `apidesc/graphql` | GraphQL schema → the schema model | Done |
| `yamldoc` | YAML and JSON with the source positions diagnostics need | Done |
| `validate` | Response validation (Gavel) | Done, oracle-verified |
| `runner` | Sending requests, judging responses | Done |
| `auth` | Obtaining the credential a run sends | Done |
| `hooks` | Running Node.js and Python hook files | Done |
| `config` | Reading `vertrag.yml` | Done |
| `server` | Starting the API under test, and stopping it | Done |
| `generate` | Values drawn from a JSON Schema | Done |
| `fuzz` | The `fuzz` phase: generated requests, shrunk findings | Done |
| `link` | Ordering a run by its links, and checking them | Done |
| `reporter` | cli, dot, markdown, html, JUnit, JSON, HAR and VCR output | Done |
| `shape` | Body outlines, and where one status answered with two | Done |

GraphQL is the one format that does not pass through API Elements, and that is
a decision rather than an omission. A schema has no resources, no URIs and no
methods; expressing it as API Elements would mean inventing an href per field,
and those hrefs would then decide the transaction names — the names hook files
and `--only` address. So the schema goes straight to `compile`, which produces
the same transactions the API Elements path produces, and everything downstream
of that point — filters, hooks, auth, transport, the runner, every reporter —
is shared and unchanged.

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
5. ~~Hooks and configuration~~ — done; Dredd's hook API, in Node.js and Python
6. ~~OpenAPI 2 parser~~ — done, oracle-verified
7. ~~Reporters~~ — done; cli, dot, markdown, html, JUnit XML, and HAR and
   VCR recordings of the traffic
8. ~~Adversarial input generation, with shrinking~~ — done; the `fuzz` phase
9. ~~Stateful sequences from OpenAPI links~~ — done; `vertrag run --sequence`
   orders a run by them, and `--phases stateful` checks whole lifecycles
10. Generated input across a sequence, rather than one operation at a time
11. ~~GraphQL schemas → transactions, sent and validated~~ — done; queries by
    default, mutations on request
12. ~~Generated ARGUMENT values for GraphQL fields~~ — done; `coverage` and
    `fuzz` probe a schema one argument at a time, and `fuzz.pin` holds one
13. Stateful sequences over a GraphQL schema, which has no links to follow —
    what corresponds to one is a field returning something you can ask more
    about

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
already written in someone's hook file.

Two names are not on this list, and changing them is the whole migration. A
`dredd.yml` is no longer discovered, so rename it to `vertrag.yml`. A Node hook
file's `require('hooks')` becomes `require('vertrag_hooks')`; see
[Hooks](#hooks) for why.

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

Dredd's bare `hooks` is not answered, so a hook file arriving from Dredd
changes that one line. A name that generic, injected into Node's resolver,
would shadow any real package called `hooks`.

Python needs `python3` on PATH and uses nothing but the standard library — no
virtualenv, no pip install. Node hook files may be TypeScript if the project
has `tsx` or `ts-node`; without one, vertrag says so rather than letting Node
fail on syntax it cannot parse.

`beforeAll`, `beforeEach`, `before(name)`, `beforeEachValidation`,
`beforeValidation(name)`, `after(name)`, `afterEach` and `afterAll` all work, as
do `transaction.skip`, `transaction.fail`, and rewriting the request or the
expectation.

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

The tasks live in [`chores.yml`](chores.yml) and run with
[chore](https://github.com/antimatter-studios/chore)
(`brew install antimatter-studios/tap/chore`):

```console
chore             # list every task
chore build       # build the binary
chore test        # unit tests, no Node required
chore cover       # unit tests plus the coverage floor CI enforces
chore oracle      # differential test against the real Dredd
```

The Makefile is deprecated. It forwards each target to the chore task of the
same name and will be removed.

The oracle needs Node to run the reference. `chore oracle-deps` installs it; the
suite skips rather than fails when it is missing, so a fresh checkout does not
report a wall of failures that are really one missing install step.

## Licence

MIT. Dredd is MIT, originally by Apiary Czech Republic, s.r.o.; the test corpus
in `oracle/corpus/` is copied from it under that licence — see
[`oracle/corpus/NOTICE.md`](oracle/corpus/NOTICE.md).
