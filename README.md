# External Tool Wrappers

A wrapper is a container that implements Mendel's contract for one External
Tool (dev/claude_plans/35_tools_outside_the_codebase.md §6, §8). It holds no
project's choices: everything that varies arrives in the request, and
everything the tool can do is declared in the answer to `probe`.

One directory per tool, named for the tool's slug. The first is
[`plausible/`](plausible/), a read-only data source. **Everything about a tool
lives with its wrapper, here or wherever its author keeps it, and never in
Mendel's repository or binaries**: nothing is embedded or seeded, so a tool
Mendel knows is one someone registered, and Mendel's own tests fail if a
wrapped tool is named in its server code.

A wrapper reaches an installation one way: a Mendel admin registers its
`wrapper.json` with its image pinned by digest, from the admin area's External
Tools tab or `mendel-tool tools register`. No Mendel deploy is involved. This
repository's workflow publishes the images (see "Publishing").

## What each verb is for

The contract states each verb's purpose (`Verb.Purpose` in `wrapperprotocol`),
and the suite holds a manifest to it where a check can reach. **Declining is a
designed outcome, not a gap to fill.** A verb claimed by stretching it to
something the tool can do -- an edit called `append_update`, a recent page
called `list_owned`, a rate called a sum -- keeps the protocol and breaks the
meaning, which is the one failure the suite finds hardest to catch (SPIKE.md
findings 14 and 16). When the tool cannot do what a verb is for, decline it,
with the reason.

| Verb | What it is for | How the suite checks it |
|---|---|---|
| `probe` | Say what the wrapper can do against this account | The manifest's form, and nothing claimed beyond `wrapper.json` |
| `authorize` | Connect through the tool's own authorization: begin, complete, refresh, revoke | begin's URL carries the state; refresh where declared; revoke with `-revoke` |
| `draft` | An asset that is not live until publish makes it so | Not live after draft; live after publish by its ref |
| `publish` | Make an asset live, from a draft or a payload | A valid asset is published; the same idempotency key is the same asset; the shape's boundaries are refused |
| `status` | What is configured and in effect: not live, live, gone | Live after publish, gone after retract |
| `append_update` | Append an update to a **log-shaped** asset, keeping every earlier one; never an edit | Refused for a wrapper none of whose kinds is log-shaped |
| `retract` | Take an asset back, safely twice, never destroying history | Twice, the same outcome |
| `read_back` | The asset as the tool holds it, in the kind's family fields | Equal to what was approved, field for field; refused once gone |
| `read_metrics` | An asset's metrics, a value or why there is none | Every metric the manifest lists is answered as declared |
| `set_cap` | The most an asset may spend (a total, a daily amount or both, and always an end date), answered as the tool now holds it: `hard`, or a `target` with how far it may be exceeded | Refused for a wrapper none of whose kinds involves spend |
| `list_owned` | The assets whose **name** starts with the prefix given | The published asset is listed for its prefix and not for another |
| `read_series` | One point per step, empty steps included | Steps, order, window; a granularity not declared is refused |
| `read_total` | One number, as the metric's kind allows | Against the venue's truth where it knows it |
| `search` | Items matching a query, honest about the window | Within the limit; every windowed result dated inside it |

## The contract module

`contract/` is a Go module of its own,
`github.com/mendelbuild/mendel-integrations/contract`, standard library only:
`wrapperprotocol` (the wire types, `wrapper.json`, the manifest and its
checks) and the conformance harness. Mendel's server imports it to run and
check wrappers, and a wrapper written in Go imports it rather than restating
the types. Nothing in it names a tool, and it never imports anything else in
this repository.

The wrappers here build against the copy beside them (a `replace` in the root
`go.mod`), so a contract change and the wrappers it affects land in one
commit. Mendel moves to a new contract commit on purpose, by `go get`.

## Conformance

`contract/cmd/conformance` runs a wrapper through the contract against a venue account
of your own and says, check by check, what it found: pass, fail, warn, or
untested (a verb the suite cannot exercise yet, which is never a pass). It
needs no Mendel and no database. The suite is `contract/conformance/`, and its own tests
are mutants of a small wrapper that each break one rule and must each be caught
by the check about that rule (doc 35 §8).

