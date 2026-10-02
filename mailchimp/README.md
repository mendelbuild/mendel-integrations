# mailchimp

An External Tool Wrapper for [Mailchimp](https://mailchimp.com) (contract
version 2). It sends scheduled reminder emails to an audience the project
already owns, as a Mailchimp "regular" campaign -- the contract's
`email_broadcast` External Asset Kind -- so multiple waves can go out timed
to an official's calendar without a person re-sending by hand.

## What it does

| Verb | Mailchimp call(s) |
|---|---|
| `probe` | `GET /lists/{id}` -- the cheapest read that shows the credential can read the named audience |
| `draft` | `POST /campaigns` (type `regular`) then `PUT /campaigns/{id}/content` |
| `publish` | the same two calls when there is no ref, then `POST .../actions/send` (`now`) or `POST .../actions/schedule` (`at`); `announce` is refused, there is no such mode |
| `status` | `GET /campaigns/{id}`, mapped from Mailchimp's own `save` / `schedule` / `sending` / `sent` |
| `retract` | `DELETE /campaigns/{id}` for a draft never scheduled or sent; `POST .../actions/unschedule` for one scheduled; `POST .../actions/cancel-send` for one sent |
| `read_back` | `GET /campaigns/{id}` and `GET /campaigns/{id}/content`, refused once retracted |
| `read_metrics` | `GET /reports/{id}` for `opens`, `clicks`, `bounces`; unavailable (never zero) before the campaign has been sent |
| `list_owned` | `GET /campaigns?list_id=...`, filtered client-side by the prefix kept in the campaign's own internal `settings.title` |

Every other verb of the contract is declined, with its reason, in
`manifest.go`: `authorize` (the wrapper connects with an account API key,
never an OAuth redirect it would have to drive), `append_update`
(`email_broadcast` is not log-shaped), `set_cap` (a campaign has no spend to
cap), `read_series` and `read_total` (this wrapper reads one campaign's own
metrics, not an account-wide data source), and `search` (Mailchimp's own
Search Campaigns matches an account's campaigns for a person browsing them,
not the article family `search` reads back).

`publish` is declared **partial**: Mailchimp has no announced or
preview-before-send mode, so `When.Mode == "announce"` is refused; `now`
and `at` are both honoured.

### Idempotency, and how `retract` stays safe twice

Mailchimp's campaign has no idempotency-key field of its own, and no field
meant for arbitrary bookkeeping beyond its internal `settings.title` (never
shown to a recipient). This wrapper uses that one field for two things a
real client of this API would otherwise have to track itself:

- the name Mendel gave the asset, so `list_owned` can filter by prefix;
- after it, marked off by `" #mendel-idempotency:"`, the idempotency key a
  `publish` with no `ref` was given, so a repeat with the same key is found
  by searching campaigns in the audience for one whose title ends with that
  marker, and answered again rather than sent a second time.

Similarly, Mailchimp gives no durable "this was taken back" flag on a
campaign already sent or scheduled -- `cancel-send` and `unschedule` just
change its `status`, to values a fresh draft can also be in. The wrapper
(and the fake in `fakeapi/`, which is the only thing it ever talks to in
this repository's own tests) carries two extra fields on the campaign
resource for this, `_mendel_retracted` and `_mendel_retracted_as`; they are
not Mailchimp's own, are written only by this wrapper and its fake, and
exist so that a second `retract` -- and `status` and `read_back` afterwards
-- see the asset is already gone rather than trying to retract it twice
over the real endpoints, which Mailchimp itself would refuse the second
time.

## The account a project connects

- `connection.account` is the Mailchimp **Audience (List) ID** the reminders
  go to -- the audience the project owns, per the brief this wrapper was
  requested for.
- `connection.credentials` is one `MAILCHIMP_API_KEY`: Mailchimp's own
  account API key, which carries its data center as the suffix after the
  key's last `-` (Fundamentals: "if your API key is
  `0123456789abcdef0123456789abcde-us6`, then the data center subdomain is
  `us6`"). The wrapper derives `https://<dc>.api.mailchimp.com/3.0` from it.
  When the run's connection also names an `endpoint` (a self-hosted proxy,
  or a stand-in for Mailchimp on the loopback for testing), every request
  goes there instead, keeping its path, and the data center is never
  derived.
