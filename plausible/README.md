# Plausible wrapper

The External Tool Wrapper for [Plausible Analytics](https://plausible.io): a
data source that answers `probe`, `read_series` and `read_total`, and declares
every other verb of the contract absent. The protocol is
[`../README.md`](../README.md).

## The spec it was written against

Fed in, never recalled (doc 35 §8). Fetched 2026-09-24:

| Source | What was taken from it |
|---|---|
| https://plausible.io/docs/stats-api | `POST /api/v2/query`; `Authorization: Bearer <key>`, a Stats API key scoped to one team; the body fields `site_id`, `metrics`, `date_range` (relative strings, or a pair of dates or ISO 8601 timestamps with an offset), `dimensions`, `filters` (`[operator, dimension, clauses]`, operator `is` for equality), `include.time_labels`; the metrics `visitors`, `visits`, `pageviews`, `events`, and the rates and averages; the time dimensions `time:hour`, `time:day`, `time:week`, `time:month`; the event and visit dimensions; that dates and timestamps are reported in the site's reporting timezone; the default rate limit of 600 requests an hour; the response shape `results[].metrics`, `results[].dimensions`, `meta.time_labels` |
| https://plausible.io/docs/metrics-definitions | What `visitors` means: a person counts once per day, "If a person visits from multiple devices or on multiple days, they are counted as separate visitors"; a visit ends after 30 minutes without an action |
| https://plausible.io/data-policy | How a visitor is identified: a hash of a daily salt, the domain, the IP address and the user agent; the salt rotated and deleted every 24 hours; no cookies |
| https://plausible.io/terms | The customer retains full ownership and control of the site's data; stats are deleted with the account, or eventually when a subscription lapses; nothing restricts keeping or showing statistics read through the API |
| https://plausible.io/docs/sites-api | Not used. It needs a separate Sites API key, and nothing in cut 1 needs to list or create sites |

The spec hash the wrapper reports is the SHA-256 of this file, so editing the
record above changes the wrapper's provenance.

## What it assumes

- **The site's domain is the account id**, as the Stats API's `site_id` takes
  it. The key is a Stats API key of the team that owns the site.
- **Hosted or self-hosted.** The connection's endpoint, when set, replaces
  `https://plausible.io`. A self-hosted instance is assumed to answer the same
  v2 query endpoint; one too old to have it fails the probe with the API's own
  answer.
- **Counts and uniques only.** `pageviews`, `visits` and `events` are read with
  `count`; `visitors` with `unique`. Any other event name is read as a goal
  configured on the site, filtered with `event:goal`: `count` is its events,
  `unique` its visitors. Rates and averages (`bounce_rate`, `visit_duration`,
  `conversion_rate` and the rest) and revenue are declared unavailable.
- **"Unique" is Plausible's.** Unique per person per day, so unique visitors
  over a week counts a person once for each day they came. The manifest says
  so, citing the definition.
- **Windows are instants.** A `read_total` window is sent as two UTC
  timestamps, the end one second before the window's (the API's range is
  inclusive, the contract's half-open); a window with no start is `"all"`. A
  `read_series` bucket is Plausible's, in the site's reporting timezone, which
  the API does not name: the manifest declares `read_series` partial for that
  reason.
- **Equality filters only,** on the dimensions the manifest lists; anything
  else is refused. A granularity other than hour, day, week or month is
  refused rather than substituted.

## Errors

A `400` is Plausible refusing the query (an unknown goal, a dimension the
metric cannot be split by) and is returned as `refused` with Plausible's own
sentence. `401`/`403` (the key), `404` (the site), `429` (the rate limit) and
anything else are `failed`.

## Building

```bash
docker build -f plausible/Dockerfile -t mendel-tool-plausible:dev .
```

From the repository root. The build compiles natively and cross-compiles to
`linux/amd64`; nothing is compiled under emulation. The tag is a label: Mendel
runs the image only once a Mendel admin has registered it pinned by digest (see
`../README.md`, "Registering, reviewing, retiring"). To run in a project's
cluster it is pushed to Mendel's public `mendel-integrations` repository and registered
by the digest the registry gives it; a Mendel admin's review against a venue
site promotes it to every project:

```bash
docker tag mendel-tool-plausible:dev us-central1-docker.pkg.dev/<mendel project>/mendel-integrations/plausible:0.1.0
docker push us-central1-docker.pkg.dev/<mendel project>/mendel-integrations/plausible:0.1.0
mendel-tool tools register -file plausible/wrapper.json \
  -image us-central1-docker.pkg.dev/<mendel project>/mendel-integrations/plausible@sha256:<digest the push printed>
PLAUSIBLE_API_KEY=... mendel-tool tools verify plausible 0.1.0 -account <venue site>
```

The wrapper's `command` in `wrapper.json` is its Dockerfile's `ENTRYPOINT`,
which the wrapper's test checks: a Job runs Mendel's shim in its place, and the
shim runs this.

## Tests

`go test ./plausible/` runs every verb against recorded
responses from a local HTTP server and never calls Plausible. The probe's
manifest is checked with Mendel's own `CapabilityManifest.Check`, so the
wrapper and the protocol cannot drift apart.
