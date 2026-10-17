# vertrag report

**0 passing, 0 failing, 0 errors, 0 skipped (0 total, 0s)**

## API surface

| Phase | Operations | Documented statuses |
| --- | --- | --- |
| examples | 3 of 5  60.0% | 4 of 9  44.4% |
| coverage | 2 of 5  40.0% | 3 of 9  33.3% |
| the run | 3 of 5  60.0% | 5 of 9  55.5% |

### Operations not reached

| Operation | Why |
| --- | --- |
| DELETE /widgets/{id} | skipped by configuration: destroys shared state |
| POST /admin/reindex | left out by exclude-tag admin |

### Documented statuses not reached

| Operation | Statuses | Why |
| --- | --- | --- |
| GET /widgets/{id} | 404, 409 | answered 200 |
| POST /widgets | 422 | a hook skipped it |

### Excluded, and not counted

| Operation | Reason | Written in |
| --- | --- | --- |
| POST /billing/charge | charges a real card; reached anyway: an exclusion decides what counts, not what is sent, and `skip` is what keeps a request back | x-vertrag-exclude |
| GET /reports | needs a reporting backend | surface.exclude |

**The run reached 3 of 5 operation(s), 60.0%, below the 100.0% it requires.**
# vertrag report

**0 passing, 0 failing, 0 errors, 0 skipped (0 total, 0s)**

## API surface

A dry run: nothing was sent, so this is what a run would send a request to.

| Phase | Operations |
| --- | --- |
| would send | 4 of 5  80.0% |

### Operations not reached

| Operation | Why |
| --- | --- |
| DELETE /widgets/{id} | skipped by configuration: destroys shared state |

**A run would send a request to 4 of 5 operation(s), 80.0%, so it cannot reach the 100.0% it requires.**