- `connection.config` has two settings a campaign needs beyond the kind's
  own family fields, since Mailchimp requires both to create one:
  `from_name` and `reply_to`, each with a usable default so a project may
  leave them unset.

Authentication is HTTP Basic, `anystring:TOKEN` (Fundamentals: "You can
either use HTTP Basic Authentication or Bearer Authentication"); this
wrapper uses Basic, since that is the form Fundamentals shows first and
either is accepted the same way.

## The spec this was written against

Fetched by Mendel on **2026-09-30**, and cited by filename in `spec/mailchimp/`:

1. `01-mailchimp-com-developer-marketing-api.txt` --
   <https://mailchimp.com/developer/marketing/api/> -- the full list of
   Marketing API v3 resources and what each does, including every
   Campaigns endpoint this wrapper calls (Add/Get/Delete campaign; Cancel,
   Send, Schedule, Unschedule campaign; Get/Set campaign content) and Get
   list info, Get campaign report and Ping.
2. `02-mailchimp-com-developer-marketing-docs-fundamentals.txt` --
   <https://mailchimp.com/developer/marketing/docs/fundamentals/> -- the
   API root's `<dc>` data center form, where it comes from in an API key,
   and HTTP Basic/Bearer authentication.
3. `03-mailchimp-com-developer-marketing-api-campaigns.txt` --
   <https://mailchimp.com/developer/marketing/api/campaigns/> -- the
   Campaigns resource group (the spec_source this wrapper's manifest
   cites).
4. `04-mailchimp-com-developer-marketing-api-campaigns-list-campaig.txt` --
   <https://mailchimp.com/developer/marketing/api/campaigns/list-campaigns/>
   -- List campaigns.
5. `05-mailchimp-com-developer-marketing-api-campaigns-get-campaign.txt` --
   <https://mailchimp.com/developer/marketing/api/campaigns/get-campaign-info/>
   -- Get campaign info.
6. `06-mailchimp-com-developer-marketing-api-campaigns-schedule-cam.txt` --
   <https://mailchimp.com/developer/marketing/api/campaigns/schedule-campaign/>
   -- Schedule campaign, and (by the same page listing every action) Send,
   Unschedule and Cancel campaign.

Mailchimp's developer site renders its endpoint-by-endpoint request and
response bodies with JavaScript; what a plain fetch of each URL above
captured is each page's static shell -- the same account-wide navigation
and one-line description for every endpoint the Marketing API has, repeated
on every page (diffing files 3-6 confirms it: only the fetched URL and
page title differ). No request or response schema came through the fetch.
Where this wrapper needed a field name or a JSON shape the fetch did not
render -- a campaign's `type`, `recipients.list_id`, `settings.subject_line`
/ `preheader` / `title` / `from_name` / `reply_to`, `schedule_time`,
content's `html` / `plain_text`, and a report's `opens_total` /
`unique_opens` / `clicks_total` / `unique_subscriber_clicks` /
`hard_bounces` / `soft_bounces` -- it is this wrapper's own design,
consistent with what the fetched pages do say each endpoint is for (cited
in the tables above and in `manifest.go`'s decline reasons), not a restated
memory of the API's wire format. `fakeapi/` implements exactly this design,
so the wrapper and its fake agree by construction; a real Mailchimp account
would need the same shapes this wrapper sends to be checked against the
live API before the image is ever registered for a project (GUIDE.md,
"Registering, reviewing, retiring": a Mendel admin's review runs the
conformance suite against the wrapper's own venue account first).

## Testing

`wrapper_test.go` (package `main`) runs every verb against
`fakeapi.Handler()` over `net/http/httptest`, including the boundary cases
(an incomplete payload, a subject past its declared `maxLength`, an
`announce` publish) and the full lifecycle (publish, idempotent repeat,
status, read_back, read_metrics, list_owned, retract twice, status and
read_back once gone). `go run ./contract/cmd/conformance` against a
`fakeapi` server on the loopback, with `-fake-venue`, passes every check
the suite can run without a person (probe, the declined verbs, the action
surface's whole lifecycle, and that no credential leaks into a result).
