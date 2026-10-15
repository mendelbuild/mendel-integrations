package main

import (
	"encoding/json"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

const (
	wrapperVersion = "0.1.0"
	specSource     = "https://documentation.mailgun.com/docs/mailgun/api-reference/send/mailgun/messages"
	// specHash is the SHA-256 of the three pages in spec/mailgun, concatenated
	// in file order, as fetched on 2026-10-08.
	specHash = "sha256:fe81401604714a5eec7e71e72975f35e0d502001d7eed8344db21bea3d19e12b"
)

// declined is every verb this wrapper does not do, with why: the reason is
// the answer to the verb and the manifest's reason for it.
var declined = map[wp.Verb]string{
	wp.VerbAuthorize: "Mailgun is connected with an API key the project supplies, sent as HTTP basic auth; the documentation " +
		"this wrapper was written against describes no authorization flow to begin, complete or refresh.",
	wp.VerbDraft: "Mailgun has no unsent state for a message: the Send an email call queues it at once. The only way to hold " +
		"one back is o:deliverytime, which commits it to being sent and cannot be changed or cancelled singly afterwards, " +
		"so it would not be 'not live until publish'.",
	wp.VerbStatus: "Mailgun answers a send only with the message's id and 'Queued'; the pages this wrapper was written " +
		"against describe no call that says what became of a message, so it cannot say what is in effect.",
	wp.VerbAppendUpdate: "a direct_message is not log-shaped: it is one message to one person, and there is no update to append to it.",
	wp.VerbRetract: "a message Mailgun has sent cannot be recalled, and its only delete, 'Delete scheduled and undelivered " +
		"mail', empties the whole domain's queue rather than one message, so it would take back every other message " +
		"waiting on the domain.",
	wp.VerbReadBack: "Mailgun's send answers only an id. Retrieving a stored message needs a storage key that arrives through " +
		"events, not from the send, and that call is not described in the pages this wrapper was written against.",
	wp.VerbReadMetrics: "per-message delivery, open and reply counts come from Mailgun's events and stats, which are not " +
		"described in the pages this wrapper was written against; no number is given rather than a made-up one.",
	wp.VerbSetCap: "a direct_message involves no spend that an asset can be capped at; Mailgun bills by the message sent.",
	wp.VerbListOwned: "Mailgun keeps no name for a message: the wrapper can attach Mendel's name as custom data, but listing " +
		"messages needs Mailgun's events, which are not described in the pages this wrapper was written against.",
	wp.VerbReadSeries: "this wrapper is not a data source: it reads no measure from Mailgun.",
	wp.VerbReadTotal:  "this wrapper is not a data source: it reads no measure from Mailgun.",
	wp.VerbSearch:     "Mailgun holds no articles or items to search; the stored messages it keeps are not searchable by the calls this wrapper uses.",
}

// directMessageShape refines the direct_message family: one recipient, a
// subject and a body of plain text.
const directMessageShape = `{
  "type": "object",
  "required": ["recipient", "subject", "body"],
  "additionalProperties": false,
  "properties": {
    "recipient": {"type": "string", "format": "email",
      "description": "One email address, without a name: a direct message is one-to-one."},
    "subject": {"type": "string", "minLength": 1, "maxLength": 200},
    "body": {"type": "string", "minLength": 1,
      "description": "Plain text, sent as Mailgun's text part, with no tracking."}
  }
}`

func manifest() *wp.CapabilityManifest {
	verbs := map[wp.Verb]wp.VerbSupport{
		wp.VerbProbe:   {Level: wp.VerbSupported},
		wp.VerbPublish: {Level: wp.VerbSupported},
	}
	for v, reason := range declined {
		verbs[v] = wp.VerbSupport{Level: wp.VerbDeclined, Reason: reason}
	}
	return &wp.CapabilityManifest{
		Contract:         wp.ContractVersion,
		Wrapper:          wp.WrapperProvenance{Version: wrapperVersion, SpecSource: specSource, SpecHash: specHash},
		Verbs:            verbs,
		Metrics:          map[string]wp.MetricSupport{},
		Granularities:    []string{},
		FilterDimensions: []string{},
		// What a run can prove is that the key sends in test mode; a message
		// sent for real cannot be taken back.
		Venue: "nothing_safe",
		// Mailgun's send takes no idempotency key, so a repeated publish sends
		// the message again; Mendel's key is kept only as custom data.
		Idempotency: "none",
		Entitlements: map[string]any{
			"send":     "accepted in test mode for this domain",
			"schedule": "o:deliverytime up to 3 or 7 days ahead, by plan; this wrapper refuses beyond 7 (plan not observed)",
		},
		StoragePolicy: "This wrapper reads nothing back from Mailgun and keeps nothing: of what it sends, only the message " +
			"id Mailgun answers is returned. Mailgun's terms on keeping and showing event data are not in the " +
			"documentation it was written against (" + specSource + ").",
		Kinds: map[string]wp.KindSupport{
			kindDirectMessage: {
				Level: wp.VerbPartial,
				Caveat: "sent for good, to one address, as plain text with no tracking; it can be scheduled at most 7 days ahead, " +
					"cannot be recalled once sent, and a repeated publish with the same idempotency key sends it again",
				Shape: json.RawMessage(directMessageShape),
			},
		},
	}
}
