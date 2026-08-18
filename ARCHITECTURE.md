# Architecture

vertrag is one Go module. The packages below the root are importable by anyone;
the ones under `internal/` are not, and the split is deliberate rather than
alphabetical.

## Importable

| Package | Responsibility |
| --- | --- |
| `refract` | The API Elements object model — the shape every format is read into |
| `apidesc` | Format detection, and the parsers (`apidesc/openapi3`, `apidesc/openapi2`, `apidesc/graphql`) |
| `compile` | API Elements, or a GraphQL schema → the HTTP transactions to test |
| `validate` | Whether a response matches what was promised |
| `uritemplate` | URI template expansion |
| `yamldoc` | YAML/JSON navigation that keeps source positions |
| `config` | Reads `vertrag.yml`, whose keys are a superset of Dredd's |
| `runner` | Sends the transactions and judges what comes back |
| `hooks` | Runs Node.js hook files and lets them rewrite transactions |
| `reporter` | Renders results — a terminal log or dots, a Markdown or HTML document, JUnit XML for CI |
| `link` | Orders a run by the sequences its description declares |
| `generate` | Values drawn from a schema, valid and invalid |
| `fuzz` | Sends generated values and judges what the server did with them |
| `corpus` | vertrag's own descriptions, and a server that answers what they promise |
| `cmd/vertrag` | The command itself: flags, wiring, exit codes |

The command lives under `cmd/` rather than at the root. Go allows a `main`
package anywhere, and putting it beside the libraries meant `main.go`, `run.go`
and their tests sat in the same directory listing as every package — so the
first thing anyone saw of the project was its wiring rather than its parts.

## Two paths into `compile`, one type out of it

Everything that describes resources and methods — OpenAPI 2, OpenAPI 3, API
Blueprint — reaches `compile` as API Elements, which is what makes the rules for
naming a transaction and expanding a URI worth writing once.

A GraphQL schema does not, and forcing it to would cost the thing the shared
pipe exists to protect. API Elements is resources and methods; a schema has one
endpoint, one method, and operations that are fields of a type. Expressing it
there means inventing an href per field, and `compile` derives transaction names
from hrefs — so the names hook files and `--only` address would come out of a
URI nothing can be requested at. So `apidesc` hands a schema over as a schema
and `compile.CompileGraphQL` turns it into the same `[]Transaction` the other
path produces.

The boundary that matters is therefore the OUTPUT type, not the input one:
filters, hooks, auth, the runner and every reporter work on `Transaction` and
are shared unchanged. What is not shared is one intermediate representation
that would have carried no GraphQL information.

The first ten are stages, not layers: each has one input type and one output type, and
each is tested against a reference implementation on its own. That is what lets
a failure name its own cause — a parse difference cannot be mistaken for a
compile difference, because they are compared separately.

## Internal

| Package | Why it is not importable |
| --- | --- |
| `internal/oracle` | Differential tests; it has no callers and never should |

`internal/` is for code that genuinely must not be depended on, which here means
the test harness alone. An earlier version of this file kept the runner, hooks,
config and reporter unexported on the grounds that their interfaces were still
moving — but a pre-1.0 module promises nothing about any of its packages, and
hiding them only stopped anyone using them. Running Node.js hook files over
Dredd's worker protocol, or writing JUnit XML from arbitrary results, are useful
outside this binary.

## Why one module rather than several

Splitting the importable packages into separate modules would let them be
versioned independently. It would also mean a version bump and a dependency
update for every change that crosses a boundary, `replace` directives while
developing, and a `go test ./...` that no longer spans the repository.

None of that buys anything today, and deferring costs nothing later: a package
at `github.com/antimatter-studios/vertrag/validate` can become its own module at
exactly that import path, so nothing a consumer wrote would change. The decision
gets easier once the boundaries stop moving, not harder.

The exception would be publishing one of these under a different name, at its
own repository — that changes the import path, so it is a decision to take
before anyone depends on it rather than after.

## No `pkg/` directory

`pkg/` is a widely used convention, not a Go standard — the layout repository
that popularised it says so itself, and the ecosystem is split down the middle
on it. The mechanism Go actually enforces is `internal/`; everything else is
importable wherever it sits. A `pkg/` directory would add a level of nesting to
every import path without changing what any of them mean.
