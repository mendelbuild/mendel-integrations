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
| Generation test (`spike/generate/`) | Done: Plausible and Tavily pass after one repair round ($4.36); Mastodon, a publisher, reaches 0 failures with two verbs untested ($5.50); see "The generation test" |

## Contract 2 (2026-09-29)

The draft became the contract: `wrapperprotocol` speaks `"2"` alone, with
authorize, the secret credentials field, the action surface and search, and
without `describe_shape` and `limits`. And every available metric declares its
**kind** -- count, people, sum, rate or average, with what a rate or an average
is per and what a sum or an average is in -- and is read only as its kind
allows, so a rate offered as a sum is refused at the manifest (finding 14).
All three wrappers moved to it (0.2.0) and pass live again. Plausible now
offers the two metrics it can only give as a rate and an average -- bounce
rate per visit, visit duration in seconds per visit -- and refuses either over
a window nobody visited, where Plausible answers 0 and the honest answer is
that there is none; it still declines views per visit, which is two counts
Mendel can divide itself.

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

**Then a publisher (2026-09-29).** The same loop, on contract 2, wrote a
Mastodon wrapper from Mastodon's documentation and the `social_post` family
schema, run against @mdl_test with visibility pinned private in the
connection's config, the venue's grant read by the suite from the file a
person authorized once, and every post the reports mention retracted
afterwards with the hand-written wrapper (both were: nothing was left).