```bash
go run ./contract/cmd/conformance -file <tool>/wrapper.json \
    (-image <image> | -cmd <built binary>) -account <venue account> [-endpoint URL] [-at RFC3339] [-json out.json] \
    [-read-only] [-fake-venue]
```

Two flags say what the venue is. `-read-only` is for someone's real account:
the suite publishes, sends, caps, refreshes and revokes nothing and runs no
boundary case (a wrapper that wrongly accepted one would post). It reads,
and for each kind makes one draft, which it shows is not live through
`status` and takes back with `retract` (or leaves undrafted where either is
declined). One warning names every honoured verb it left alone, and the
report records `read_only`, so a read-only pass is never read as a full one.
`-fake-venue` is for a stand-in for the tool on the loopback at `-endpoint`:
everything is judged as usual, and `authorize` may answer an http URL on
127.0.0.1, localhost or [::1].

Credentials come from the shell's environment under the names `wrapper.json`
gives them. `venues/` holds local venues for tools that can run on this
machine: `venues/plausible-ce/up.sh` brings up a Plausible Community Edition
with known traffic, so the Plausible wrapper is checked with nobody's
Plausible account.

What the suite and the wrappers have taught so far about the contract itself
is in [`SPIKE.md`](SPIKE.md).

The Go types for everything below are in `wrapperprotocol` (at the root of Mendel's module),
which is the source of truth. A wrapper may restate them rather than import
them, so its image need not carry Mendel's server; its tests should then run
its answers through `CapabilityManifest.Check` and `ParseWrapperResponse`, as
`plausible/` does.

## The protocol

One run is one container start. Mendel writes **one JSON request to stdin**
and reads **one JSON response from stdout**. Nothing else crosses: no
environment variables, no arguments, no network listener. The credential is in
the request body, so it never appears on a command line.

```json
{
  "contract": "2",
  "connection": {
    "credentials": {"PLAUSIBLE_API_KEY": "..."},
    "account_id": "pong.example.com",
    "endpoint": ""
  },
  "calls": [
    {"verb": "probe"},
    {"verb": "read_total",
     "measure": {"event": "pageviews", "aggregation": "count"},
     "window": {"start": "2026-09-23T00:00:00Z", "end": "2026-09-24T00:00:00Z"},
     "filter": {"dimension": "visit:utm_content", "value": "hn"}}
  ]
}
```

- `contract` is the protocol version. A wrapper written against another one
  answers its first call `failed` and stops.
- `connection` is the project's account, injected for this run only. A
  wrapper never stores it. **When `endpoint` is given, every request the
  wrapper makes to the tool goes to it**, keeping its path, whether or not
  `wrapper.json` declares an endpoint field: a self-hosted instance and a
  stand-in for the tool on the loopback are reached the same way.
- `calls` is a **list**, answered in order, so a day's reads for every measure
  a project has cost one container start (§8 "Invocation cost").

```json
{
  "results": [
    {"verb": "probe", "manifest": {...}},
    {"verb": "read_total", "total": {"value": 412}}
  ]
}
```

One result per call, in order, **stopping at the first that does not
succeed**. Each result has exactly one of: the verb's payload (`manifest`,
`series`, `total`), `refused`, or `failed`.

- `refused` is a designed outcome, with a sentence: a granularity the tool
  lacks, a metric it withholds, a filter on a dimension it does not have.
  Mendel records it and does not retry until something changes.
- `failed` is something that went wrong: the key was rejected, the API
  answered 500. Mendel records it and the next scheduled run tries again.

Mendel checks the response on its own side: a wrapper that answers more
results than calls, goes on after a failure, or stops early without a failing
result is refused.

A process that cannot read its request at all exits non-zero; that is the only
case where stdout may be empty.

## What a data source reads

The contract has fourteen verbs (the table above). A data source honours three:

