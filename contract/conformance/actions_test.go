package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

// publisher is a small wrapper for a publishing tool, written against the
// draft, with switches that each break one rule of the action surface.
type publisher struct {
	posts    map[string]json.RawMessage
	byKey    map[string]string
	next     int
	retracts int

	ignoresKey       bool // a repeat with the same key posts again
	altersReadBack   bool // reads back something other than what was posted
	postsTooLong     bool // accepts text past the shape's maxLength
	retractTwiceFail bool // a second retract fails
	liveAfterRetract bool // status stays live after retracting
	readsImpressions bool // reads an unavailable metric as zero
	stateless        bool // begin's URL does not carry the state
	tokenInField     bool // puts the token in a field other than credentials

	lists        bool // honours list_owned, by name
	listsRecent  bool // honours list_owned, and lists everything whatever the prefix
	drafts       bool // honours draft
	draftIsLive  bool // honours draft, and a draft is live the moment it is made
	claimsAppend bool // claims append_update for a social post
	claimsSetCap bool // claims set_cap for a social post

	names       map[string]string
	draftsHeld map[string]json.RawMessage

	loopbackURL bool           // begin answers an http URL on the loopback, as a fake venue does
	calls       map[string]int // every call made, by verb and step
}

func (p *publisher) manifest() *wp.CapabilityManifest {
	m := &wp.CapabilityManifest{
		Contract: wp.ContractVersion,
		Wrapper:  wp.WrapperProvenance{Version: "1.0", SpecSource: "https://pub.example/api", SpecHash: "sha256:x"},
		Verbs:    map[wp.Verb]wp.VerbSupport{},
		Metrics: map[string]wp.MetricSupport{
			"likes":       {Level: wp.MetricAvailable, Kind: wp.KindCount, Aggregations: []string{"count"}, Quality: []string{"lifetime"}},
			"impressions": {Level: wp.MetricUnavailable, Reason: "not counted"},
		},
		Venue: "reversible_writes", Idempotency: "native key", Entitlements: map[string]any{},
		StoragePolicy: "The author owns it.",
		Authorization: &wp.AuthorizationSupport{Steps: []string{wp.AuthorizeBegin, wp.AuthorizeComplete, wp.AuthorizeRevoke},
			Scopes: []string{"write"}, Expires: "never"},
		Kinds: map[string]wp.KindSupport{"social_post": {Level: wp.VerbSupported,
			Shape: json.RawMessage(`{"type":"object","required":["text"],"properties":{"text":{"type":"string","maxLength":40},"link":{"type":"string"}}}`)}},
	}
	for _, v := range wp.ContractVerbs() {
		m.Verbs[v] = wp.VerbSupport{Level: wp.VerbDeclined, Reason: "not here"}
	}
	for _, v := range []wp.Verb{wp.VerbProbe, wp.VerbAuthorize, wp.VerbPublish, wp.VerbStatus, wp.VerbReadBack,
		wp.VerbReadMetrics, wp.VerbRetract} {
		m.Verbs[v] = wp.VerbSupport{Level: wp.VerbSupported}
	}
	if p.lists || p.listsRecent {
		m.Verbs[wp.VerbListOwned] = wp.VerbSupport{Level: wp.VerbSupported}
	}
	if p.drafts || p.draftIsLive {
		m.Verbs[wp.VerbDraft] = wp.VerbSupport{Level: wp.VerbSupported}
	}
	if p.claimsAppend {
		m.Verbs[wp.VerbAppendUpdate] = wp.VerbSupport{Level: wp.VerbPartial, Caveat: "edits the text"}
	}
	if p.claimsSetCap {
		m.Verbs[wp.VerbSetCap] = wp.VerbSupport{Level: wp.VerbSupported}
	}
	return m
}

func (p *publisher) run(_ context.Context, req wp.WrapperRequest) (wp.WrapperResponse, string, error) {
	var out wp.WrapperResponse
	if req.Contract != wp.ContractVersion {
		out.Results = append(out.Results, wp.VerbResult{Verb: req.Calls[0].Verb, Refused: "draft only"})
		return out, "", nil
	}
	for _, c := range req.Calls {
		if p.calls != nil {
			p.calls[string(c.Verb)+" "+c.Step]++
		}
		r := p.answer(req.Connection, c)
		out.Results = append(out.Results, r)
		if !r.Succeeded() {
			break
		}
	}
	return out, "", nil
}

