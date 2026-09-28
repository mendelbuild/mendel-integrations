package conformance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// The draft's checks: authorize, the action surface as one lifecycle, and
// that no credential leaks out of the one field that may carry it.

func nonce() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// --- authorize ---

// authorize checks the steps that need no person: begin answers a URL that
// carries Mendel's state, refresh (where declared) answers new credentials,
// and revoke runs only when the target asks, since it ends the grant.
func (s *suite) authorize(ctx context.Context, m *wp.CapabilityManifest) {
	a := m.Authorization
	steps := map[string]bool{}
	for _, st := range a.Steps {
		steps[st] = true
	}

	c := Check{Name: "authorize begin answers a URL that carries Mendel's state", Verb: wp.VerbAuthorize, Calls: 1}
	state := "conformance-" + nonce()
	res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: wp.VerbAuthorize, Step: wp.AuthorizeBegin,
		RedirectURI: wp.OutOfBandRedirect, State: state})
	c.Elapsed = elapsed
	switch {
	case err != nil:
		c.Outcome, c.Detail = Fail, err.Error()
	case !res.Succeeded():
		c.Outcome, c.Detail = Fail, res.Why()
	default:
		u, perr := url.Parse(res.AuthorizeURL)
		switch {
		case perr != nil || u.Scheme != "https" || u.Host == "":
			c.Outcome, c.Detail = Fail, fmt.Sprintf("%q is not an https URL a person could open", res.AuthorizeURL)
		case !strings.Contains(res.AuthorizeURL, url.QueryEscape(state)) && !strings.Contains(res.AuthorizeURL, state):
			c.Outcome, c.Detail = Fail, "the URL does not carry the state Mendel sent, so the redirect could not be matched to it"
		default:
			c.Outcome, c.Detail = Pass, fmt.Sprintf("asks for %s", strings.Join(a.Scopes, " "))
		}
	}
	s.add(c)

	if !steps[wp.AuthorizeRefresh] {
		c := Check{Name: "a refresh not declared is refused", Verb: wp.VerbAuthorize, Calls: 1}
		res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: wp.VerbAuthorize, Step: wp.AuthorizeRefresh})
		c.Elapsed = elapsed
		switch {
		case err != nil:
			c.Outcome, c.Detail = Fail, err.Error()
		case res.Refused == "":
			c.Outcome, c.Detail = Fail, "refresh is not among the declared steps, and was not refused"
		default:
			c.Outcome, c.Detail = Pass, res.Refused
		}
		s.add(c)
	} else {
		c := Check{Name: "authorize refresh answers new credentials", Verb: wp.VerbAuthorize, Calls: 1}
		res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: wp.VerbAuthorize, Step: wp.AuthorizeRefresh})
		c.Elapsed = elapsed
		switch {
		case err != nil:
			c.Outcome, c.Detail = Fail, err.Error()
		case !res.Succeeded():
			c.Outcome, c.Detail = Fail, res.Why()
		case len(res.Credentials) == 0:
			c.Outcome, c.Detail = Fail, "refresh answered no credentials"
		default:
			c.Outcome = Pass
			for k, v := range res.Credentials {
				s.t.Connection.Credentials[k] = v
			}
		}
		s.add(c)
	}

	c = Check{Name: "authorize complete exchanges a code for credentials", Verb: wp.VerbAuthorize, Outcome: Warn,
		Detail: "needs a person to approve in a browser; run `conformance authorize`, which this run's credentials came from"}
	if len(s.t.Connection.Credentials) == 0 {
		c.Outcome = Untested
	}
	s.add(c)
}

