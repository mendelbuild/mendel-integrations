package main

import (
	"encoding/json"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

const (
	wrapperVersion = "0.1.0"
	specSource     = "https://docs.ayrshare.com/rest-api/endpoints/post"
	// specHash is the SHA-256 of the three files under spec/ayrshare/, in name
	// order, concatenated. A test recomputes it.
	specHash = "000412f104926b9cb636a66e7247231909fb59ae2e52388d84bbf5009e0b99a9"

	kindSocialPost = "social_post"
	// maxText is this wrapper's bound on a post's text. The pages under spec/
	// give none for Reddit (Ayrshare validates a post before sending it); the
	// bound only keeps what is sent to something Ayrshare can be expected to
	// take, and lets the shape declare its boundary.
	maxText = 10000
)

// socialPostShape refines the social_post family (text, link, media).
var socialPostShape = json.RawMessage(`{
 "type": "object",
 "required": ["text", "link", "media"],
 "additionalProperties": false,
 "properties": {
  "text": {"type": "string", "minLength": 1, "maxLength": 10000,
           "description": "The post's text, in Reddit-flavoured Markdown (Ayrshare, 'Rich Text Posts')."},
  "link": {"type": ["string", "null"], "description": "An http(s) URL, placed at the end of the post's text."},
  "media": {"type": ["array", "null"], "minItems": 1,
            "items": {"type": "string", "description": "Image URL, JPG, PNG, GIF or WEBP, directly reachable (Reddit takes no video)."}}
 }
}`)

var declined = map[wp.Verb]string{
	wp.VerbAuthorize: "Ayrshare is connected with an API key a person pastes in; the pages this wrapper was written against " +
		"describe no authorization flow for it to run.",
	wp.VerbAppendUpdate: "A social post is not log-shaped, and Ayrshare's update a post (PATCH) edits a post rather than " +
		"appending to one, which is what append_update is not.",
	wp.VerbReadMetrics: "The pages this wrapper was written against describe no per-post analytics for a Reddit post: " +
		"the only Reddit analytics they mention (changelog, November 2022) are the account's karma, profile image and page URL. " +
		"Claiming a metric Ayrshare is not documented to give for the post would be a number made up.",
	wp.VerbSetCap: "A social post spends nothing a cap could bound: Ayrshare bills by plan, not by post.",
	wp.VerbReadSeries: "Ayrshare's analytics are not described by the pages this wrapper was written against, and the " +
		"history it reads lists posts, not a measure per step of a window.",
	wp.VerbReadTotal: "Ayrshare's analytics are not described by the pages this wrapper was written against, and the " +
		"history it reads lists posts, not a measure over a window.",
	wp.VerbSearch: "Ayrshare's history lists an account's own posts; it takes no query and no window, so it cannot answer " +
		"items matching a query within a window.",
}

func manifest() *wp.CapabilityManifest {
	verbs := map[wp.Verb]wp.VerbSupport{
		wp.VerbProbe:  {Level: wp.VerbSupported},
		wp.VerbDraft:  {Level: wp.VerbSupported},
		wp.VerbStatus: {Level: wp.VerbSupported},
		wp.VerbPublish: {Level: wp.VerbPartial, Caveat: "Publishes now or at an instant. An announced start is refused: " +
			"Ayrshare has no notice-then-start mode."},
		wp.VerbRetract:  {Level: wp.VerbSupported},
		wp.VerbReadBack: {Level: wp.VerbSupported},
		wp.VerbListOwned: {Level: wp.VerbPartial, Caveat: "Ayrshare keeps no name for a post, so Mendel's name is kept in the " +
			"post's notes, and the list is read from the latest 500 posts of the history (its largest answer): older posts " +
			"are not found."},
	}
	for v, why := range declined {
		verbs[v] = wp.VerbSupport{Level: wp.VerbDeclined, Reason: why}
	}
	return &wp.CapabilityManifest{
		Contract:         wp.ContractVersion,
		Wrapper:          wp.WrapperProvenance{Version: wrapperVersion, SpecSource: specSource, SpecHash: specHash},
		Verbs:            verbs,
		Metrics:          map[string]wp.MetricSupport{},
		Granularities:    []string{},
		FilterDimensions: []string{},
		Venue:            "reversible_writes",
		Idempotency: "Native: idempotencyKey on POST /post; Ayrshare refuses a reused key as a duplicate, whatever became of the " +
			"first post. The wrapper keeps the key in the post's notes and answers a repeat with the post it finds there " +
			"(within the latest 500 posts).",
		Entitlements: map[string]any{
			"platforms":      []string{"reddit"},
			"history_window": historyCount,
			"plan":           "not reported by the endpoints this wrapper calls",
		},
		StoragePolicy: "The pages this wrapper was written against state no terms on keeping or showing what is read. The wrapper " +
			"stores nothing; it reads back only the posts a project made through it.",
		Kinds: map[string]wp.KindSupport{
			kindSocialPost: {Level: wp.VerbSupported, Shape: socialPostShape},
		},
	}
}