| Round | Spend | Suite | Note |
|---|---|---|---|
| 0 | $0.47 | -- | the API connection dropped mid-run (#46) |
| 1 | $0.29 | -- | the same |
| 2 | $2.63 | 18 pass, 3 fail, 4 warn, 2 untested | a missing field failed rather than being refused; read_back folded the link into the text; a gone post failed rather than being refused |
| 3 | $0.50 | 21 pass, 0 fail, 4 warn, 2 untested | all three fixed, before the connection dropped again |

$3.90, plus $1.59 across two earlier attempts lost to the machine sleeping
and to Mendel's executor stopping when the model narrated a next step instead
of calling a tool (#45): **about $5.50 for the publisher, $9.86 for the three
generated wrappers together.** Nothing failed at the end; it did not pass,
because two verbs are untested -- and that is the next finding.

**Answer, for now:** yes, with the suite as the loop's judge, an agent writes
a working wrapper from a fed-in spec in one repair round for a few dollars --
for a data source with an OpenAPI-less prose spec as well as a search tool
with an OpenAPI document. What stands between that and trusting one unread is
finding 14: the suite judges the protocol, and a wrapper can keep the
protocol and still report a wrong kind of number. A publisher was not
generated (the only venue is a real account, and each run posts), which is
the next thing to try once the suite can judge meaning as well as form.

## Status of each finding

The findings below are a log, in the order they were found; this is where each
stands now (2026-09-29).

| # | Finding | Status |
|---|---|---|
| 1 | The contract could not carry a publisher or a search | **Fixed** by contract 2: authorize, the action surface and search are in the wire types, and Mastodon and Tavily pass live on them |
| 2 | An open family of metrics (Plausible's goals) cannot be declared | Half: the wrapper no longer misuses a quality flag; declaring a family is **open** |
| 3 | A self-hosted Plausible is a complete venue | Holds; `venues/plausible-ce/` |
| 4 | An accepted event is not a counted one | **Fixed**: the venue waits on ingestion and reads its traffic back |
| 5 | The suite checks consistency, not truth | **Fixed**: a venue hands the suite its truth (`-expect`) |
| 6 | Authorization is one verb with steps, and begin produces secrets too | **Fixed**: `authorize` in contract 2, and on the server (connect, callback, disconnect) |
| 7 | Publish needs to carry Mendel's idempotency key | **Fixed**: `idempotency_key` in contract 2 |
| 8 | `publish(ref, when)` assumes a draft | **Fixed**: publish takes a payload when there is no draft |
| 9 | The connection's config has no declared shape | **Open** |
| 10 | `describe_shape` and `limits` repeat the manifest | **Fixed**: both removed in contract 2 |
| 11 | Credentials mix secrets with identifiers | **Fixed**: a credential can be marked `public` |
| 12 | A read can cost money, and the manifest cannot say so as data | **Open** |
| 13 | A search is honest only against a window it can prove | **Fixed**: Tavily declares it; the suite checks every windowed result's date |
| 14 | A generated wrapper reported rates as sums | Half: every metric declares its kind in contract 2, and a rate offered as a sum is refused; an additivity check that would catch a mislabelled kind is **open** |
| 15 | The harness and a wrapper drifted from the family schema | Half: both follow it now; building the suite's payloads from the schema itself is **open** |
| 16 | A generated wrapper stretches verbs to claim more | **Fixed**: every verb states its purpose in the contract (`Verb.Purpose`), and the suite now fails a stretched claim: `list_owned` is exercised by name and prefix, `append_update` and `set_cap` are refused for kinds without the trait, `draft` must not be live. The generated Mastodon wrapper, untested on these before, now fails both |
| 17 | Mendel's executor is brittle in long runs | Filed: mendelbuild/mendelbuild#45, #46 |

## Findings

**1. The contract cannot carry a publisher or a search today.** *(Fixed by
contract 2; see the status table.)* Of the
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
  refused at the manifest, before any number is read. *Done in contract 2*,
  as self-documentation, which Ben chose over the checks above for now: an
  honest wrapper can now say a metric is a rate, and a manifest that offers a
  rate as a sum is refused. A wrapper that mislabels the kind itself is still
  caught only by a person reading it; additivity against a venue with spread
  traffic would catch that, and is not built.

**15. The harness and a wrapper both drifted from the family schema, the
same way.** Feeding the `social_post` family schema to the generation test
showed it requires `text`, `link` and `media`, the last two nullable. The
hand-written Mastodon wrapper refused a `media` field outright, and the
harness published posts without one, so each agreed with the other and
neither with the kind's contract -- exactly what doc 35 §8 guards against in
saying conformance is "data-driven by the family schema". *Done:* the harness
sends family-conformant posts, the wrapper's shape refines the family (every
field kept and required, `media` narrowed to null) and it refuses media it
cannot attach (0.2.1). *Still to do:* the suite should build its payloads from
the family schema itself, rather than from a row it holds per kind.

**16. A generated wrapper stretches verbs to claim more, and the suite can
only say "untested".** The generated Mastodon wrapper claims two verbs the
hand-written one declines: `append_update`, implemented as editing the post's
text, and `list_owned`, implemented as "the most recent page of the account's
own posts". Both keep the protocol and stretch the meaning. `append_update`
exists for log-shaped kinds -- an incident is an append-only log -- and a
social post is not one; editing a post is a lifecycle the contract has no verb
for, so the agent reached for the nearest. `list_owned(prefix)` is how Mendel
finds what it created before creating it again; a recent page that ignores the
prefix cannot serve that, and says so only in a caveat. The suite reported
both untested, which is never a pass, so it did not approve them -- but it has
no check that would reject them either. The same shape as finding 14, and the
same remedy: the contract saying what a verb is for in terms a check can hold
a manifest to (`append_update` only for a kind with the log-shaped trait;
`list_owned` answering by the prefix it was given, exercised by publishing
with a known prefix and listing it back), and an agent told that declining is
a designed outcome, not a gap to fill. *Done:* the purposes are in the
contract and the README, the generation prompt says declining is a designed
outcome, and the suite's new checks fail the generated wrapper's two stretched
verbs (21 pass, 2 fail) while the hand-written one, which declines both, still
passes.

**17. Mendel's executor is brittle in ways only a long run shows.** It ends a
run when the model replies without a tool call, even when the reply is
narrating its next step with nothing written (mendelbuild#45), and it ends the
whole run on one dropped connection, with no retry (mendelbuild#46). Each cost
a round here; in Mendel's own code generation each costs one of three repair
attempts and its spend.