| Verb | Arguments | Answer |
|---|---|---|
| `probe` | none | The Capability Manifest |
| `read_series` | `measure`, `window`, `granularity`, `filter?` | `series`: `[{"at", "value"}]`, one point per bucket |
| `read_total` | `measure`, `window`, `filter?` | `total`: `{"value", "quality"?}` |

- `measure` is `{"event", "aggregation"}`, aggregation one of `count`,
  `unique`, `sum`, `value`, as the metric's declared kind allows (count:
  count or unique; people: unique; sum: sum; rate and average: value).
- `window` is `{"start", "end"}`, instants, half-open. A zero `start` means
  the total to date.
- `granularity` is `hour`, `day`, `week` or `month`. A wrapper **refuses** a
  granularity the tool cannot serve; it never substitutes another.
- `filter` is one **equality** filter, `{"dimension", "value"}`, on a
  dimension the manifest lists. No other filter exists in the contract.

Every other verb is answered `refused` by a data source, and declared
`declined` in its manifest.

## The Capability Manifest

`probe` answers what the wrapper can do **against this account**, having
checked that the credential works. Mendel refuses a manifest that does not
answer every question (`CapabilityManifest.Check`):

- `contract`, and `wrapper`: `version`, `spec_source` (the URL of the spec it
  was written against) and `spec_hash`.
- `verbs`: **all fifteen**, each `supported`, `partial` with a `caveat`, or
  `declined` with a `reason`. A verb left out is not declined, it is
  unaccounted for. A data source declines every write verb (`draft`,
  `publish`, `append_update`, `retract`, `set_cap`).
- `metrics`: each `available` with the `aggregations` it can be read with, or
  `unavailable` with a `reason` (never zero for something withheld). A metric
  readable as `unique` must say what unique means for it (`uniqueness`), in
  the tool's own words: "unique" means three different things across six
  analytics tools.
- `granularities` and `filter_dimensions`.
- `venue`: what the tool's conformance venue could prove -- `test_account`,
  `reversible_writes`, `read_only` or `nothing_safe` (§8).
- `idempotency`, and `entitlements` observed on this account.
- `storage_policy`: what the tool's terms say about keeping and showing what
  is read, with the source cited. The data-licensing obligation is judged
  against it.

Mendel records a successful probe's provenance in `external_tool_wrappers`,
the manifest flattened into `external_tool_capabilities` as demonstrated rows,
and the connection's entitlements and verification time. A read is refused
when the last probe is older than thirty days
(`domain.DataSourceVerificationHorizon`).

## `wrapper.json`: how Mendel learns a wrapper exists

Every wrapper describes itself, whether it ships here or is registered at
runtime. `./registry.go` embeds this directory's `*/wrapper.json`
as the seed, and `internal/external/toolregistry` writes them into
`external_tools` and `external_tool_wrappers`: the server adds any shipped
definition it has not seen on every worker turn, and `mendel-tool tools
refresh` rewrites unpinned ones. A registration writes the same tables from a
`wrapper.json` given as text.

```json
{
  "tool": {"slug": "acme-analytics", "name": "Acme Analytics",
           "homepage": "https://acme.example", "categories": ["know what visitors do"]},
  "version": "0.1.0",
  "contract": "1",
  "image": "mendel-tool-acme-analytics:dev",
  "command": ["/acme-analytics"],
  "spec_source": "https://acme.example/docs/api",
  "connection": {
    "account": {"label": "Site", "help": "As Acme names it."},
    "credentials": [{"name": "ACME_ANALYTICS_KEY", "label": "API key"}],
    "endpoint": {"label": "Endpoint (self-hosted only)"}
  },
  "claims": {"probe": "supported", "read_series": "partial", "read_total": "supported"}
}
```

- **`connection`** is what a project supplies, and the connect form is built
  from it. Each credential is held encrypted in `project_env_vars` under its
  `name` and arrives in a run's `connection.credentials` under the same name.
  Name credentials for the tool, so they do not collide with an
  application's own secrets. `connection.config` declares the settings the
  wrapper reads from a run's `connection.config` (who sees a post): a
  lower-case `name`, a `label`, the `values` it accepts when only some, and
  the `default` it applies when unset. Mendel sends only a declared setting
  with a value the wrapper takes.
