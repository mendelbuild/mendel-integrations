package main

import (
	"encoding/json"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

// wrapperVersion, specSource and specHash must be what wrapper.json and the
// manifest both say (GUIDE.md "wrapper.json": "verification refuses an
// image that answers as another version or contract"). specHash is the
// sha256 of the fetched spec documents this wrapper was written against
// (mailchimp/README.md lists them, with the date).
const (
	wrapperVersion = "0.1.2"
	specSource     = "https://mailchimp.com/developer/marketing/api/campaigns/"
	specHash       = "8e0bae0b30f1a438e77c1c44d44a269f49424fc3c6e36f48eecf1fb3e61a44d1"
)

// emailBroadcastShape refines the email_broadcast family for this wrapper:
// a subject line and preheader Mailchimp shows before a recipient opens
// the email, and the body in both forms a Mailchimp campaign's content
// takes (Campaigns > Content: "Manage the HTML, plain-text ... content").
const emailBroadcastShape = `{
  "type": "object",
  "properties": {
    "subject": {"type": "string", "maxLength": 150},
    "preheader": {"type": "string", "maxLength": 130},
    "body": {
      "type": "object",
      "properties": {
        "html": {"type": "string"},
        "text": {"type": "string"}
      },
      "required": ["html", "text"]
    }
  },
  "required": ["subject", "preheader", "body"]
}`

// buildManifest is probe's answer, once the credential has been shown to
// read the named audience. entitlements is what that read found.
func buildManifest(entitlements map[string]any) *wp.CapabilityManifest {
	supported := wp.VerbSupport{Level: wp.VerbSupported}
	declined := func(reason string) wp.VerbSupport { return wp.VerbSupport{Level: wp.VerbDeclined, Reason: reason} }

	return &wp.CapabilityManifest{
		Contract: wp.ContractVersion,
		Wrapper: wp.WrapperProvenance{
			Version:    wrapperVersion,
			SpecSource: specSource,
			SpecHash:   specHash,
		},
		Verbs: map[wp.Verb]wp.VerbSupport{
			wp.VerbProbe: supported,
			wp.VerbAuthorize: declined("Mailchimp is connected with an account API key (Fundamentals: \"You should " +
				"use an API key if you're writing code that tightly couples your application data to your Mailchimp " +
				"account data\"); this wrapper drives no OAuth redirect, so it has no begin, complete, refresh or " +
				"revoke to answer."),
			wp.VerbDraft: supported,
			wp.VerbPublish: {Level: wp.VerbPartial, Caveat: "`now` sends the campaign immediately (Send campaign) " +
				"and `at` schedules it for an instant (Schedule campaign); Mailchimp has no announced or " +
				"preview-before-send mode, so `announce` is refused."},
			wp.VerbStatus: supported,
			wp.VerbAppendUpdate: declined("email_broadcast is not log-shaped: sending again creates a new campaign " +
				"rather than appending an update to one already sent."),
			wp.VerbRetract:     supported,
			wp.VerbReadBack:    supported,
			wp.VerbReadMetrics: supported,
			wp.VerbSetCap: declined("a Mailchimp campaign has no spend to cap; sending to an owned audience is " +
				"covered by the account's own plan, not billed per campaign."),
			wp.VerbListOwned: supported,
			wp.VerbReadSeries: declined("this wrapper reads a campaign's own metrics (read_metrics); it is not a " +
				"data source over account-wide events, so it has no series to read."),
			wp.VerbReadTotal: declined("this wrapper reads a campaign's own metrics (read_metrics); it is not a " +
				"data source over account-wide events, so it has no total to read."),
			wp.VerbSearch: declined("Mailchimp's Search Campaigns endpoint matches an account's own campaigns for " +
				"someone browsing them; it answers nothing in the article family search reads back."),
		},
		Metrics: map[string]wp.MetricSupport{
			"opens": {Level: wp.MetricAvailable, Kind: wp.KindCount, Aggregations: []string{"count", "unique"},
				Uniqueness: "a distinct recipient who opened the campaign at least once (Mailchimp's unique_opens)"},
			"clicks": {Level: wp.MetricAvailable, Kind: wp.KindCount, Aggregations: []string{"count", "unique"},
				Uniqueness: "a distinct recipient who clicked a link in the campaign at least once (Mailchimp's " +
					"unique_subscriber_clicks)"},
			"bounces": {Level: wp.MetricAvailable, Kind: wp.KindCount, Aggregations: []string{"count"}},
		},
		Kinds: map[string]wp.KindSupport{
			kindEmailBroadcast: {Level: wp.VerbSupported, Shape: json.RawMessage(emailBroadcastShape)},
		},
		Venue: "test_account",
		Idempotency: "a repeat publish with the same idempotency key is answered against the campaign already " +
			"created for it under the target audience (found by the key kept in the campaign's own internal " +
			"title), rather than sending a second wave.",
		Entitlements: entitlements,
		StoragePolicy: "Mailchimp's Data Processing Addendum (part of its Standard Terms of Use, " +
			"https://www.intuit.com/legal/terms/en-us/mailchimp/dpa/) governs what Mailchimp itself retains about " +
			"the audience and the campaigns sent to it; this wrapper stores nothing of what it reads beyond the " +
			"one run that read it.",
	}
}
