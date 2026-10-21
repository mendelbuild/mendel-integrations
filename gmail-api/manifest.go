package main

import (
	"encoding/json"
	"fmt"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

const (
	wrapperVersion = "0.1.0"

	// specSource is the reference the wrapper was written against.
	specSource = "https://developers.google.com/workspace/gmail/api/reference/rest"
	// specHash is the SHA-256 of the four pages in spec/gmail-api/ (fetched
	// 2026-10-21), concatenated in file-name order.
	specHash = "sha256:a6a0b33acd893398dff03b9b401732648201404f7a0027f1e2cb38832af79485"

	kindDirectMessage = "direct_message"
)

// directMessageShape is the shape of a direct_message here: the family's
// three fields, all required, nothing else, with the subject bounded.
var directMessageShape = json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "required": ["recipient", "subject", "body"],
  "additionalProperties": false,
  "properties": {
    "recipient": {"type": "string", "description": "One plain address, such as name@example.com: no display name, no list."},
    "subject": {"type": "string", "maxLength": %d, "description": "One line, with no control characters and no leading or trailing space."},
    "body": {"type": "string", "description": "Plain text, sent exactly as written."}
  }
}`, maxSubject))

// notDocumented is the reason for a verb the need asked for and the
// documentation does not show Gmail API doing.
func notDocumented(what string) string {
	return "the Gmail API documentation Gregor found (the REST reference, the users.drafts resource, the drafts guide " +
		"and the usage limits) does not show it " + what + "; its documentation was not found, " +
		"so this wrapper does not write it from memory"
}

func verbSupport() map[wp.Verb]wp.VerbSupport {
	declined := func(reason string) wp.VerbSupport { return wp.VerbSupport{Level: wp.VerbDeclined, Reason: reason} }
	return map[wp.Verb]wp.VerbSupport{
		wp.VerbProbe: {Level: wp.VerbSupported},
		wp.VerbDraft: {Level: wp.VerbSupported},
		wp.VerbPublish: {Level: wp.VerbPartial, Caveat: "Sends now, from a draft by its ref (drafts.send) or from a payload " +
			"(messages.send); a time to send at, or an announcement, is refused, since the documentation found shows no " +
			"scheduled send. Gmail API documents no idempotency key for sending, so publishing the same payload twice sends it twice."},
		wp.VerbReadBack: {Level: wp.VerbSupported},

		wp.VerbAuthorize: declined("this wrapper takes the OAuth 2.0 access token the project supplies as " + tokenCredential +
			"; the documentation found does not include Gmail's authorization pages, so the grant is not begun, completed, " +
			"refreshed or revoked here"),
		wp.VerbStatus: declined(notDocumented("reporting what is configured and in effect for a message") +
			" (draft or sent is shown only by a message's labels, which is not what this verb answers)"),
		wp.VerbAppendUpdate: declined("a direct message is not log-shaped: once sent it is not added to, and Gmail API offers " +
			"no way to append to a message"),
		wp.VerbRetract:     declined(notDocumented("taking a message back") + "; a message that has been sent cannot be recalled"),
		wp.VerbReadMetrics: declined(notDocumented("reading metrics of a message")),
		wp.VerbSetCap:      declined("a direct message involves no spend, so there is nothing to cap"),
		wp.VerbListOwned: declined(notDocumented("listing messages by a name Mendel gave them") +
			"; Gmail keeps no name for a message"),
		wp.VerbReadSeries: declined("this wrapper is not a data source: it reads no measure of the account over time"),
		wp.VerbReadTotal:  declined("this wrapper is not a data source: it reads no measure of the account"),
		wp.VerbSearch: declined(notDocumented("searching messages by a query") +
			" (messages.list is listed, without the query it takes)"),
	}
}

// manifest is probe's answer, once the credential has shown it reaches the
// account.
func manifest() *wp.CapabilityManifest {
	return &wp.CapabilityManifest{
		Contract: wp.ContractVersion,
		Wrapper:  wp.WrapperProvenance{Version: wrapperVersion, SpecSource: specSource, SpecHash: specHash},
		Verbs:    verbSupport(),
		Metrics:  map[string]wp.MetricSupport{},
		// Not a data source: nothing is read as a series.
		Granularities:    []string{},
		FilterDimensions: []string{},
		Venue:            "test_account",
		// Exactly "none": Gmail API documents no idempotency key (see publish's caveat).
		Idempotency: "none",
		Entitlements: map[string]any{
			"quota_units_per_minute_per_user":    6000,
			"quota_units_per_minute_per_project": 1200000,
			"quota_units_per_day_per_project":    80000000,
			"quota_units_messages_send":          100,
			"quota_units_drafts_create":          10,
			"quota_units_drafts_send":            100,
			"quota_source":                       "https://developers.google.com/workspace/gmail/api/reference/quota, fetched 2026-10-21",
		},
		StoragePolicy: "The Gmail API pages fetched on 2026-10-21 state no terms on keeping or showing what is read from a " +
			"mailbox, so none is claimed. This wrapper keeps nothing between runs and reads back only the one message " +
			"Mendel sent, as recipient, subject and body.",
		Kinds: map[string]wp.KindSupport{
			kindDirectMessage: {Level: wp.VerbSupported, Shape: directMessageShape},
		},
	}
}
