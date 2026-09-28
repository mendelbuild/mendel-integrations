# External Tool Wrappers

A wrapper is a container that implements Mendel's contract for one External
Tool (dev/claude_plans/35_tools_outside_the_codebase.md §6, §8). It holds no
project's choices: everything that varies arrives in the request, and
everything the tool can do is declared in the answer to `probe`.

One directory per tool. The first is [`plausible/`](plausible/), a read-only
data source. **Everything about a tool lives with its wrapper**, and Mendel's
server never names one: `internal/external.TestNoWrappedToolIsNamedInTheServer`
fails if a shipped tool's name, slug, credential names or image appear under
`internal/` or `cmd/`.

A wrapper reaches an installation one of two ways, and **only one needs a
Mendel deploy**:

- **Shipped:** a directory here. Its `wrapper.json` is embedded in Mendel's
  binary and seeded with a deploy, and its image is still built and pushed on
  its own. A shipped definition arrives unpinned, and runs only once a Mendel
  admin registers it with a digest, like any other.
- **Registered:** a wrapper built and pushed anywhere, entered from the Mendel
  admin area's External Tools tab or `mendel-tool tools register` with its
  `wrapper.json` and its image pinned by digest. No Mendel deploy. This is
  how a tool enters the promotion pipeline (doc 35 §9).

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
  "contract": "1",
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
  wrapper never stores it.
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

## Verbs in contract 1

The contract has fifteen verbs (§6). A data source honours three:

| Verb | Arguments | Answer |
|---|---|---|
| `probe` | none | The Capability Manifest |
| `read_series` | `measure`, `window`, `granularity`, `filter?` | `series`: `[{"at", "value"}]`, one point per bucket |
| `read_total` | `measure`, `window`, `filter?` | `total`: `{"value", "quality"?}` |

- `measure` is `{"event", "aggregation"}`, aggregation one of `count`,
  `unique`, `sum`.
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
  application's own secrets.
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
   **in Mendel's public `mendel-integrations` repository**: a project's cluster can
   pull from its own registry and from that one, and nothing else. A local
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
docker push us-central1-docker.pkg.dev/<mendel project>/mendel-integrations/acme-analytics:1.0
mendel-tool tools register -file wrapper.json \
  -image us-central1-docker.pkg.dev/<mendel project>/mendel-integrations/acme-analytics@sha256:...
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
is what the wrapper is written to; it closes as wrapped when the registry seeds
a wrapper for the tool.

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
7. Push the image to Mendel's public `mendel-integrations` repository and register it
   by the digest the push printed, so a project's cluster can pull it.
