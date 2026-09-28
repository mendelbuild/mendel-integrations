# The wrapper-vetting spike

Doc 35 §19 (decided 2026-09-28): before cut 2 generates wrappers, vet the
wrapper model by hand, outside Mendel. A conformance harness that runs any
wrapper through the contract with no server and no database; two or three
wrappers written by hand and chosen for shape (Plausible, read-only with an
API key; Mastodon, a publisher behind OAuth; Tavily, search); then an agent
given a docs URL and the harness, to count repair rounds.

This file is the running record of what that turns up. Each finding says what
was seen, what it means for the contract, and what, if anything, was done.

## Where it stands

| Step | State |
|---|---|
| Conformance harness (`conformance/`, `cmd/conformance`) | Built; mutation-tested against ten mutants |
| Local venue for Plausible (`venues/plausible-ce/`) | Built; 36 of 36 checks pass against the real thing |
| Mastodon wrapper (publisher, OAuth) | Next; blocked on the contract, finding 1 |
| Tavily wrapper (search) | After Mastodon; blocked on the contract, finding 1 |
| Generation test | Last |

## Findings

**1. The contract cannot carry a publisher or a search today.** Of the
fifteen verbs, the wire types have arguments and answers for three:
`VerbCall` carries only `read_series`/`read_total` arguments (measure, window,
granularity, filter), and `VerbResult` only a manifest, a series or a total.
`draft`, `publish`, `status`, `retract`, `read_back`, `read_metrics`,
`list_owned` and `search` exist as names, and a wrapper can do nothing with
them but decline. Doc 35 §6 says what each takes and returns; none of it is in
`wrapperprotocol`. So Mastodon and Tavily start with a contract change, not a
wrapper. Seen before any publisher was written, which is what the spike is
for.

**2. A data source's metrics can be an open family, and the manifest can only
list names.** Plausible reads any goal configured on a site by its name. The
manifest has no way to say "any goal name, read with count or unique", so the
wrapper put that rule in the `events` metric's `quality` flags, a field for
flags every read carries. The suite caught it on its first run against a real
Plausible (`events/count carries quality []; the manifest says every read
carries [any other event name is read as a goal…]`). Mendel's side has the
same hole from the other direction: `ToolMeasure.CheckAgainst` lets any metric
the manifest does not list through, so "not listed" means both "any goal
works" and "never heard of it". *Done:* the wrapper's manifest no longer
misuses the flag (0.1.1). *For the contract:* a manifest that can declare a
family of metrics (a pattern or a kind, its aggregations, and a sentence), and
a Mendel that refuses an unlisted name outside a declared family. Several
analytics tools have custom events, so this passes doc 35's two-tool rule.

**3. A self-hosted Plausible is a complete venue.** Community Edition 3.2.1
serves the same `POST /api/v2/query` the wrapper uses (the research had found
conflicting reports; checked directly). It runs in under 900 MB, seeds through
its own release modules, and takes events through its public endpoint, so the
Plausible wrapper is checked against the real product with nobody's account,
and against numbers known in advance: 37 pageviews, 11 visitors, 11 visits.

**4. An accepted event is not a counted one.** Plausible answers `202` to
every event, and drops those for a site its ingestion cache does not hold yet:
the first venue read zero, a later one 31 of 37. The venue now waits on the
cache and reads its traffic back before saying it is ready. The general lesson
for any write-then-read conformance, which a publisher's `publish` →
`read_back` is: an acknowledgement is not evidence, and a venue whose truth is
not read back is not a fixture.

**5. The suite can check consistency but not truth, unless the venue knows the
truth.** It confirms a count read as a daily series sums to the same count
read as a total, which holds on Plausible for a UTC site. It cannot confirm
that 37 is right: only a venue that sent the 37 knows that. A venue should
hand the suite its expected values, so a wrapper that reads the wrong metric
consistently still fails. Not built yet.
