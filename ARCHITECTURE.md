# Architecture

vertrag is one Go module. The packages below the root are importable by anyone;
the ones under `internal/` are not, and the split is deliberate rather than
alphabetical.

## Importable

| Package | Responsibility |
| --- | --- |
| `refract` | The API Elements object model — the shape every format is read into |
| `apidesc` | Format detection, and the parsers (`apidesc/openapi3`, `apidesc/openapi2`) |
| `compile` | API Elements → the HTTP transactions to test |
| `validate` | Whether a response matches what was promised |
| `uritemplate` | URI template expansion |
| `yamldoc` | YAML/JSON navigation that keeps source positions |

These are stages, not layers: each has one input type and one output type, and
each is tested against a reference implementation on its own. That is what lets
a failure name its own cause — a parse difference cannot be mistaken for a
compile difference, because they are compared separately.

## Internal

| Package | Why it is not importable |
| --- | --- |
| `internal/config` | Reads `dredd.yml`; shaped by the CLI's needs, not a library's |
| `internal/runner` | Executes transactions; its API is still moving |
| `internal/hooks` | Runs hook files; coupled to the runner's transaction type |
| `internal/reporter` | Renders results for a terminal |
| `internal/oracle` | Differential tests only |

These stay unexported because their interfaces are not settled. Exporting one is
a promise, and a promise made early is a promise broken later.

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