// revokeAtEnd runs revoke when the target asked for it, and says it did not
// otherwise.
func (s *suite) revokeAtEnd(ctx context.Context, m *wp.CapabilityManifest) {
	if m.Verbs[wp.VerbAuthorize].Level == wp.VerbDeclined || m.Authorization == nil {
		return
	}
	declared := false
	for _, st := range m.Authorization.Steps {
		declared = declared || st == wp.AuthorizeRevoke
	}
	c := Check{Name: "authorize revoke ends the grant, and is safe twice", Verb: wp.VerbAuthorize}
	switch {
	case !declared:
		c.Outcome, c.Detail = Warn, "revoke is not declared, so disconnecting cannot end the grant at the tool"
	case !s.t.Revoke:
		c.Outcome, c.Detail = Warn, "not run: it ends the venue's grant (run with -revoke to exercise it)"
	default:
		c.Calls = 2
		res, elapsed, err := s.call(ctx, wp.VerbCall{Verb: wp.VerbAuthorize, Step: wp.AuthorizeRevoke},
			wp.VerbCall{Verb: wp.VerbAuthorize, Step: wp.AuthorizeRevoke})
		c.Elapsed = elapsed
		switch {
		case err != nil:
			c.Outcome, c.Detail = Fail, err.Error()
		case len(res) < 2 || !res[1].Succeeded():
			c.Outcome, c.Detail = Fail, res[len(res)-1].Why()
		default:
			c.Outcome = Pass
		}
	}
	s.add(c)
}

// --- the action surface ---

// payloads the suite knows how to write, per External Asset Kind: a valid
// asset, and the boundary cases §8 asks for. A kind the suite has no row for
// is reported untested.
type payloads struct {
	// valid is a valid asset that fits max, the shape's maxLength for
	// lengthOf, or any length when the shape declares none (0).
	valid      func(max int) map[string]any
	tooLong    func(max int) map[string]any
	incomplete map[string]any
	lengthOf   string // the field whose maxLength the shape declares
}

// fit cuts s to max characters, when there is a max.
func fit(s string, max int) string {
	if r := []rune(s); max > 0 && len(r) > max {
		return string(r[:max])
	}
	return s
}

func knownKinds() map[string]payloads {
	return map[string]payloads{
		"social_post": {
			valid: func(max int) map[string]any {
				return map[string]any{"text": fit("Mendel conformance check "+nonce()+". This post is deleted within the minute.", max),
					"link": "https://mendel.build/?conformance=" + nonce()}
			},
			tooLong:    func(max int) map[string]any { return map[string]any{"text": strings.Repeat("x", max+1)} },
			incomplete: map[string]any{"link": "https://mendel.build/"},
			lengthOf:   "text",
		},
	}
}

