# tavily -- Mendel's External Tool Wrapper for Tavily

A search tool (GUIDE.md): Mendel calls `probe` and `search` against a real
account, and gets every other verb of contract `2-draft` declined with a
reason (`wrapper.go`'s `declineReasons`). It is not a data source or a
publisher -- Tavily has no analytics stream to read and nothing for a
wrapper to draft, publish, or retract.

## What this was written against

Fetched **2026-09-29** from `docs.tavily.com` and `tavily.com`, and kept
verbatim in `spec/tavily/` for anyone re-reading this later without a
network:

- `spec/tavily/search.md` -- `POST /search`'s OpenAPI schema: request fields
  (`query`, `search_depth`, `max_results`, ...), the 200 response shape
  (`results[]`, each with `title`, `url`, `content`, `published_date`, ...),
  and its error responses (400, 401, 422, 429, 432, 433, 500). Drives
  `search.go`.
- `spec/tavily/usage.md` -- `GET /usage`'s OpenAPI schema: `key.*` and
  `account.*` usage and plan fields. Drives `probe.go`'s call and the
  `entitlements` it reports.
- `spec/tavily/api-credits.md` -- what each Tavily endpoint costs in API
  credits. `basic`/`fast`/`ultra-fast` search depths cost 1 credit,
  `advanced` costs 2; nothing on this page prices `/usage`. This is why
  `probe` calls `/usage`, never `/search`, and why `search.go` pins
  `search_depth` to `"basic"` rather than leaving it to `auto_parameters` or
  the caller.
- `spec/tavily/rate-limits.md` -- the fixed RPM ceilings by key environment
  (Development 100, Production 1,000), and that `/usage` has its own,
  separate limit. Quoted into probe's manifest as `entitlements.rate_limit_rpm`,
  since `/usage`'s own response does not say which kind of key answered it.
- `spec/tavily/terms.txt` -- Tavily's Platform Terms of Service (page footer:
  "Last updated: May 4, 2026"), fetched from `https://tavily.com/terms`.
  Sections 3.4, 6, 6.5, 7 and 9.2 are what `storagePolicy` (`wrapper.go`)
  cites for the manifest's `storage_policy`.

`spec_hash` in every manifest is `sha256:799d02149d52787de984725d61588d7f9ccb9d9d9ca3c332bfcceb42acd7566e`,
computed as:

```
cat search.md usage.md api-credits.md rate-limits.md terms.txt | sha256sum
```

run from `spec/tavily/`, in that order -- reproducible from what is checked
in, so a later change to any of the five is visible as a changed hash.

## Design decisions this record made, and why

