# Mastodon

Mendel's External Tool Wrapper for [Mastodon](https://joinmastodon.org), the
federated social network. It acts on one account's own posts as the
`social_post` External Asset Kind: it publishes a post, edits its text,
deletes it, reads it back, reads its engagement counts, lists the account's
own recent posts, and reports whether a given post is still live.

Written against docs.joinmastodon.org as fetched on 2026-09-28 at commit
`bbf263446459` (the pages are under `spec/mastodon` in this repository's
root, alongside every other wrapper's spec citations):

- `spec/mastodon/methods_apps.md`, `methods_oauth.md`, `api_oauth-scopes.md`
  -- registering an OAuth application and exchanging a code for a token
  (the `authorize` verb).
- `spec/mastodon/methods_accounts.md` -- verifying the credential and
  listing the account's own posts.
- `spec/mastodon/methods_statuses.md`, `entities_Status.md`,
  `entities_StatusSource.md` -- posting, editing, deleting, and reading a
  status back.
- `spec/mastodon/entities_Instance.md` -- the instance's own terms of
  service, cited in the manifest's `storage_policy`.
- `spec/mastodon/api_rate-limits.md` -- the manifest's `entitlements`.
- `spec/mastodon/social_post.family.schema.json` -- the shape a `social_post`
  asset takes here (embedded into the binary and returned verbatim as the
  `social_post` kind's `shape`).

## What it declines, and why

- **`draft`**: Mastodon posts a status live the moment it is created. It has
  no unpublished form of one to hold before that; its separate scheduled-post
  feature is not covered by the spec this wrapper was written against, so it
  is left alone rather than guessed at.
- **`read_series` / `read_total`**: Mastodon's API reports a post's or an
  account's counts as they stand now, not a windowed total to bucket by
  granularity. Read a single post's counts with `read_metrics` instead.
- **`search`**: this wrapper acts on one account's own posts; it does not
  search Mastodon's federated content on the account's behalf.
- **`set_cap`**: Mastodon exposes nothing this wrapper could set a numeric
  cap on.

## Connecting

A project supplies the instance's domain (e.g. `mastodon.social`, without
`https://`) as the account, and connects through Mastodon's own sign-in flow
(`authorize`): this wrapper registers an OAuth application on that instance
the first time, and reuses it on every later connection. What it keeps is
three credentials -- `MASTODON_ACCESS_TOKEN`, `MASTODON_CLIENT_ID`,
`MASTODON_CLIENT_SECRET` -- never typed by a person.

## Testing

```bash
go test -mod=vendor ./mastodon/...
```

Every test runs against an `httptest.Server` standing in for a Mastodon
instance; none call a real one. `TestWrapperJSONAgreesWithProbe` checks that
`wrapper.json`'s claims and the probe's manifest agree, and
`TestDockerfileEntrypointMatchesCommand` checks that the Dockerfile's
`ENTRYPOINT` is exactly `wrapper.json`'s `command`.

## Building

```bash
docker build -f mastodon/Dockerfile -t mendel-tool-mastodon:dev .
```

(the build context is this repository's root: the module's `go.mod`,
`go.sum` and vendored `wrapperprotocol` live there.)
