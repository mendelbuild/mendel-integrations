# Mailgun wrapper

An External Tool Wrapper (contract 2) for [Mailgun](https://www.mailgun.com). Its purpose, as asked: send a short,
consented one-to-one check-in message to each new family two weeks after signup, and collect their replies.

## Written against

Mailgun's documentation, fetched by Mendel on **2026-10-08** and kept in `../spec/mailgun/`:

1. <https://documentation.mailgun.com/docs/mailgun/api-reference/send/mailgun/messages> — "Send an email",
   `POST /v3/{domain_name}/messages`: multipart form, `from`/`to`/`subject`/`text`, the `o:`, `h:` and `v:` options
   (`o:testmode`, `o:tracking`, `o:deliverytime`, `o:suppress-headers`), the 200 answer `{"id", "message"}`, basic auth,
   the US (`api.mailgun.net`) and EU (`api.eu.mailgun.net`) hosts.
2. <https://documentation.mailgun.com/docs/mailgun/user-manual/sending-messages/schedule-delivery> — scheduling
   with `o:deliverytime`, up to 3 or 7 days by plan.
3. <https://documentation.mailgun.com/docs/mailgun/api-reference/send/mailgun/messages/delete-v3--domain-name--envelopes>
   — "Delete scheduled and undelivered mail", which empties the whole domain's queue.

`spec_hash` in the probe is the SHA-256 of the three pages concatenated in file order (a test checks it).

Not from the pages: the basic-auth user name `api` (the pages show `<username>:<password>`); it is Mailgun's
well-known convention and the one thing here written from memory.

## What it does

| Verb | |
|---|---|
| `probe` | supported. Sends a message in **test mode** (`o:testmode=yes`, "processed normally but not actually delivered") to `mendel-probe@example.com`: the documentation describes no call that only reads the account, and this proves the key can send for the domain. |
| `publish` | supported, kind `direct_message` `{recipient, subject, body}`: one address, plain text, no tracking (`o:tracking=no`). `now`, or `at` an instant up to 7 days ahead (`o:deliverytime`). Not `announce`. Mendel's name and idempotency key go to Mailgun as `v:` custom data, with `o:suppress-headers=X-Mailgun-Variables` so the recipient does not see them. |
| everything else | declined, with the reason in the probe and in the refusal |

Declined, and why (full reasons in `manifest.go`):

- **draft** — Mailgun has no unsent state; a scheduled message is committed, and cannot be changed.
- **retract** — a sent message cannot be recalled, and the only delete empties the domain's whole queue.
- **status, read_back, read_metrics, list_owned** — the send answers only an id, and the calls that would say more
  (events, stats, stored messages) are not in the pages this was written against. No number is given rather than a
  made-up one, and Mailgun keeps no name for a message to list by.
- **authorize** (an API key is used), **append_update**, **set_cap**, **read_series**, **read_total**, **search**.

**Idempotency is `none`.** Mailgun's send has no idempotency key, so publishing twice sends twice; the caller must
not retry a publish whose outcome it does not know.

**Replies are not collected by this wrapper.** Receiving needs Mailgun's Routes, which are not in the documentation
read. The `reply_to` setting points replies at an address the project reads.

## Connection

- account: the sending domain; credential `MAILGUN_API_KEY`.
- endpoint: empty for the US host; `https://api.eu.mailgun.net` for the EU. When given, every request goes to it,
  keeping its path (`{endpoint}/v3/{domain}/messages`).
- settings: `from` (default `postmaster@<domain>`), `reply_to`.

## Fake and tests

`fakeapi` is an in-memory Mailgun for "Send an email": accepts any credential (but requires basic auth to be sent),
validates the fields the documentation marks required, keeps accepted messages (test-mode ones are not kept), and
refuses a `o:deliverytime` beyond 7 days. Tests in this directory run against it.

```
go test ./mailgun/...
docker build -f mailgun/Dockerfile -t mendel-tool-mailgun:dev .   # from the repository root
```

(`go build ./mailgun` from the repository root wants to write a file named `mailgun` where the directory is; use
`go build -o /tmp/mailgun ./mailgun`.)
