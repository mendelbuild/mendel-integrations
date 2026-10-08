package main

import (
	"encoding/json"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

const (
	wrapperVersion = "0.1.0"
	specSource     = "https://resend.com/docs/api-reference/emails"
	// specHash is the SHA-256 of the four pages in spec/resend/, concatenated
	// in file-name order (cat spec/resend/0[1-4]* | sha256sum).
	specHash = "sha256:09a6d8591209787991f686a13cd62e08e29df3b690265d732e5afc81db7fa8cf"

	kindDirectMessage = "direct_message"
	// Limits of the shape: a short check-in, to one person.
	maxSubject = 200
	maxBody    = 2000
)

// declined are the verbs this wrapper does not honour, each with its reason.
var declined = map[wp.Verb]string{
	wp.VerbAuthorize: "Resend is connected with an API key the project holds; it has no authorization flow for this wrapper to run.",
	wp.VerbDraft: "Resend has no unsent, held state for an email: a message is sent at once or scheduled, and a scheduled one " +
		"goes out by itself at its time, so nothing a draft could hold is 'not live until publish'. Use publish with when 'at' to schedule.",
	wp.VerbAppendUpdate: "A direct_message is not log-shaped; a sent email cannot be appended to.",
	wp.VerbRetract: "A direct_message cannot be recalled once sent. Resend can cancel only an email still scheduled, so retract " +
		"could not answer the same way for a message already sent; the contract allows a kind that cannot be recalled to decline it.",
	wp.VerbSetCap:     "A direct_message involves no spend to cap.",
	wp.VerbReadSeries: "This wrapper is not a data source: Resend's email endpoints give no counts over time to read.",
	wp.VerbReadTotal:  "This wrapper is not a data source: Resend's email endpoints give no counts over a window to read.",
	wp.VerbSearch:     "Resend's email endpoints have no search; the wrapper finds its own assets by name with list_owned instead.",
}

const directMessageShape = `{"type":"object","additionalProperties":false,"required":["recipient","subject","body"],` +
	`"properties":{` +
	`"recipient":{"type":"string","description":"One email address, bare (no display name, no list)."},` +
	`"subject":{"type":"string","minLength":1,"maxLength":200},` +
	`"body":{"type":"string","minLength":1,"maxLength":2000,"description":"Plain text, sent as the email's text part."}}}`

func manifest() *wp.CapabilityManifest {
	verbs := map[wp.Verb]wp.VerbSupport{}
	for _, v := range wp.ContractVerbs() {
		if why, ok := declined[v]; ok {
			verbs[v] = wp.VerbSupport{Level: wp.VerbDeclined, Reason: why}
		} else {
			verbs[v] = wp.VerbSupport{Level: wp.VerbSupported}
		}
	}
	verbs[wp.VerbPublish] = wp.VerbSupport{Level: wp.VerbPartial,
		Caveat: "Sends at once (when 'now') or schedules (when 'at'); 'announce' is refused, since a one-to-one message is not announced."}
	verbs[wp.VerbReadMetrics] = wp.VerbSupport{Level: wp.VerbPartial,
		Caveat: "Only 'delivered' is read, from the email's last event; opens, clicks and replies are declared unavailable."}
	verbs[wp.VerbListOwned] = wp.VerbSupport{Level: wp.VerbPartial,
		Caveat: "Resend keeps no name for an email, so the name is kept as a tag and found by reading each listed email: " +
			"the cost grows with the account's sending history."}

	why := "List Sent Emails and Retrieve Sent Email carry only the email's last event, a single latest state, so "
	return &wp.CapabilityManifest{
		Contract: wp.ContractVersion,
		Wrapper:  wp.WrapperProvenance{Version: wrapperVersion, SpecSource: specSource, SpecHash: specHash},
		Verbs:    verbs,
		Metrics: map[string]wp.MetricSupport{
			"delivered": {Level: wp.MetricAvailable, Kind: wp.KindCount, Aggregations: []string{"count"},
				Quality: []string{"from_last_event"}},
			"opened":  {Level: wp.MetricUnavailable, Reason: why + "an email opened and then clicked reads as clicked, and opens cannot be counted from it."},
			"clicked": {Level: wp.MetricUnavailable, Reason: why + "a click cannot be told from a later event, and clicks cannot be counted from it."},
			"replies": {Level: wp.MetricUnavailable, Reason: "A reply reaches Resend as a received email, which this wrapper does not read " +
				"(the Receiving endpoints were not among the documentation it was written against); no reply count is claimed, and never zero."},
		},
		Granularities:    []string{},
		FilterDimensions: []string{},
		Venue:            "read_only",
		Idempotency: "Send Email takes an Idempotency-Key header (from the publish call's idempotency_key); " +
			"a repeat with the same key answers the email already sent.",
		Entitlements: map[string]any{"api_key_can_read_sent_emails": true},
		StoragePolicy: "Resend's terms were not among the documentation read. The wrapper keeps nothing: it holds no state between runs, " +
			"and returns only the message the project itself approved and its delivery state. Recipient addresses are personal data; " +
			"keep them only as far as the recipient's consent to the check-in covers.",
		Kinds: map[string]wp.KindSupport{
			kindDirectMessage: {Level: wp.VerbSupported, Shape: json.RawMessage(directMessageShape)},
			"email_broadcast": {Level: wp.VerbDeclined, Reason: "This wrapper sends one message to one person with Send Email; Resend's Broadcasts are not wrapped."},
			"social_post":     {Level: wp.VerbDeclined, Reason: "Resend sends email; it has no social posts."},
			"listing":         {Level: wp.VerbDeclined, Reason: "Resend sends email; it has no listings."},
		},
	}
}
