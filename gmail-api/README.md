# gmail-api: External Tool Wrapper for Gmail API

Sends a personal message to one named person from the project owner's own
account, with the owner approving the exact text. Contract `2`, version `0.1.0`,
External Asset Kind `direct_message` (`recipient`, `subject`, `body`).

| Verb | Answer |
|---|---|
| `probe` | supported: `users.getProfile` (1 quota unit) proves the credential reaches the account, then the manifest |
| `draft` | supported: `users.drafts.create`; the message carries the `DRAFT` label and goes to no one |
| `publish` | partial: from a draft by its ref (`users.drafts.send`) or from a payload (`users.messages.send`), now only; a time or an announcement is refused; Gmail API documents no idempotency key, so the manifest says `none` and a repeat sends again |
| `read_back` | supported: `users.drafts.get` / `users.messages.get` with `format=raw`, read as RFC 2822 into `recipient`, `subject`, `body` |
| `status`, `retract`, `read_metrics`, `list_owned` | declined: the documentation found does not show Gmail API doing them, so they are not written from memory |
| `authorize` | declined: the wrapper takes the project's OAuth 2.0 access token (`GMAIL_API_ACCESS_TOKEN`); Gmail's authorization pages were not among the documentation |
| `append_update`, `set_cap`, `read_series`, `read_total`, `search` | declined, each with its reason in the manifest |

Refs are `draft:<id>` and `message:<id>`. Publishing a draft deletes it and
creates a new message with a new id and the `SENT` label (the drafts guide), so
the ref `publish` answers for a draft is `message:<new id>`, and the draft's own
ref is then refused.

The text is sent exactly as approved: the body travels base64 encoded in a
`text/plain; charset="UTF-8"` part, a non-ASCII subject as RFC 2047 words. A
payload that could not be read back identically (a subject with a line break
or leading/trailing space, a recipient that is a list or has a display name, a
field the family does not have) is refused rather than sent altered. The subject
is bounded at 200 characters: the wrapper's own bound, not one the documentation states.

## Connection

- Account: the owner's Gmail address, used as the `userId` of every path (`me` when empty) and as `From`.
- Credential: `GMAIL_API_ACCESS_TOKEN`, sent as `Authorization: Bearer`.
- Endpoint: when given, every request goes to it, keeping its path
  (`<endpoint>/gmail/v1/users/<account>/...`); otherwise `https://gmail.googleapis.com`.

## Documentation used

Fetched by Gregor on **2026-10-21**, kept in `spec/gmail-api/`:

1. `https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.drafts`: the Draft resource (`id`, `message`) and its methods.
2. `https://developers.google.com/workspace/gmail/api/reference/rest`: the service endpoint `https://gmail.googleapis.com` and the method paths used here: `GET /gmail/v1/users/{userId}/profile`, `POST .../drafts`, `POST .../drafts/send`, `GET .../drafts/{id}`, `POST .../messages/send`, `GET .../messages/{id}`.
3. `https://developers.google.com/workspace/gmail/api/guides/drafts`: drafts carry the `DRAFT` label; the message is an RFC 2822 MIME message, base64url encoded in `message.raw`; sending a draft deletes it and returns a new message with the `SENT` label; `format=raw` returns the MIME message; the body of `drafts.send` names the draft `id`.
4. `https://developers.google.com/workspace/gmail/api/reference/quota`: per-method quota units (getProfile 1, drafts.create 10, drafts.send 100, messages.send 100) and the per-minute and per-day limits, shown in the manifest's entitlements.

`spec_hash` in the manifest is the SHA-256 of those four files concatenated in file-name order.

Not in what was fetched, so not relied on: the Message resource's other fields, the
`format` values beyond `raw`, the shape of getProfile's answer (the wrapper reads none of it),
the error body (the wrapper reads `error.message` where there is one and otherwise quotes the
status), the authorization pages, and the terms for keeping what is read (the manifest says so).

## The fake: `fakeapi`

`fakeapi.Handler()` is an in-memory Gmail API for the methods above, plus `drafts.list`, `drafts.update`, `drafts.delete`,
`messages.list` and `messages.delete`. It keeps state between requests, so what is sent is
read back and counted; it accepts any credential; it finds its routes by `/gmail/v1/` in
the path, so it answers behind an endpoint's path prefix. A send with no recipient is
a 400, an unknown id a 404, and a sent draft is gone. Where the documentation is silent
(the fields of a Message beyond `id`, `threadId`, `labelIds`, `raw`) it answers the few the
wrapper could use and nothing it does not need.

## Tests

`go test ./gmail-api/...` runs the wrapper against the fake: the manifest through `CapabilityManifest.Check`, answers
through `ParseWrapperResponse`, `wrapper.json` against the manifest and the Dockerfile's `ENTRYPOINT`,
round trips of publish and read_back (unicode, long subject, line breaks), the draft lifecycle, every
refusal, and that every request goes to the endpoint under its path. No test calls the real API.

`go build ./gmail-api` from the repository root names its output `gmail-api`, which is this
directory; build with `go build -o /tmp/gmail-api ./gmail-api` where that matters. The image is
built from the repository root: `docker build -f gmail-api/Dockerfile -t mendel-tool-gmail-api:dev .`