- **`probe` never calls `/search`.** The task that follows this wrapper
  everywhere it runs is "an account whose every search spends credits";
  `/usage` is free (no credit cost is listed for it in
  `spec/tavily/api-credits.md`, and it has its own generous, separate rate
  limit per `spec/tavily/rate-limits.md`) and still proves the key is good,
  which is all `probe` owes (GUIDE.md, "Writing one" §2: "the cheapest call
  that proves the credential can read the account").
- **`search` always asks for `search_depth: "basic"`.** The contract's
  `VerbCall` for `search` carries only `query` and `limit` (`draft.go`); it
  gives Mendel no way to ask for `advanced`'s extra relevance at its extra
  cost, so this wrapper never spends the second credit on its own initiative.
  It does not set `auto_parameters` either, since Tavily may pick `advanced`
  for you under that flag (`spec/tavily/search.md`), which would spend
  credits Mendel did not ask for.
- **`search` refuses an empty query.** The contract's `query` argument is a
  plain string with no minimum length (`draft.go`); Tavily's own schema marks
  `query` `required` (`spec/tavily/search.md`) but only fails it with a 422
  from the API. Refusing it before the call is a designed outcome, not a
  wasted credit and a failure that looks like Tavily's fault.
- **`search` refuses any `filter`.** `probe`'s manifest declares no
  `filter_dimensions` (`probe.go`) -- `/search` has nothing shaped like the
  contract's equality filter on a named dimension, only its own free-form
  parameters (`include_domains`, `country`, `language`, ...) that the
  contract's `search` call carries no field for. A call that names a `filter`
  is refused rather than silently ignored or misapplied to one of those.
- **`search` narrows a `window` with `start_date`/`end_date` and
  `filter_by_published_date: true`.** Left at Tavily's default
  (`filter_by_published_date: false`), `start_date`/`end_date` only narrow
  what Tavily manages to date and leave every undated result in -- not "only
  what the window holds". Setting it true also drops undated results, since
  nothing then says they fall inside the window either. A call with no
  `window` sends neither field, unchanged from before.
- **`limit` above 20 is refused, not capped.** `max_results`'s documented
  ceiling is 20 (`spec/tavily/search.md`). The contract's rule for
  `read_series`'s granularity -- refuse what cannot be served, never
  substitute -- is applied here for the same reason: a caller relying on
  getting back what it asked for should be told plainly that it cannot,
  rather than silently receiving fewer.
- **`Item.Source` is the result URL's host.** Tavily's `/search` response
  names no separate publisher field (`spec/tavily/search.md`); the host is
  the only thing in the response that answers "where is this from" without
  a second call (which would cost more credits for no contractual benefit,
  since the contract has nowhere else to put it).
- **`include_published_date` is always requested, `include_answer` never
  is.** The former is free metadata the response can carry
  (`published_at` on `Item`); the latter has nowhere to go in the contract's
  `search` result (`Item` has no answer field) and, per the Terms
  (§6, §6.5), an LLM-generated answer is "AI Functionality" Output that
  Tavily and its AI providers may separately retain to train their models
  -- worth avoiding when nothing asked for it.
- **`authorize` is declined.** Tavily's own authentication is a bearer API
  key a person copies from `app.tavily.com`; there is no per-instance client
  registration or redirect for a wrapper to drive (contrast the OAuth tools
  contract `2-draft` was built for, `draft.go`'s doc comment). The
  connection asks for the key as a plain credential, not `authorize`'s
  produced one.
- **`venue: "read_only"`.** `search` reads the web through Tavily and
  reports it; it creates or mutates nothing that persists in the Tavily
  account itself (unlike a publisher's `draft`/`publish`). It does spend the
  account's credits on every call, which is why `probe` is built never to
  trigger one.

## Testing

`wrapper_test.go` never calls the real API: `net/http/httptest` stands in
for `api.tavily.com`, fed canned bodies shaped like `spec/tavily/usage.md`
and `spec/tavily/search.md`'s examples. It checks:

- `probe` builds a manifest that passes `wrapperprotocol.CapabilityManifest.CheckAs`
  for contract `2-draft`, with every verb of the contract accounted for.
- `probe` fails (not refuses) on a rejected key or a missing credential.
- `search` maps Tavily's `results[]` into `Item`s, parses `published_date`
  when present and leaves `PublishedAt` nil when it is not, derives `Source`
  from the URL, and asks for `search_depth: "basic"`.
- `search` refuses a negative limit and a limit over 20, and omits
  `max_results` (letting Tavily's own default apply) when none was given.
- `search` refuses an empty query, and any `filter` (no `filter_dimensions`
  are declared).
- `search` sends `start_date`, `end_date` and `filter_by_published_date` when
  the call carries a `window`, and neither when it does not.
- every verb other than `probe` and `search` is refused, and the run stops
  there (an intentionally over-eager second call is never reached).
- a request for a contract other than `2-draft` fails the first call and
  stops, per GUIDE.md.
- the wrapper's own `WrapperResponse` round-trips through
  `wrapperprotocol.ParseWrapperResponse` without complaint.
- `wrapper.json` parses, passes `wrapperprotocol.Description.Check`, and its
  `claims` agree exactly with what a real `probe` answers (GUIDE.md: "The
  wrapper's own tests check its probe does not decline anything this file
  claims").

Run with:

```bash
go build -mod=vendor ./tavily
go test -mod=vendor ./tavily
```

Building the image (needs the repository root as build context, since
`vendor/` and `go.mod` live there):

```bash
docker build -f tavily/Dockerfile -t mendel-tool-tavily:dev .
```