// maxLength reads a string field's maxLength from a kind's shape, or 0.
func maxLength(shape json.RawMessage, field string) int {
	var sch struct {
		Properties map[string]struct {
			MaxLength int `json:"maxLength"`
		} `json:"properties"`
	}
	if json.Unmarshal(shape, &sch) != nil {
		return 0
	}
	return sch.Properties[field].MaxLength
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// publishLifecycle publishes one asset of each kind it can, and walks it
// through every honoured verb to retraction. It publishes only what it can
// retract: §8's venue rule, that what cannot be undone is not automated.
func (s *suite) publishLifecycle(ctx context.Context, m *wp.CapabilityManifest) {
	honours := func(v wp.Verb) bool { return m.Verbs[v].Level != wp.VerbDeclined }
	if !honours(wp.VerbRetract) {
		s.add(Check{Name: "an asset is published, read back and retracted", Verb: wp.VerbPublish, Outcome: Untested,
			Detail: "retract is declared absent, and the suite does not publish what it cannot take back"})
		return
	}
	kinds := make([]string, 0, len(m.Kinds))
	for k := range m.Kinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	known := knownKinds()
	for _, kind := range kinds {
		ks := m.Kinds[kind]
		if ks.Level == wp.VerbDeclined {
			continue
		}
		p, ok := known[kind]
		if !ok {
			s.add(Check{Name: "an asset is published, read back and retracted", Verb: wp.VerbPublish, Outcome: Untested,
				Detail: fmt.Sprintf("the suite has no payloads for kind %s yet", kind)})
			continue
		}
		s.boundaries(ctx, kind, ks, p)
		s.lifecycle(ctx, m, kind, p.valid(maxLength(ks.Shape, p.lengthOf)))
	}
}

// boundaries checks that what the shape does not allow is refused, and posts
// nothing.
func (s *suite) boundaries(ctx context.Context, kind string, ks wp.KindSupport, p payloads) {
	cases := []struct {
		name    string
		payload map[string]any
	}{{"a payload missing a required field is refused", p.incomplete}}
	if max := maxLength(ks.Shape, p.lengthOf); max > 0 {
		cases = append(cases, struct {
			name    string
			payload map[string]any
		}{fmt.Sprintf("a %s one past its declared maxLength (%d) is refused", p.lengthOf, max), p.tooLong(max)})
	} else {
		s.add(Check{Name: "the shape declares its boundaries", Verb: wp.VerbPublish, Outcome: Warn,
			Detail: fmt.Sprintf("kind %s's shape declares no maxLength for %s, so its boundary cannot be exercised", kind, p.lengthOf)})
	}
	for _, tc := range cases {
		c := Check{Name: tc.name, Verb: wp.VerbPublish, Calls: 1}
		res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: wp.VerbPublish, AssetKind: kind, Payload: raw(tc.payload),
			When: &wp.When{Mode: wp.WhenNow}, IdempotencyKey: "conformance-" + nonce()})
		c.Elapsed = elapsed
		switch {
		case err != nil:
			c.Outcome, c.Detail = Fail, err.Error()
		case res.Succeeded():
			c.Outcome, c.Detail = Fail, "it was published as "+res.Ref
			// Take it back: the suite never leaves anything behind.
			_, _, _ = s.one(ctx, wp.VerbCall{Verb: wp.VerbRetract, Ref: res.Ref})
		case res.Refused == "":
			c.Outcome, c.Detail = Fail, "it failed rather than being refused: "+res.Failed
		default:
			c.Outcome, c.Detail = Pass, res.Refused
		}
		s.add(c)
	}
}

