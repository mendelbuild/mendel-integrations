# Ayrshare wrapper

An External Tool Wrapper (contract 2) for [Ayrshare](https://www.ayrshare.com), to help a
person publish and compare posts in parenting and personal-finance community forums. It
publishes External Asset Kind `social_post`, to Reddit.

```bash
docker build -f ayrshare/Dockerfile -t mendel-tool-ayrshare:dev .   # from the repository root
go test ./ayrshare/...
```

(`go build ./ayrshare` names its output `ayrshare`, which is this directory; use
`go build -o /dev/null ./ayrshare` to check that it compiles.)

## What it was written against

Ayrshare's documentation, fetched by Mendel on **2026-10-07** into `spec/ayrshare/`:

1. `01-...-rest-api-endpoints-post.txt`: https://docs.ayrshare.com/rest-api/endpoints/post, the Post
   API Overview. Used for: approval workflow (`requiresApproval`, status "awaiting approval", PATCH
   `approved: true`), `scheduleDate`, `notes`, `idempotencyKey` (a repeat, "regardless of the post's
   state ... success, error, pending, or deleted", is an error), the post response (`id`, `status`,
   `postIds[]` with `postUrl`), media URL rules, "Line Breaks" (`\u2063\n`), Reddit-flavoured Markdown,
   "Image or Video Only" (which networks take blank text), the GET / DELETE / PATCH calls by post ID.
2. `02-...-media-guidelines-reddit.txt`: https://www.ayrshare.com/docs/media-guidelines/reddit. Used for:
   Reddit images only, JPG, PNG, GIF or WEBP, 10 MB; no video.
3. `03-...-whatsnew-archive.txt`: https://www.ayrshare.com/docs/whatsnew/archive. Used for: `notes`
   "can be retrieved via the history endpoint" (September 2023); deleted posts can be retrieved via
   the history endpoint (September 2022); history returns the latest 20 records by default and up to
   500 with `count` (March 2022); Reddit analytics are the account's karma, profile image and page
   URL (November 2022).

`spec_hash` in the manifest is the SHA-256 of the three files concatenated in name order; a test
recomputes it.

### What the fetched pages do not say, and what the wrapper assumed

The pages are an overview, not the reference for each endpoint. These come from knowledge of
the API outside them, and are the first things to check against the reference:

- The base URL `https://api.ayrshare.com/api` (used only when the connection gives no endpoint),
  and `Authorization: Bearer <API key>`.
- The paths: `POST /post`, `GET /post/{id}`, `PATCH /post` and `DELETE /post` with `{"id"}` in the
  body, `GET /history?count=500`. The pages name the calls ("the /post PATCH endpoint", "the DELETE
  call with the post ID", "the GET call with the post ID") but not the paths.
- The shape of an error body (`message`) and that a rejected post is HTTP 400.
- Reddit's own fields, `redditOptions.subreddit` and `.title`, sent when the connection's
  `subreddit` setting is set. The pages mention Reddit flair only in the changelog.
- That the history includes posts awaiting approval and deleted posts, and carries `notes`.

## The verbs

Honoured: `probe`, `draft`, `publish`, `status`, `read_back`, `retract`, `list_owned`.

| Verb | How |
|---|---|
| `probe` | `GET /history?count=1`: the cheapest call that proves the key reads the account. |
| `draft` | `POST /post` with `requiresApproval: true`: Ayrshare holds it "awaiting approval", sent to no one. If Ayrshare ever answers such a post as published, the wrapper deletes it and fails. |
| `publish` | A draft by its ref: `PATCH /post` with `approved: true` (and `scheduleDate` for `at`). A payload: `POST /post`, now or with `scheduleDate`. **Partial**: `announce` is refused, Ayrshare has no such mode. |
| `status` | `GET /post/{id}`: `success` is live, `deleted` gone, everything else (awaiting approval, scheduled, pending) not live. |
| `read_back` | The post as Ayrshare holds it, as `{text, link, media}`. Refused once deleted. |
| `retract` | `DELETE /post`. Ayrshare keeps a deleted post in its history, marked deleted, so nothing is destroyed; a second retract finds it deleted and answers `deleted` without a second delete. |
| `list_owned` | Ayrshare has no name for a post, so Mendel's name is the first line of the post's `notes`; the list is the posts of the latest 500 of the history whose name starts with the prefix, deleted ones left out. **Partial**: posts older than the latest 500 are not found. |

Declined, each with its reason in the manifest:

- `read_metrics`, `read_series`, `read_total`: the fetched pages describe no per-post analytics for
  Reddit; the only Reddit analytics they mention are account-level (karma). "What each brings in" cannot
  be read through this wrapper until Ayrshare's analytics reference is fetched and shown to give a
  post's numbers for Reddit; the manifest lists no metric, and none is invented. The same goes for a series or a total.
- `authorize`: an API key a person pastes in; no flow to run.
- `append_update`: a social post is not log-shaped.
- `set_cap`: a post spends nothing a cap could bound.
- `search`: the history takes no query.

### Idempotency

Ayrshare answers a reused `idempotencyKey` with an error, not with the first post. The wrapper puts
the key in the post's notes (`<name>\nmendel:{"key":...,"link":...}`) and, on that error, finds the
first post in the latest 500 of the history and answers with it. If that post has since been deleted,
the repeat is refused (Ayrshare will not take the key again).

### The social_post shape

`{text, link, media}`, all required, `link` and `media` nullable:

- `text`: 1 to 10000 characters, Reddit-flavoured Markdown. The limit is this wrapper's, not
  Ayrshare's (the pages give none for Reddit). New lines are sent as `\u2063\n` as the pages ask.
- `link`: an http(s) URL, sent at the end of the text after a blank line; read back by the
  link kept in the notes, so a post edited elsewhere reads back as text alone.
- `media`: null, or a non-empty list of image URLs (JPG, PNG, GIF, WEBP); video is refused.

### Connection

Credential `AYRSHARE_API_KEY`. Optional setting `subreddit` (the community posts go to). The
connection's `endpoint`, when given, is used in place of Ayrshare's base URL, path included.

## The fake

`fakeapi.Handler()` is an in-memory Ayrshare as the pages above describe it: posts, approval,
schedules, idempotency keys, history (20 by default, 500 at most, deleted posts included), Reddit's
media rules. It accepts any credential and keeps state between requests.