func (p *publisher) answer(conn wp.Connection, c wp.VerbCall) wp.VerbResult {
	r := wp.VerbResult{Verb: c.Verb}
	m := p.manifest()
	support, known := m.Verbs[c.Verb]
	switch {
	case !known:
		r.Refused = "not a verb"
		return r
	case support.Level == wp.VerbDeclined:
		r.Refused = "declared absent"
		return r
	}
	switch c.Verb {
	case wp.VerbProbe:
		r.Manifest = m
	case wp.VerbAuthorize:
		switch c.Step {
		case wp.AuthorizeBegin:
			r.AuthorizeURL = "https://pub.example/oauth?client_id=" + conn.Credentials["PUB_CLIENT"] + "&state=" + c.State
			if p.loopbackURL {
				r.AuthorizeURL = "http://127.0.0.1:8123/oauth?state=" + c.State
			}
			if p.stateless {
				r.AuthorizeURL = "https://pub.example/oauth"
			}
			if p.tokenInField {
				r.AuthorizeURL += "&leak=" + conn.Credentials["PUB_TOKEN"]
			}
		case wp.AuthorizeRevoke:
		default:
			r.Refused = "no step " + c.Step
		}
	case wp.VerbListOwned:
		r.Owned = []string{}
		for ref, name := range p.names {
			if _, live := p.posts[ref]; live && (p.listsRecent || strings.HasPrefix(name, c.Prefix)) {
				r.Owned = append(r.Owned, ref)
			}
		}
	case wp.VerbDraft:
		p.next++
		id := fmt.Sprint(p.next)
		p.names[id] = c.Name
		if p.draftIsLive {
			p.posts[id] = c.Payload // live at once: the mutant
		} else {
			p.draftsHeld[id] = c.Payload
		}
		r.Ref = id
	case wp.VerbPublish:
		if c.Ref != "" {
			if body, ok := p.draftsHeld[c.Ref]; ok {
				p.posts[c.Ref] = body
				delete(p.draftsHeld, c.Ref)
			} else if _, live := p.posts[c.Ref]; !live {
				r.Refused = "no such draft"
				return r
			}
			r.Ref = c.Ref
			return r
		}
		var post map[string]any
		json.Unmarshal(c.Payload, &post)
		text, _ := post["text"].(string)
		switch {
		case text == "":
			r.Refused = "needs text"
			return r
		case len(text) > 40 && !p.postsTooLong:
			r.Refused = "too long"
			return r
		}
		if id, ok := p.byKey[c.IdempotencyKey]; ok && !p.ignoresKey {
			r.Ref = id
			return r
		}
		p.next++
		id := fmt.Sprint(p.next)
		p.posts[id], p.byKey[c.IdempotencyKey], p.names[id] = c.Payload, id, c.Name
		r.Ref, r.URL = id, "https://pub.example/"+id
	case wp.VerbStatus:
		if _, held := p.draftsHeld[c.Ref]; held {
			r.Status = &wp.AssetStatus{Configured: "draft", Effective: wp.EffectiveNotLive}
		} else if _, ok := p.posts[c.Ref]; ok || p.liveAfterRetract {
			r.Status = &wp.AssetStatus{Configured: "public", Effective: wp.EffectiveLive}
		} else {
			r.Status = &wp.AssetStatus{Configured: "deleted", Effective: wp.EffectiveGone}
		}
	case wp.VerbReadBack:
		body, ok := p.posts[c.Ref]
		if !ok {
			r.Refused = "gone"
			return r
		}
		if p.altersReadBack {
			body = json.RawMessage(strings.Replace(string(body), "Mendel", "mendel", 1))
		}
		r.Asset = body
	case wp.VerbReadMetrics:
		if _, ok := p.posts[c.Ref]; !ok {
			r.Refused = "gone"
			return r
		}
		three, zero := 3.0, 0.0
		r.AssetMetrics = map[string]wp.FieldReading{"likes": {Value: &three, Quality: []string{"lifetime"}},
			"impressions": {Unavailable: "not counted"}}
		if p.readsImpressions {
			r.AssetMetrics["impressions"] = wp.FieldReading{Value: &zero}
		}
	case wp.VerbRetract:
		p.retracts++
		if _, ok := p.posts[c.Ref]; !ok && p.retractTwiceFail {
			r.Failed = "not found"
			return r
		}
		delete(p.posts, c.Ref)
		delete(p.draftsHeld, c.Ref)
		r.Retracted = wp.RetractedDeleted
	}
	return r
}

func runPublisher(p *publisher) Report { return runPublisherAs(p, func(*Target) {}) }