// lifecycle publishes one valid asset and takes it through status, read_back,
// read_metrics and retract (twice), whatever fails along the way: once
// something is published, retracting it is not optional.
func (s *suite) lifecycle(ctx context.Context, m *wp.CapabilityManifest, kind string, valid map[string]any) {
	honours := func(v wp.Verb) bool { return m.Verbs[v].Level != wp.VerbDeclined }
	key := "conformance-" + nonce()
	pub := wp.VerbCall{Verb: wp.VerbPublish, AssetKind: kind, Payload: raw(valid), When: &wp.When{Mode: wp.WhenNow},
		IdempotencyKey: key}

	c := Check{Name: "a valid asset is published", Verb: wp.VerbPublish, Calls: 1}
	res, elapsed, err := s.one(ctx, pub)
	c.Elapsed = elapsed
	switch {
	case err != nil:
		c.Outcome, c.Detail = Fail, err.Error()
	case !res.Succeeded():
		c.Outcome, c.Detail = Fail, res.Why()
	case res.Ref == "":
		c.Outcome, c.Detail = Fail, "published without a ref, so nothing could read it back or take it back"
	default:
		c.Outcome, c.Detail = Pass, fmt.Sprintf("%s %s", kind, res.URL)
	}
	s.add(c)
	if c.Outcome != Pass {
		return
	}
	ref := res.Ref
	refs := map[string]bool{ref: true}
	defer func() {
		// Whatever happened above, nothing is left published.
		for r := range refs {
			if r != ref {
				_, _, _ = s.one(ctx, wp.VerbCall{Verb: wp.VerbRetract, Ref: r})
			}
		}
	}()

	if strings.TrimSpace(m.Idempotency) != "" && m.Idempotency != "none" {
		c := Check{Name: "publishing again with the same key is the same asset", Verb: wp.VerbPublish, Calls: 1}
		res, elapsed, err := s.one(ctx, pub)
		c.Elapsed = elapsed
		switch {
		case err != nil:
			c.Outcome, c.Detail = Fail, err.Error()
		case !res.Succeeded():
			c.Outcome, c.Detail = Fail, res.Why()
		case res.Ref != ref:
			refs[res.Ref] = true
			c.Outcome, c.Detail = Fail, fmt.Sprintf("the manifest declares idempotency (%s), and a repeat made %s beside %s",
				m.Idempotency, res.Ref, ref)
		default:
			c.Outcome = Pass
		}
		s.add(c)
	}

	if honours(wp.VerbStatus) {
		s.expectStatus(ctx, ref, wp.EffectiveLive, "a published asset's status is live")
	}
	if honours(wp.VerbReadBack) {
		c := Check{Name: "read_back equals what was approved, field for field", Verb: wp.VerbReadBack, Calls: 1}
		res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: wp.VerbReadBack, Ref: ref})
		c.Elapsed = elapsed
		var got map[string]any
		switch {
		case err != nil:
			c.Outcome, c.Detail = Fail, err.Error()
		case !res.Succeeded():
			c.Outcome, c.Detail = Fail, res.Why()
		case json.Unmarshal(res.Asset, &got) != nil:
			c.Outcome, c.Detail = Fail, "the asset is not a JSON object"
		case !reflect.DeepEqual(got, valid):
			c.Outcome, c.Detail = Fail, fmt.Sprintf("read back %s; approved %s", res.Asset, raw(valid))
		default:
			c.Outcome = Pass
		}
		s.add(c)
	}
	if honours(wp.VerbReadMetrics) {
		s.readMetrics(ctx, m, ref)
	}

	c = Check{Name: "retract takes the asset back, and is safe twice", Verb: wp.VerbRetract, Calls: 2}
	rs, elapsed, err := s.call(ctx, wp.VerbCall{Verb: wp.VerbRetract, Ref: ref}, wp.VerbCall{Verb: wp.VerbRetract, Ref: ref})
	c.Elapsed = elapsed
	outcomes := map[string]bool{wp.RetractedDeleted: true, wp.RetractedPaused: true, wp.RetractedArchived: true, wp.RetractedResolved: true}
	switch {
	case err != nil:
		c.Outcome, c.Detail = Fail, err.Error()
	case len(rs) < 2 || !rs[len(rs)-1].Succeeded():
		c.Outcome, c.Detail = Fail, rs[len(rs)-1].Why()
	case !outcomes[rs[0].Retracted]:
		c.Outcome, c.Detail = Fail, fmt.Sprintf("retracted as %q; want deleted, paused, archived or resolved", rs[0].Retracted)
	case rs[1].Retracted != rs[0].Retracted:
		c.Outcome, c.Detail = Fail, fmt.Sprintf("the second retract answered %q, the first %q", rs[1].Retracted, rs[0].Retracted)
	default:
		c.Outcome, c.Detail = Pass, rs[0].Retracted
	}
	s.add(c)

	if honours(wp.VerbStatus) {
		s.expectStatus(ctx, ref, wp.EffectiveGone, "a retracted asset's status says so")
	}
	if honours(wp.VerbReadBack) {
		c := Check{Name: "read_back of a retracted asset is refused", Verb: wp.VerbReadBack, Calls: 1}
		res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: wp.VerbReadBack, Ref: ref})
		c.Elapsed = elapsed
		switch {
		case err != nil:
			c.Outcome, c.Detail = Fail, err.Error()
		case res.Refused == "":
			c.Outcome, c.Detail = Fail, "a retracted asset was not refused: "+map[bool]string{true: "it was read back", false: res.Failed}[res.Succeeded()]
		default:
			c.Outcome, c.Detail = Pass, res.Refused
		}
		s.add(c)
	}
}

