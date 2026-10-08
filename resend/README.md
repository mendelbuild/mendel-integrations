# Resend wrapper

An External Tool Wrapper (contract `2`) for [Resend](https://resend.com), written for one purpose:
a short, consented, one-to-one check-in message to each new family two weeks after signup, and
what can be known of it afterwards. It publishes one External Asset Kind, `direct_message`
(`recipient`, `subject`, `body`), sent as plain text from the connection's account.

## Documentation used

Written against the four pages in `../spec/resend/`, all fetched by Mendel on **2026-10-08**:

- Cancel Email — https://resend.com/docs/api-reference/emails/cancel-email
- List Sent Emails — https://resend.com/docs/api-reference/emails/list-emails
- Retrieve Sent Email — https://resend.com/docs/api-reference/emails/retrieve-email
- Update Email — https://resend.com/docs/api-reference/emails/update-email

`spec_hash` in the manifest is the SHA-256 of those four files concatenated in file-name order.

**What those pages do not cover**, and what was therefore taken from elsewhere. Check these against
Resend's Send Email page before relying on them:

- `POST /emails` (Send Email) was not fetched. Its body (`from`, `to`, `subject`, `text`,
  `scheduled_at`, `tags`) is written from the fields Retrieve Sent Email returns and the
  `scheduled_at` format Update Email shows; the answer `{"id": ...}` and the `Idempotency-Key`
  header are from memory of the API.
- The rule that tag values hold ASCII letters, digits, underscores and dashes is from memory.
- List pagination (`limit`, `after`) is from the List Sent Emails page's Go example (`After`) and
  memory; the Pagination page was not fetched.
- The error body shape (`statusCode`, `name`, `message`) is from memory; the Errors page was not fetched.
- `last_event` values other than `delivered` and `opened` (the page's examples) — `scheduled`,
  `canceled`, `clicked` — are from memory; the Email Events overview was not fetched.
- Resend's terms were not read, so the manifest's `storage_policy` cites none.

## Connection

- Account: a domain verified at Resend (mail is sent from `hello@<domain>`) or a full from address.
- Credential `RESEND_API_KEY`.
- Every request goes to the connection's `endpoint` when one is given, keeping its path; otherwise
  `https://api.resend.com`.

## Verbs

Honoured:

| Verb | How |
|---|---|
| `probe` | `GET /emails?limit=1` proves the key can read the account, then the manifest |
| `publish` (partial) | `POST /emails`; `when` now sends, `at` schedules (`scheduled_at`), `announce` is refused. `idempotency_key` becomes the `Idempotency-Key` header; Mendel's `name` is kept as the tag `mendel_name` |
| `status` | `GET /emails/:id`: `scheduled` is not live, `canceled` or unknown is gone, anything else live |
| `read_back` | `GET /emails/:id`, as `recipient`, `subject`, `body` (the text part); refused for a canceled or unknown email |
| `read_metrics` (partial) | `delivered` (1 when the last event is delivered, opened or clicked, else 0). `opened`, `clicked` and `replies` are declared unavailable, with why |
| `list_owned` (partial) | lists emails and reads each one's tags, since the list carries none; the cost grows with sending history, and a history longer than 5000 emails is a failure, never a short answer |

Declined, each with its reason in the manifest: `authorize` (API key, no flow), `draft` (Resend has no
held, unsent state: a scheduled email sends itself), `retract` (a sent email cannot be recalled;
Cancel Email works only while scheduled, so it could not answer as retract must, twice and for
anything published; the contract lets a kind that cannot be recalled decline it), `append_update`,
`set_cap`, `read_series`, `read_total`, `search`.

Cancel Email and Update Email are therefore not called by the wrapper; the fake implements them as
documented. Replies are not read: they arrive as received emails (the Receiving endpoints were not in
the documentation), so `replies` is unavailable rather than zero.

Consent is Mendel's approval of the asset; the wrapper's part is to send exactly one bare address per
message and refuse a list, a display name or any other field.

## Fake and tests

`fakeapi.Handler()` is an in-memory Resend (send, retrieve, list with pagination, update, cancel; any
credential accepted; matched from the `emails` path segment on, so any prefix works). `go test ./resend/...`
runs the wrapper against it. The contract's conformance suite was also run against the built binary and
the fake with `-fake-venue` and passed.
