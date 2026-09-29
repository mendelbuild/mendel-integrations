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
| Contract 2-draft (`wrapperprotocol/draft.go` in Mendel) | Built: `authorize` with steps, a secret `credentials` field, the action surface's and search's fields |
| Harness for the draft | Built: authorize, a publish-to-retract lifecycle, boundaries, a credential-leak check; 19 mutants across two fakes |
| Mastodon wrapper (publisher, OAuth) | 27 of 27 checks pass live against @mdl_test on mastodon.social (0.1.1); two warnings: `complete` needs a person, and `revoke` is run only when asked |
| Tavily wrapper (search) | 24 of 24 checks pass live (two basic searches, 2 credits) |
| Venue-supplied expected values (finding 5) | Built: `-expect`; the Plausible venue writes its truth, and a consistently wrong wrapper now fails |
| Generation test (`spike/generate/`) | Done: both tools generated blind, each passing after one repair round, $4.36 in all; see "The generation test" |

## The generation test

The question the spike exists to answer: can an agent write a wrapper from a
spec, with the conformance suite as its test? Run with
`spike/generate/generate.py`, on 2026-09-28:

- **The agent** was Mendel's own code-generation executor
  (`internal/codegen/executor`, its own system prompt and tools) on
  `claude-sonnet-5`, driven by `spike/generate/driver/main.go.txt`, in a
  container holding the workspace and nothing else. The Anthropic key reached
  the driver on stdin and was never in the agent's environment (checked: its
  shell counted zero Anthropic variables and could not see the host).
- **Blind:** the workspace held the contract (vendored), the authoring guide
  with every line naming a hand-written wrapper removed, and the tool's
  documentation fetched that day (`spike/generate/spec/`). No web, no key, no
  hand-written wrapper.
- **The loop was Mendel's:** write and unit-test; the suite runs against the
  real venue with the key given to the suite alone; what did not pass goes
  back as a new run on the same workspace; at most three repairs.

| Tool | Round 0 | Repair 1 | Rounds | Spend | Time | Final |
|---|---|---|---|---|---|---|
| Plausible (contract 1, local CE venue with known truth) | 37 pass, 3 fail | 40 pass | 1 repair | $1.58 | 10.6 min | 40/40 |
| Tavily (contract 2-draft, live, 4 credits in all) | 20 pass, 3 fail, 1 warn | 23 pass, 1 warn | 1 repair | $2.78 | 13.3 min | 23/23 + 1 warn |

What round 0 got wrong is exactly what the suite exists to catch:
Plausible's series left out empty steps (the API omits empty buckets; the
contract is one point per step), and Tavily's windowed search let undated
results through, and did not refuse an empty query or an unknown filter before
spending anything. One round of the suite's sentences fixed each.

The generated code is kept, with its prompts and reports, in
`spike/generate/testdata/generated/`. Neither wrapper special-cases the venue
or the suite. Both cite their spec with dates; both have real unit tests (15
and 19); both are larger than the hand-written ones (about 1,100 lines each,
against roughly 560 and 350). Tavily's is better than the hand-written one in
one respect: its storage policy notices that Tavily's terms license the
search *query* itself to Tavily.

**And the generated Plausible wrapper is wrong in a way the suite passed.**
See finding 14.

**Answer, for now:** yes, with the suite as the loop's judge, an agent writes
a working wrapper from a fed-in spec in one repair round for a few dollars --
for a data source with an OpenAPI-less prose spec as well as a search tool
with an OpenAPI document. What stands between that and trusting one unread is
finding 14: the suite judges the protocol, and a wrapper can keep the
protocol and still report a wrong kind of number. A publisher was not
generated (the only venue is a real account, and each run posts), which is
the next thing to try once the suite can judge meaning as well as form.

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
consistently still fails. *Done:* a venue hands the suite an `expected.json`
(`-expect`); the Plausible venue writes 37/37/11/11, and a mutant reading every
count double -- consistent with itself, so it passed everything else -- fails.

**6. Authorization is one verb with steps, owned by the wrapper, and `begin`
produces secrets too.** Decided with Ben (§19, 2026-09-28): the alternative,
Mendel running every tool's OAuth from a config block, would put each tool's
variant in Mendel's server. Mastodon shows why: there is no fixed client, so
`begin` registers one on the person's own instance, and that client's secret
has to be kept until `complete` (and after, to revoke). So `credentials` can
come back from `begin` as well as `complete`, and Mendel merges them. `revoke`
is a step too: disconnecting should end the grant at the tool, not just forget
the token.

