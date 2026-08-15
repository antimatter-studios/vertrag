# Corpus provenance

The fixtures in `apib/`, `openapi2/` and `openapi3/` are copied from Dredd's
`dredd-transactions` test suite:

- Upstream: <https://github.com/antimatter-studios/dredd>, itself a fork of
  <https://github.com/apiaryio/dredd>
- Original author: Apiary Czech Republic, s.r.o.
- Licence: MIT (the same licence this project is released under)

Each fixture is a pair:

- `<name>.apib` / `<name>.yml` — the API description document
- `<name>.json` — that document parsed into API Elements

They are copied rather than read out of a sibling checkout so the oracle runs
identically on a developer's machine and in CI, where no such checkout exists.

The `.json` files are what the compile-stage oracle consumes today, and they are
also the target output for the format parsers when those are written: a parser
is finished when it reproduces the `.json` its source document is paired with.
That is why the source documents are kept even though nothing reads them yet.
