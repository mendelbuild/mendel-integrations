# Plausible Analytics wrapper

An External Tool Wrapper (see `../GUIDE.md`) for [Plausible
Analytics](https://plausible.io), speaking Mendel's wrapper protocol,
contract `"1"`. It is a read-only data source: it answers `probe`,
`read_series` and `read_total` against Plausible's Stats API v2
(`POST /api/v2/query`), and declines every other verb. It is written to run
against a self-hosted Plausible Community Edition, reached through the
connection's `endpoint`; against plausible.io itself it works the same way
with `endpoint` left blank.

## Sources

Written against Plausible's own documentation, fetched **2026-09-29** from
`github.com/plausible/docs` at commit `ed9a0a1a48ed` (published at
`plausible.io/docs`), copied into `../spec/plausible/`:

- `stats-api.md` -- the Stats API v2 reference: authentication, the
  `/api/v2/query` request and response shapes, `metrics`, `dimensions`,
  `filters`, `date_range`, and the "Quirks" section on imported-data limits
  and metric-value drift.
- `metrics-definitions.md` -- what each metric means, used for `Unique
  Visitors`' uniqueness (`visitors`' `uniqueness` in the manifest) and to
  understand `bounce_rate`, `visit_duration`, `views_per_visit`.

No other page was fetched, so nothing here rests on memory of the API: where
the wrapper needed something the two documents did not cover (a written data
policy for API reads, machine-readable rate-limit or plan fields), the
manifest says so plainly rather than invent it.

## What it does

`probe` (`main.go:probe`) proves the credential can read the named site with
the cheapest call the Stats API offers: one metric (`visitors`), one day,
no dimensions or filters. On success it answers the Capability Manifest;
on any error (rejected key, unknown site, a 5xx) it answers `failed`.

`read_total` and `read_series` (`main.go:readTotal`, `readSeries`) map the
contract's `measure.event` onto one Stats API metric (`main.go:metricCatalog`)
and its `filter` onto one Stats API `is` filter
(`main.go:buildFilters`/`filterDimensions`), then call `/api/v2/query` and
read back either the one row's value or, for `read_series`, one row per time
bucket.

### Metric mapping

| contract event | aggregation | Plausible metric |
|---|---|---|
| `visitors` | `unique` | `visitors` |
| `visits` | `count` | `visits` |
| `pageviews` | `count` | `pageviews` |
| `events` | `count` | `events` |
| `bounce_rate` | `sum` | `bounce_rate` |
| `visit_duration` | `sum` | `visit_duration` |
| `views_per_visit` | `sum` | `views_per_visit` |

`count` and `unique` are used where Plausible's own metric already means
exactly that. The contract's `sum` bucket is used for the three metrics that
arrive from Plausible already aggregated into a single percentage, second
count, or ratio, since the contract offers no fourth "the tool's own
pre-aggregated figure" bucket and `sum` is the least misleading of the three
it does offer; each carries a `quality` flag saying what the number actually
is (`percentage`, `seconds`).

`scroll_depth`, `percentage`, `conversion_rate`, `group_conversion_rate`,
`average_revenue`, `total_revenue` and `time_on_page` are declared
`unavailable`: each needs a filter, a dimension, or revenue goals configured
on the site (`stats-api.md`'s metrics table, "Requirements" column) that this
wrapper does not add on its own, so reading them generically would be
inventing an implicit filter Mendel never asked for.

### `read_series` is partial

Plausible's time-bucket labels (`time:hour`/`day`/`week`/`month`) are
reported in the site's own Reporting Timezone, not UTC (`stats-api.md`,
"Time dimensions": "these dates and timestamps are reported in sites
Reporting Timezone"). The Stats API does not say what that zone is anywhere
this wrapper calls, so it cannot convert the label to the correct instant; it
reads the label as if it were UTC instead and says so as `read_series`'s
manifest caveat. `read_total` does not carry this problem: its window is
always sent as explicit ISO8601 instants, never a bucket label.

### Date ranges

The contract's `window` is a half-open `[start, end)` instant pair. A zero
`start` means "the total to date", which becomes Plausible's `date_range:
"all"`. Otherwise it becomes a custom two-element `date_range` of ISO8601
instants; `end` is moved back one second first, since the API's custom range
is documented only by inclusive examples in `stats-api.md`'s `date_range`
table.

## What is not tested

Nothing here calls the real API. `main_test.go` runs every verb against a
`net/http/httptest` server standing in for `/api/v2/query`, recorded from
the request/response shapes `stats-api.md` documents, and checks
`wrapper.json` against `wrapperprotocol.Description.Check` and the probe's
manifest against `wrapperprotocol.CapabilityManifest.CheckAs` and against
`wrapper.json`'s own claims.