func runPublisherAs(p *publisher, as func(*Target)) Report {
	p.posts, p.byKey, p.names, p.draftsHeld = map[string]json.RawMessage{}, map[string]string{}, map[string]string{},
		map[string]json.RawMessage{}
	d := wp.Description{Tool: wp.Tool{Slug: "pub", Name: "Pub"}, Version: "1.0", Contract: wp.ContractVersion,
		Image: "pub:dev", Command: []string{"/pub"}, SpecSource: "https://pub.example/api",
		Connection: wp.ConnectionSpec{Account: &wp.Field{Label: "Account"}, Authorize: true,
			Credentials: []wp.Field{{Name: "PUB_TOKEN", Label: "Token"}}},
		Claims: map[string]string{"authorize": "supported", "publish": "supported", "retract": "supported"}}
	d.Connection.Credentials = append(d.Connection.Credentials, wp.Field{Name: "PUB_CLIENT", Label: "Client", Public: true})
	t := Target{Description: d, Run: p.run,
		Connection: wp.Connection{AccountID: "pub.example", Credentials: map[string]string{
			"PUB_TOKEN": "pub-token-SECRET", "PUB_CLIENT": "pub-client-ID"}}}
	as(&t)
	return Run(context.Background(), t, now)
}

func TestAPublisherThatKeepsTheDraftPassesAndLeavesNothingBehind(t *testing.T) {
	p := &publisher{}
	r := runPublisher(p)
	for _, c := range r.Checks {
		if c.Outcome == Fail || c.Outcome == Untested {
			t.Errorf("%s (%s): %s: %s", c.Name, c.Verb, c.Outcome, c.Detail)
		}
	}
	if len(p.posts) != 0 {
		t.Errorf("the run left %d post(s) published", len(p.posts))
	}
	// The one thing it cannot do unattended, complete, and revoke (not asked
	// for) are warnings.
	if r.Count()[Warn] != 2 {
		t.Errorf("warnings: %d", r.Count()[Warn])
	}
}

func TestEveryPublisherMutantIsCaught(t *testing.T) {
	for name, tc := range map[string]struct {
		p     *publisher
		check string
	}{
		"ignores the idempotency key": {&publisher{ignoresKey: true}, "same key is the same asset"},
		"alters what it reads back":   {&publisher{altersReadBack: true}, "equals what was approved"},
		"posts past its maxLength":    {&publisher{postsTooLong: true}, "past its declared maxLength"},
		"fails a second retract":      {&publisher{retractTwiceFail: true}, "safe twice"},
		"live after retract":          {&publisher{liveAfterRetract: true}, "retracted asset's status"},
		"reads an unavailable metric": {&publisher{readsImpressions: true}, "answers every metric"},
		"drops the state":             {&publisher{stateless: true}, "carries Mendel's state"},
		"puts the token in a field":   {&publisher{tokenInField: true}, "no credential appears"},
		"lists whatever the prefix":   {&publisher{listsRecent: true}, "answers by the prefix"},
		"claims append for a post":    {&publisher{claimsAppend: true}, "claimed only for them"},
		"claims a cap for a post":     {&publisher{claimsSetCap: true}, "claimed only for them"},
		"a draft that is live":        {&publisher{draftIsLive: true}, "not live until published"},
	} {
		r := runPublisher(tc.p)
		caught := false
		for _, c := range r.Checks {
			if c.Outcome == Fail && strings.Contains(c.Name, tc.check) {
				caught = true
			}
		}
		if !caught {
			var not []string
			for _, c := range r.Checks {
				if c.Outcome != Pass {
					not = append(not, c.Name+": "+string(c.Outcome))
				}
			}
			t.Errorf("%s: not caught by %q; what did not pass: %v", name, tc.check, not)
		}
		if len(tc.p.posts) != 0 {
			t.Errorf("%s: the run left %d post(s) published", name, len(tc.p.posts))
		}
	}
}

// A publisher that honours list_owned by name and a draft that is not live
// passes those checks, and leaves nothing behind.
func TestAPublisherThatListsAndDraftsHonestlyPasses(t *testing.T) {
	p := &publisher{lists: true, drafts: true}
	r := runPublisher(p)
	for _, c := range r.Checks {
		if c.Outcome == Fail || c.Outcome == Untested {
			t.Errorf("%s (%s): %s: %s", c.Name, c.Verb, c.Outcome, c.Detail)
		}
	}
	if len(p.posts) != 0 || len(p.draftsHeld) != 0 {
		t.Errorf("left %d posts and %d drafts", len(p.posts), len(p.draftsHeld))
	}
}
