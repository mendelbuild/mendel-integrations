# Tavily wrapper

The External Tool Wrapper for [Tavily](https://tavily.com), a search API,
written against contract **2-draft**: it honours `probe` and `search` and
declares every other verb absent, with the reason. The protocol is
[`../README.md`](../README.md); the draft is `wrapperprotocol/draft.go` in
Mendel's module.

## The spec it was written against

Fed in, never recalled (doc 35 §8). Fetched 2026-09-29:

| Source | What was taken from it |
|---|---|
| docs.tavily.com/documentation/api-reference/endpoint/search (its OpenAPI 3.0.3 document) | `POST https://api.tavily.com/search`, `Authorization: Bearer <key>`; the body fields `query`, `search_depth` (`basic`, `advanced`, `fast`, `ultra-fast`; default `basic`), `max_results` (0 to 20, default 10), `topic` (`general`, `news`, `finance`), `start_date` and `end_date` (`YYYY-MM-DD`), `include_published_date`, `filter_by_published_date` (drops results outside the window and results with no detectable date), `include_domains` with `include_domains_mode` `restrict`; the answer `results[]` with `title`, `url`, `content` ("a short description"), `score`, `published_date` (Tavily's best estimate of publication or last update, e.g. `Tue, 11 Mar 2025 17:00:00 GMT`); errors `400`, `401`, `422`, `429`, `432` (key or plan limit), `433` (pay-as-you-go limit), `500`, each with `detail.error` |
| docs.tavily.com/documentation/api-reference/endpoint/usage | `GET /usage`: the key's `usage` and `limit`, and the account's `current_plan`, `plan_usage`, `plan_limit`; ten calls in ten minutes |
| docs.tavily.com/documentation/api-credits | 1,000 free credits a month; a `basic` search costs 1 credit and `advanced` 2 |
| docs.tavily.com/documentation/rate-limits | 100 requests a minute on a development key, 1,000 on production |
| tavily.com/terms (updated 2026-05-04) | "Output" is what the service delivers, including links; the customer must verify it before use and may not rely on it alone for decisions with a legal or similarly significant effect on a person, nor use it to build competing models |

The spec hash the wrapper reports is the SHA-256 of this file.

## What it assumes

- **The probe costs nothing.** It reads `/usage`, which proves the key and
  reports the plan and what has been spent, into the manifest's entitlements.
- **Every search costs credits,** which on a paid plan are money: 1 at
  `basic` depth, 2 at `advanced`. Depth and topic are the connection's config
  (`search_depth`, `topic`); `basic` and `general` when not set.
- **A window is whole UTC days** on Tavily's estimated publish or update date,
  and a windowed search drops results with no detectable date. `search` is
  declared partial for that.
- **One filter dimension, `domain`**, restricted to that domain.
- **A result is an `article`** (doc 35 §7): URL, title, the snippet Tavily
  returns, the host as its source, and the estimated date when there is one.
- **What may be kept** is a reference to each result, not the page: the
  results are third parties' content, and the manifest's storage policy says so.

## Errors

`400` and `422` are Tavily refusing the search and come back `refused` with its
sentence. `401`, `429`, `432`, `433` and `500` are `failed`. Nothing the wrapper
prints carries the key.

## Building

```bash
docker build -f tavily/Dockerfile -t mendel-tool-tavily:dev .
```