**7. Some tools honour idempotency natively, and publish needs to carry
Mendel's key.** §6 found native idempotency on almost nothing and planned for
Mendel to build it from a naming prefix and `list_owned`. Mastodon takes an
`Idempotency-Key` on posting and keeps it an hour, and has no name to prefix
and no search of an account's own posts, so `list_owned` is declined and the
key is the mechanism. The draft's publish carries `idempotency_key`; the
harness checks a repeat is the same asset when the manifest declares native
idempotency.

**8. `publish(ref, when)` assumes a draft, and many tools have none.** §6's
publish takes a ref from `draft`. A Mastodon post is live to its audience the
moment it exists, so `draft` is declined and publish has to take the payload
itself. The draft allows either. (Mastodon does have one not-live state, a
status scheduled at least five minutes ahead, which a later version could use
as `draft`: it would make the whole lifecycle rehearsable with nothing ever
visible.)

**9. The connection's config has no declared shape.** A post's visibility is
a setting of the project's (§6: "a setting is config"), and the wrapper
defaults it to followers-only, but nothing tells Mendel the setting exists or
what it may be. `wrapper.json` declares the connection's fields; it should
declare the config's too, the same way.

**10. `describe_shape` and `limits` repeat what the manifest already says.**
The manifest carries each kind's shape (refined per instance: 475 characters
beside a link on mastodon.social) and the rate limits in its entitlements, so
both verbs are declined with that reason. Worth removing from the contract
rather than having every wrapper decline them.

**11. Credentials mix secrets with identifiers.** The first live Mastodon run
passed every check that posts -- publish, a repeat under the same key answered
as the same post, read-back field for field, counts, retract twice, and the
gone states -- and failed the leak check: the client id was in the authorize
URL. That is OAuth working as designed (the person's browser carries the
client id to the instance), and yet Mastodon's own documentation says to treat
the client id as a password, and the draft held every credential to one rule.
*Done:* a credential in `wrapper.json` can be marked `public`, an identifier
the protocol shows; secret stays the default, Mendel holds both, and the leak
check looks only for the secret ones. The harness caught this on real data in
its first live run of a publisher, which is the case for having it.

**12. A read can cost money, and the manifest has nowhere to say so.** Every
Tavily search spends credits (1 at basic depth, 2 at advanced), which past the
free tier are dollars, and Mendel already keeps a `tool_spend` cost kind for
exactly this. The manifest can say it only in prose, in entitlements. It
should say it as data -- a price per call per verb, in the tool's own unit,
and what that unit costs -- so a Hop's search is metered and bounded before it
runs, the same rule as a generation run. The probe reads the key's usage from
an endpoint that costs nothing, which is the pattern to ask of every paid tool.

**13. A search is honest only against a window it can prove.** Tavily's
dates are its estimate of when a page was published or last updated, and a
windowed search has to drop what it cannot date. So `search` is partial, and
the harness checks every result of a windowed search is dated inside it; an
undated result fails rather than passing on trust.

**14. A generated wrapper reported rates as sums, and the suite passed it.**
The generated Plausible wrapper declares `bounce_rate`, `views_per_visit` and
`visit_duration` available with aggregation `sum`, and reads them as 0, 3.36
and 1 -- a percentage, a ratio and an average, "summed". Every check passed:
the numbers are consistent with themselves, and the venue's truth covered only
the four counts. Mendel would have recorded "bounce rate, summed: 0" as a Key
Result reading. The hand-written wrapper declines the three, because the
contract reads counts, uniques and sums, and a person reading the manifest
would have caught it; the suite did not. The suite checks form, and needs to
check meaning where it can:
- *Additivity.* A count or a sum over a window equals the sum of it over the
  window's parts; a rate does not. Checking that for every metric read as
  `count` or `sum` catches this -- but only against a venue with traffic on
  more than one day or hour, and the local venue's events all arrive at once
  (Plausible's event endpoint cannot backdate). The venue needs its traffic
  spread over time, most likely by writing events with timestamps into its
  own ClickHouse.
- *The venue's truth for every metric,* not just the counts: a venue that
  sent its traffic knows its bounce rate, and would refuse to accept a "sum"
  of it.
- *The contract could say what a metric is* -- a count of events, a count of
  people, a sum of a value, a rate, an average -- so "rate read as sum" is
  refused at the manifest, before any number is read.
