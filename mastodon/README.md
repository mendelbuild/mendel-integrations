# Mastodon wrapper

The External Tool Wrapper for [Mastodon](https://joinmastodon.org), written
against contract **2-draft**: it connects an account through the instance's
own OAuth (`authorize`), and publishes, reads back, reads the counts of and
retracts a `social_post`. Every other verb is declared absent, with the reason.
The protocol is [`../README.md`](../README.md); the draft is
`wrapperprotocol/draft.go` in Mendel's module.

## The spec it was written against

Fed in, never recalled (doc 35 §8). Fetched 2026-09-28 from the source of
docs.joinmastodon.org, `mastodon/documentation` at `bbf263446459`:

| Source | What was taken from it |
|---|---|
| methods/apps | `POST /api/v1/apps` with `client_name`, `redirect_uris`, `scopes`, `website`; answers `client_id` and `client_secret`, to be treated as passwords; `urn:ietf:wg:oauth:2.0:oob` shows the code instead of redirecting; since 4.3 applications are no longer vacuumed |
| methods/oauth | `GET /oauth/authorize` with `response_type=code`, `client_id`, `redirect_uri`, `scope`, `state`, `force_login`; the code comes back as `code` (treat as a password); `POST /oauth/token` with `grant_type=authorization_code`, `code`, `client_id`, `client_secret`, `redirect_uri`, answering `access_token` with no expiry; `POST /oauth/revoke` with `client_id`, `client_secret`, `token` |
| api/oauth-scopes | Scopes are hierarchical, least privilege is recommended; `profile` (4.3+) reaches only `verify_credentials`; `read:statuses`, `write:statuses` |
| methods/statuses | `POST /api/v1/statuses` with `status`, `visibility` (`public`, `unlisted`, `private`, `direct`), an `Idempotency-Key` header kept for up to an hour, `scheduled_at` at least five minutes ahead; `GET /api/v1/statuses/:id`; `DELETE /api/v1/statuses/:id`; `GET /api/v1/statuses/:id/source` for the plain text as posted |
| entities/Status | `id`, `url`, `visibility`, `favourites_count`, `reblogs_count`, `replies_count`, `quotes_count` |
| entities/Instance | `configuration.statuses.max_characters` (500 by default) and `characters_reserved_per_url` (23) |
| api/rate-limits | 300 calls in five minutes per account and per IP; `DELETE /api/v1/statuses/:id` 30 times in thirty minutes |

The spec hash the wrapper reports is the SHA-256 of this file, so editing the
record above changes the wrapper's provenance.

## What it assumes

- **The account is the instance.** `@you@mastodon.social` is connected with
  account `mastodon.social`; the person's own identity comes from their grant,
  and the probe reports it (`verify_credentials`) once there is a token.
- **authorize does the whole OAuth dance, and Mendel only hosts the redirect.**
  `begin` registers a client named Mendel on the person's instance (reusing one
  the connection already holds for the same redirect) and answers the URL to
  open; `complete` exchanges the code for a token; `revoke` ends it. The client
  id, secret and redirect come back as credentials with the token, because the
  client is registered on the person's instance and is theirs, like the token.
  Tokens do not expire, so there is no `refresh`.
- **Least privilege.** `profile read:statuses write:statuses`: who the account
  is, and its own statuses. Nothing about followers, notifications or
  direct messages.
- **Posts are followers-only unless configured otherwise.** Visibility is the
  connection's config (`visibility`), and `private` when it is not set. A
  growth Hop that means to be seen says `public` in its config; a conformance
  run on a locked account with no followers is seen by nobody.
- **A link is appended after a blank line** and taken back off by `read_back`,
  which reads the status's plain-text source rather than its HTML. The shape
  keeps the text short enough that a link always fits.
- **Counts are lifetime totals as this instance has seen them.** A federated
  post's favourites on other servers may not all have arrived; every reading
  says so with its quality flags. Impressions are declared unavailable:
  Mastodon does not count them.
- **Idempotency is the instance's.** A publish carries Mendel's key as
  `Idempotency-Key`, so a repeated publish within the hour answers the same
  status rather than a second one.

## Errors

A `422` on posting is the instance refusing the post and is `refused` with its
sentence; a `400` from the token endpoint is the code refused. A `404` on a
status is "gone": `status` answers it, `read_back` and `read_metrics` refuse,
and `retract` treats it as already retracted. Anything else is `failed`.
Nothing the wrapper prints, on either stream, carries a credential.

## Building

```bash
docker build -f mastodon/Dockerfile -t mendel-tool-mastodon:dev .
```