func (s *suite) expectStatus(ctx context.Context, ref, effective, name string) {
	c := Check{Name: name, Verb: wp.VerbStatus, Calls: 1}
	res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: wp.VerbStatus, Ref: ref})
	c.Elapsed = elapsed
	switch {
	case err != nil:
		c.Outcome, c.Detail = Fail, err.Error()
	case !res.Succeeded():
		c.Outcome, c.Detail = Fail, res.Why()
	case res.Status == nil || res.Status.Effective != effective:
		c.Outcome, c.Detail = Fail, fmt.Sprintf("status %+v; want effective %s", res.Status, effective)
	default:
		c.Outcome, c.Detail = Pass, res.Status.Configured
	}
	s.add(c)
}

// readMetrics checks every metric the manifest lists is answered: a value
// with the quality the manifest promises where available, and a sentence,
// never a number, where not.
func (s *suite) readMetrics(ctx context.Context, m *wp.CapabilityManifest, ref string) {
	c := Check{Name: "read_metrics answers every metric the manifest lists", Verb: wp.VerbReadMetrics, Calls: 1}
	res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: wp.VerbReadMetrics, Ref: ref})
	c.Elapsed = elapsed
	if err != nil {
		c.Outcome, c.Detail = Fail, err.Error()
		s.add(c)
		return
	}
	if !res.Succeeded() {
		c.Outcome, c.Detail = Fail, res.Why()
		s.add(c)
		return
	}
	var wrong, read []string
	for _, nm := range wp.SortedMetrics(m.Metrics) {
		got, ok := res.AssetMetrics[nm.Name]
		switch {
		case !ok:
			wrong = append(wrong, nm.Name+" is not answered")
		case nm.Support.Level == wp.MetricUnavailable && got.Value != nil:
			wrong = append(wrong, fmt.Sprintf("%s is declared unavailable and read as %v", nm.Name, *got.Value))
		case nm.Support.Level == wp.MetricUnavailable && got.Unavailable == "":
			wrong = append(wrong, nm.Name+" is declared unavailable and not answered with why")
		case nm.Support.Level == wp.MetricAvailable && got.Value == nil:
			wrong = append(wrong, fmt.Sprintf("%s is declared available and not read: %s", nm.Name, got.Unavailable))
		case nm.Support.Level == wp.MetricAvailable && !covers(got.Quality, nm.Support.Quality):
			wrong = append(wrong, fmt.Sprintf("%s carries quality %v, not the %v the manifest promises", nm.Name, got.Quality, nm.Support.Quality))
		case got.Value != nil:
			read = append(read, fmt.Sprintf("%s %v", nm.Name, *got.Value))
		}
	}
	if len(wrong) > 0 {
		c.Outcome, c.Detail = Fail, strings.Join(wrong, "; ")
	} else {
		c.Outcome, c.Detail = Pass, strings.Join(read, ", ")
	}
	s.add(c)
}

// --- credentials ---

// credentialsStayPut checks that no credential the run held or was given
// appears in anything the wrapper printed outside authorize's credentials
// field: not on stderr, not in an error, not in any other field.
func (s *suite) credentialsStayPut() {
	c := Check{Name: "no credential appears outside the credentials field, on either stream"}
	public := map[string]bool{}
	for _, f := range s.t.Description.Connection.Credentials {
		public[f.Name] = f.Public
	}
	var values []string
	for name, v := range s.t.Connection.Credentials {
		// A public credential is an identifier the protocol shows (a client
		// id in an authorize URL); short values would match by accident.
		if !public[name] && len(v) >= 8 {
			values = append(values, v)
		}
	}
	var leaks []string
	for _, out := range s.seen {
		for _, v := range values {
			if strings.Contains(out, v) {
				leaks = append(leaks, fmt.Sprintf("a credential of %d characters", len(v)))
			}
		}
	}
	switch {
	case len(values) == 0:
		c.Outcome, c.Detail = Warn, "the run held no credential to look for"
	case len(leaks) > 0:
		c.Outcome, c.Detail = Fail, fmt.Sprintf("found %s in the wrapper's output", strings.Join(leaks, ", "))
	default:
		c.Outcome, c.Detail = Pass, fmt.Sprintf("%d credential(s) looked for in %d outputs", len(values), len(s.seen))
	}
	s.add(c)
}