- **`prices`**, where the tool charges per call: for each claimed verb, the
  list price of one call that succeeds (`amount`, `currency` as an ISO 4217
  code), the `plan` it is for and the `source` that publishes it, and
  optionally a `plan_setting`, a declared setting in which a project states
  its own price on its plan. Mendel writes what a call cost from this; a
  price it cannot read makes the spend unknown, never zero. A metric that is
  what an asset cost at the tool is marked `"spend": true` in the manifest: a
  sum in a currency, and at most one, so spend is never mistaken for revenue.
- **`version`** and **`contract`** must be what the wrapper's `probe`
  answers: verification refuses an image that answers as another version or
  contract. **One digest per version, ever:** a changed image, or a changed
  definition, is a new version.
- **`claims`** are shortlisting evidence only (doc 35 §9). They decide where a
  tool is offered -- a tool claiming `read_total` is offered as a data source
  -- and never what Mendel relies on, which is what the probe demonstrates
  against the project's account. The wrapper's own tests check its probe does
  not decline anything this file claims.
- **`image`** is the tag the wrapper's Dockerfile builds. It is a label and
  never runs: what runs is the digest given at registration.
- **`command`** is required: what the image runs, exactly its Dockerfile's
  `ENTRYPOINT` in exec form. A wrapper runs as a Job in a project's cluster,
  where Mendel's shim takes the entrypoint's place and runs this (see "Where a
  wrapper runs"), so the image cannot say it for itself. The wrapper's own
  test should hold the two to agree, as `plausible/` does.

## Registering, reviewing, retiring

A registered image runs with a project's decrypted credentials, in that
project's own cluster. Two Functional Area Conditions stand between a
registration and any project's credentials, before a project connects and
before every read:

1. **`external-tool.wrapper-pinned`: pinned by digest, and not retired.** A
   tag is refused, because it can be moved to other bytes after the first
   ones were checked; only the pinned image ever runs. To run in a project's
   cluster the pin must be a **registry digest**, `name@sha256:<64 hex>`,
   **on a public registry**: a project's cluster can pull from its own
   registry and from a public one, and nothing else. This repository's
   workflow publishes each changed wrapper to staging's package on push to
   `main`, and a second copies it by digest into production's (see
   "Publishing"); each prints the digest to register. A local
   image's id (`sha256:<64 hex>`, from `docker image inspect --format
   '{{.Id}}'`) is still accepted, for a Mendel admin's own verification, and
   the condition says why a project cannot run it. Retiring stops a version
   at once, reads included, and is undone by reinstating.
2. **`external-tool.channel-runs-wrappers`: the project's deployment channel
   can run a wrapper.** A Kubernetes channel, on a Mendel that knows its public
   address (`MENDEL_BASE_URL`) and names its shim image by digest
   (`MENDEL_WRAPPER_SHIM_IMAGE`). A Fly.io or Cloud Run channel is declined by
   name.

**A review promotes; it does not gate.** Connecting is the project's consent
to the wrapper running in its cluster, with the same trust as the application
code already running there with its secrets. A Mendel admin's review --
verifying the pinned image against a venue account, a Mendel admin's own
account with the tool and never a project's -- promotes a version to every
project. Until then, connecting says plainly that the version has not been
reviewed (`external-tool.wrapper-reviewed`, a warning). Verification checks the
manifest, that it answers as the registered version and contract, and that it
declines nothing `wrapper.json` claims; a failure is recorded and clears any
earlier review. This is the probe cut 2's conformance suite will build on, not
the suite itself.

Which version runs, of those pinned and not retired: one a cluster can pull
before a local id, a reviewed one before an unreviewed one, then the newest.

```bash
# Merged to main at commit 8cf9e5f0a1b2, the workflow publishes
# ghcr.io/mendelbuild/wrappers-staging:acme-analytics-1.0-8cf9e5f0a1b2 and
# prints its digest. On staging:
mendel-tool tools register -file wrapper.json \
  -image ghcr.io/mendelbuild/wrappers-staging@sha256:...
ACME_ANALYTICS_KEY=... mendel-tool tools verify acme-analytics 1.0 -account venue.example
mendel-tool tools retire acme-analytics 1.0 -reason "sent a key to the wrong host"
```

Registering, retiring and reinstating are also on the admin area's External
Tools tab: each is undone from the same page, recorded with who and when, and
carries no secret (doc 36 §4). **Verifying is the shell's alone.** It runs the
image with a credential, which the shell holds and a page must not, and which
cannot be recalled once sent. Its credentials are read from the shell's
environment under the names `wrapper.json` gives them, and are never stored.

## Where a wrapper runs

**For a project, as a Kubernetes Job in the project's own cluster** (doc 35
§19), through the machinery Mendel's datastore adapters use: Mendel reaches the
cluster through the project's deployment channel, mints an invocation with a
one-use token, and applies a Secret holding the instruction and a Job naming
it. Nothing can pipe into a container in someone else's cluster, so the Job's
pod has an init container, from Mendel's small static shim image
(`cmd/mendel-wrapper-shim`), that copies the shim into a volume the wrapper's
container shares. The wrapper's container runs the shim in place of its
entrypoint; the shim runs the wrapper's `command` with the request on stdin,
checks what it printed, and POSTs one report to Mendel with the token. The
wrapper never sees the token, and needs nothing but the protocol above.

Connecting and reading are **asynchronous**: each starts a Job and returns.
The data-sources page shows a connection being probed until its report
arrives; a reading is written when its read reports, and the reconcile starts
no second read of a source while one is out. A Job that never reports is
closed as failed when its token expires, naming its logs in the project's
cluster.

Every run is logged in `external_tool_runs`. A project's runs are **the
project's own bill**: metered from invocation to report as hosting, paid by the
project, under `tool_wrapper_run`.

**For a verification, on the Mendel admin's own machine**, with
`docker run -i --rm <image>` (`wrapperprotocol.DockerRunner`), against a venue
account. Logged, and never priced.

## Writing one

A tool nobody has wrapped yet can be named for wrapping, with what it is for,
from the Mendel admin area's External Tools tab or with `mendel-tool tools
request`. The request records the need and the verbs that follow from it, which
is what the wrapper is written to; it closes as wrapped when a wrapper for the
tool is registered.

1. Fetch the tool's current API documentation and cite it, with the date, in
   the wrapper's README. Do not write a wrapper from memory of an API.
2. Answer `probe` with the cheapest call that proves the credential can read
   the account, then the manifest.
3. Decline what the tool cannot do, at the granularity it cannot do it.
4. Test every verb against recorded responses. A test never calls the real
   API.
5. A Dockerfile that compiles natively and cross-compiles for `linux/amd64`
   onto a minimal base with CA certificates, with its `ENTRYPOINT` in exec
   form.
6. A `wrapper.json`, and a test that it agrees with what the wrapper answers
   and that its `command` is the Dockerfile's `ENTRYPOINT`, as `plausible/`
   has.
7. Name the wrapper's directory for its slug, and merge to `main`. See
   "Publishing" for what happens next.

## Publishing

Every wrapper's image goes to one of two packages on ghcr.io, one per Mendel
environment, and its tag says which wrapper it is:

```
ghcr.io/mendelbuild/wrappers-staging:<slug>-<version>-<commit>
ghcr.io/mendelbuild/wrappers-prod:<slug>-<version>-<commit>
```

On every push to `main`, `.github/workflows/images.yml` builds each wrapper
whose directory changed (all of them when the contract module, `go.mod` or
the workflow changed), publishes it to `wrappers-staging`, and prints the
`mendel-tool tools register` line with the digest in the run's summary.

A wrapper Mendel wrote itself does not come through `main`. Mendel pushes it
to a branch of its own, `generated/<env>/<slug>-<version>-<id>`, and the same
workflow builds that one wrapper into the environment's candidates package,

```
ghcr.io/mendelbuild/wrapper-candidates-staging:<slug>-<version>-<commit>
ghcr.io/mendelbuild/wrapper-candidates-prod:<slug>-<version>-<commit>
```

where the Mendel that pushed it reads the digest and registers it as an
unreviewed candidate, which runs only for a project whose person agreed to
it. Nobody here reviews it first: the branch is the record, not a review. A
generated branch may change only its wrapper's directory, and the workflow
fails one that changes anything else, since a branch push runs the workflow
from the branch's own copy. Merging one to `main` is what promotes it to
every project, through the path below.

**What keeps unreviewed code away from the packages.** A wrapper's own code
runs in the workflow twice -- its tests, and its Dockerfile's `RUN` steps --
and neither job can write a package: both have a token that only reads this
public repository, no checkout leaves that token on disk
(`persist-credentials: false`), and the image is built from `git archive`,
without `.git`, into a tarball. A separate job, the only one with
`packages: write`, checks out nothing and pushes that tarball. And a
generated branch cannot change the workflow itself only because the token
Mendel pushes with has `contents: write` and **not** the Workflows
permission, which GitHub requires for any change under `.github/workflows`:
that token must stay contents-only. The workflow also fails a generated
branch that changes anything outside its wrapper's directory, renames
included.

Once staging has run it, `.github/workflows/promote.yml`, run by hand from
Actions with the tag and digest `images.yml` printed, copies that digest into
`wrappers-prod` under the same tag. The copy keeps the digest, and the workflow
checks that it did, so production registers exactly the bytes staging tested;
nothing is rebuilt. It refuses a staging tag that names another digest and a
production tag that already names one, and promoting twice is a no-op.

**Why two packages rather than one per wrapper.** The set of packages under
`ghcr.io/mendelbuild` is fixed: one per kind of image a project's cluster
pulls, per environment. There are six: these two, the two candidates
packages, and Mendel's `wrapper-shim-staging` and `wrapper-shim-prod`, pushed
by its `deploy/gke-deploy.sh`. A new kind of image adds a package; a new wrapper
never does. Every one has to be public, because a private package can be
registered but cannot be pulled by any project's cluster. A package first
pushed by a workflow in this public repository takes the repository's
visibility, so these were public from their first push; the shim's,
first pushed with a person's token, were created private and made public once,
by hand, since GitHub has no API for it. Both workflows still check that what
they wrote can be pulled with no credentials, and name the settings page to
fix it if not.

Nothing checks for collisions, because none can happen. A directory must be
named for the slug its `wrapper.json` declares (the workflow fails otherwise),
so two wrappers cannot share a slug; the commit is the last part of a tag and
always twelve hex digits; and a version may not contain a `-` (the workflow
fails on one), so read from the right a tag splits into slug, version and
commit exactly one way. A tag already published is never pushed again, so
re-running the workflow reuses it rather than moving it to a rebuild, which
would not be byte-identical; a bad build is replaced by a new commit. A tag is
only a label for finding a digest. What Mendel runs is the digest, and each
installation's registry refuses a second digest for a version it already
holds, so a wrapper changed without a new `version` publishes fine and is
refused at registration, saying why.

Three rules keep the packages trustworthy:

- **This repository's workflows are the only writers** of the wrapper
  packages: `images.yml` of `wrappers-staging`, `promote.yml` of
  `wrappers-prod`. Never push to either by hand: a tag on ghcr.io can be
  pushed over by anyone with write access, so this rule is what keeps it
  naming one image. (The other writer under `ghcr.io/mendelbuild` is Mendel's
  own `deploy/gke-deploy.sh`, which publishes the shim to its two packages,
  tagged by Mendel's commit.)
- **Nothing published is ever deleted.** An image that looks unused may be
  what an installation runs, and every production digest is also a staging
  one, so a staging image that looks superseded can be what production pins.
- **Production runs only what staging published.** It registers from
  `wrappers-prod`, and nothing reaches `wrappers-prod` but a copy of a staging
  digest.
