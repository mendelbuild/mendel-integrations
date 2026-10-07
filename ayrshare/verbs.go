package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

const (
	credentialName = "AYRSHARE_API_KEY"
	platform       = "reddit"

	// Ayrshare's statuses, as its documentation words them.
	stAwaiting = "awaiting approval"
	stSuccess  = "success"
	stDeleted  = "deleted"

	// metaMark starts the line of a post's notes in which the wrapper keeps what
	// Ayrshare has no field for: the idempotency key, and the link that ends the
	// text. The first line is Mendel's name for the post.
	metaMark = "\nmendel:"
	// lineBreak is Ayrshare's invisible line break (Post API Overview, "Line
	// Breaks"): a new line is sent as U+2063 then \n.
	lineBreak = "\u2063\n"
)

type wrapper struct {
	req wp.WrapperRequest
	c   *client
}

func refused(format string, a ...any) wp.VerbResult {
	return wp.VerbResult{Refused: fmt.Sprintf(format, a...)}
}

func failed(format string, a ...any) wp.VerbResult {
	return wp.VerbResult{Failed: fmt.Sprintf(format, a...)}
}

// fromErr is how an error from Ayrshare is answered: a post it will not take is
// refused, with its words; anything else went wrong.
func fromErr(what string, err error) wp.VerbResult {
	var ae *apiError
	if errors.As(err, &ae) {
		switch ae.HTTP {
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			return refused("Ayrshare refused %s: %s", what, ae.Message)
		case http.StatusUnauthorized, http.StatusForbidden:
			return failed("Ayrshare rejected the API key while %s (HTTP %d): %s", what, ae.HTTP, ae.Message)
		}
	}
	return failed("%s: %v", what, err)
}

func (w *wrapper) handle(call wp.VerbCall) wp.VerbResult {
	if why, no := declined[call.Verb]; no {
		return wp.VerbResult{Refused: why}
	}
	switch call.Verb {
	case wp.VerbProbe, wp.VerbDraft, wp.VerbPublish, wp.VerbStatus, wp.VerbRetract, wp.VerbReadBack, wp.VerbListOwned:
	default:
		return refused("%q is not a verb of the contract", call.Verb)
	}
	if strings.TrimSpace(w.req.Connection.Credentials[credentialName]) == "" {
		return failed("the connection carries no %s", credentialName)
	}
	switch call.Verb {
	case wp.VerbProbe:
		return w.probe()
	case wp.VerbDraft:
		return w.draft(call)
	case wp.VerbPublish:
		return w.publish(call)
	case wp.VerbStatus:
		return w.status(call)
	case wp.VerbRetract:
		return w.retract(call)
	case wp.VerbReadBack:
		return w.readBack(call)
	default:
		return w.listOwned(call)
	}
}

// probe proves the key reads the account with the smallest call there is: one
// record of the history.
func (w *wrapper) probe() wp.VerbResult {
	if _, err := w.c.history(1); err != nil {
		return fromErr("reading the account's history", err)
	}
	return wp.VerbResult{Manifest: manifest()}
}

// --- the social_post kind ---

type socialPost struct {
	Text  string
	Link  string // "" is null
	Media []string
}

type asset struct {
	Text  string   `json:"text"`
	Link  *string  `json:"link"`
	Media []string `json:"media"`
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && !strings.ContainsAny(s, " \t\r\n")
}

// parsePayload reads a social_post payload against the kind's shape, and says
// why not when it does not fit.
func parsePayload(raw json.RawMessage) (socialPost, string) {
	var sp socialPost
	if len(raw) == 0 {
		return sp, "there is no payload to publish"
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return sp, "the payload is not a JSON object"
	}
	for _, f := range []string{"text", "link", "media"} {
		if _, ok := fields[f]; !ok {
			return sp, fmt.Sprintf("the payload lacks %q, which a social_post requires (null where there is none)", f)
		}
	}
	for f := range fields {
		if f != "text" && f != "link" && f != "media" {
			return sp, fmt.Sprintf("the payload has %q, which is not a field of a social_post", f)
		}
	}
	if err := json.Unmarshal(fields["text"], &sp.Text); err != nil {
		return sp, "text is not a string"
	}
	if strings.TrimSpace(sp.Text) == "" {
		return sp, "text is empty, and a Reddit post needs text"
	}
	if n := utf8.RuneCountInString(sp.Text); n > maxText {
		return sp, fmt.Sprintf("text is %d characters; the most this wrapper sends is %d", n, maxText)
	}
	var link *string
	if err := json.Unmarshal(fields["link"], &link); err != nil {
		return sp, "link is neither a string nor null"
	}
	if link != nil {
		if !isHTTPURL(*link) {
			return sp, fmt.Sprintf("link %q is not an http(s) URL", *link)
		}
		sp.Link = *link
	}
	var media []string
	if err := json.Unmarshal(fields["media"], &media); err != nil {
		return sp, "media is neither a list of image URLs nor null"
	}
	if string(fields["media"]) != "null" && len(media) == 0 {
		return sp, "media is an empty list; use null for none"
	}
	for _, m := range media {
		if !isHTTPURL(m) {
			return sp, fmt.Sprintf("media %q is not an http(s) URL Ayrshare can fetch", m)
		}
		u, _ := url.Parse(m)
		switch ext := strings.ToLower(path.Ext(u.Path)); ext {
		case ".jpg", ".jpeg", ".png", ".gif", ".webp", "":
		default:
			return sp, fmt.Sprintf("media %q is not a JPG, PNG, GIF or WEBP image; Reddit takes no video through the API", m)
		}
	}
	sp.Media = media
	return sp, ""
}

func encodeText(s string) string { return strings.ReplaceAll(s, "\n", lineBreak) }
func decodeText(s string) string { return strings.ReplaceAll(s, lineBreak, "\n") }

type meta struct {
	Key  string `json:"key,omitempty"`
	Link string `json:"link,omitempty"`
}

func notesFor(name string, m meta) string {
	b, _ := json.Marshal(m)
	return name + metaMark + string(b)
}

// parseNotes splits a post's notes into Mendel's name for it and what the
// wrapper kept beside the name.
func parseNotes(notes string) (string, meta) {
	i := strings.Index(notes, metaMark)
	if i < 0 {
		return notes, meta{}
	}
	var m meta
	_ = json.Unmarshal([]byte(notes[i+len(metaMark):]), &m)
	return notes[:i], m
}

// newBody is the body of POST /post for a social_post.
func (w *wrapper) newBody(sp socialPost, name, key string, hold bool) map[string]any {
	text := sp.Text
	if sp.Link != "" {
		text += "\n\n" + sp.Link
	}
	body := map[string]any{
		"post":      encodeText(text),
		"platforms": []string{platform},
		"notes":     notesFor(name, meta{Key: key, Link: sp.Link}),
	}
	if len(sp.Media) > 0 {
		body["mediaUrls"] = sp.Media
	}
	if key != "" {
		body["idempotencyKey"] = key
	}
	if hold {
		body["requiresApproval"] = true
	}
	// Reddit's own fields are not in the pages this wrapper was written
	// against (README.md); they are sent only when the project names a
	// subreddit.
	if sub, _ := w.req.Connection.Config["subreddit"].(string); strings.TrimSpace(sub) != "" {
		body["redditOptions"] = map[string]any{
			"subreddit": strings.TrimPrefix(strings.TrimSpace(sub), "r/"),
			"title":     redditTitle(sp.Text),
		}
	}
	return body
}

func redditTitle(text string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(text), "\n", 2)[0])
	if r := []rune(line); len(r) > 300 {
		line = string(r[:300])
	}
	return line
}

// assetOf reads a post back into the kind's family fields.
func assetOf(p *apiPost) asset {
	text := decodeText(p.Post)
	a := asset{Text: text}
	_, m := parseNotes(p.Notes)
	if m.Link != "" && strings.HasSuffix(text, "\n\n"+m.Link) {
		a.Text = strings.TrimSuffix(text, "\n\n"+m.Link)
		l := m.Link
		a.Link = &l
	}
	if len(p.MediaURLs) > 0 {
		a.Media = p.MediaURLs
	}
	return a
}

func (w *wrapper) payload(call wp.VerbCall) (socialPost, *wp.VerbResult) {
	if call.AssetKind != kindSocialPost {
		r := refused("this wrapper acts on %s only, not %q", kindSocialPost, call.AssetKind)
		return socialPost{}, &r
	}
	sp, why := parsePayload(call.Payload)
	if why != "" {
		r := refused("%s", why)
		return sp, &r
	}
	return sp, nil
}

// --- draft, publish ---

// draft makes a post held for approval: Ayrshare's approval workflow
// (requiresApproval) keeps it unsent, "awaiting approval", until approved.
func (w *wrapper) draft(call wp.VerbCall) wp.VerbResult {
	sp, bad := w.payload(call)
	if bad != nil {
		return *bad
	}
	p, res := w.create(w.newBody(sp, call.Name, call.IdempotencyKey, true), call.IdempotencyKey, "drafting the post")
	if res != nil {
		return *res
	}
	if p.Status != stAwaiting {
		if p.Status == stSuccess {
			// Never leave a draft that went out. Taking it back is best effort.
			_ = w.c.do(http.MethodDelete, "/post", nil, map[string]any{"id": p.ID}, nil)
		}
		return failed("Ayrshare answered a held post with status %q rather than %q", p.Status, stAwaiting)
	}
	return wp.VerbResult{Ref: p.ID}
}

// create posts a body. A repeated idempotency key is answered with the post the
// key made, found by the key in its notes.
func (w *wrapper) create(body map[string]any, key, what string) (*apiPost, *wp.VerbResult) {
	var p apiPost
	err := w.c.do(http.MethodPost, "/post", nil, body, &p)
	if err != nil {
		var ae *apiError
		if key != "" && errors.As(err, &ae) && ae.duplicateKey() {
			return w.byKey(key)
		}
		r := fromErr(what, err)
		return nil, &r
	}
	if p.ID == "" {
		r := failed("Ayrshare answered %s without a post id", what)
		return nil, &r
	}
	if strings.EqualFold(p.Status, "error") {
		r := refused("Ayrshare could not post it: %s", errorDetail(p))
		return nil, &r
	}
	return &p, nil
}

func errorDetail(p apiPost) string {
	for _, id := range p.PostIDs {
		if id.Status == "error" {
			return "the post failed on " + id.Platform
		}
	}
	return "status error"
}

func (w *wrapper) byKey(key string) (*apiPost, *wp.VerbResult) {
	list, err := w.c.history(historyCount)
	if err != nil {
		r := fromErr("looking for the post a repeated idempotency key made", err)
		return nil, &r
	}
	for i := range list {
		if _, m := parseNotes(list[i].Notes); m.Key == key {
			if list[i].Status == stDeleted {
				r := refused("the idempotency key %q made post %s, which has since been retracted; use a new key to post again", key, list[i].ID)
				return nil, &r
			}
			return &list[i], nil
		}
	}
	r := failed("Ayrshare says the idempotency key %q was used, and the post it made is not in the latest %d of the history", key, historyCount)
	return nil, &r
}

func (w *wrapper) publish(call wp.VerbCall) wp.VerbResult {
	var at string
	when := call.When
	switch {
	case when == nil || when.Mode == wp.WhenNow || when.Mode == "":
	case when.Mode == wp.WhenAt:
		if when.At == nil || when.At.IsZero() {
			return refused("publishing at an instant needs the instant")
		}
		at = when.At.UTC().Format(time.RFC3339)
	case when.Mode == wp.WhenAnnounce:
		return refused("Ayrshare has no announced start: a post is sent now or at a scheduled time")
	default:
		return refused("%q is not a way to time a publish (now, at, announce)", when.Mode)
	}

	if call.Ref != "" {
		return w.publishDraft(call.Ref, at)
	}
	sp, bad := w.payload(call)
	if bad != nil {
		return *bad
	}
	body := w.newBody(sp, call.Name, call.IdempotencyKey, false)
	if at != "" {
		body["scheduleDate"] = at
	}
	p, res := w.create(body, call.IdempotencyKey, "publishing the post")
	if res != nil {
		return *res
	}
	return wp.VerbResult{Ref: p.ID, URL: p.url()}
}

// publishDraft approves a post held for approval (PATCH approved: true), which
// sends it at its scheduled time, or at once.
func (w *wrapper) publishDraft(ref, at string) wp.VerbResult {
	p, err := w.c.getPost(ref)
	if errors.Is(err, errNoPost) {
		return refused("Ayrshare holds no post %s", ref)
	}
	if err != nil {
		return fromErr("reading the draft", err)
	}
	switch strings.ToLower(p.Status) {
	case stDeleted:
		return refused("post %s was deleted, and cannot be published", ref)
	case stSuccess:
		return wp.VerbResult{Ref: p.ID, URL: p.url()}
	case stAwaiting:
		body := map[string]any{"id": p.ID, "approved": true}
		if at != "" {
			body["scheduleDate"] = at
		}
		if err := w.c.do(http.MethodPatch, "/post", nil, body, nil); err != nil {
			return fromErr("approving the draft", err)
		}
		p, err = w.c.getPost(ref)
		if err != nil {
			return fromErr("reading the post after approving it", err)
		}
		if strings.EqualFold(p.Status, stAwaiting) {
			return failed("Ayrshare still holds post %s for approval after it was approved", ref)
		}
	}
	return wp.VerbResult{Ref: p.ID, URL: p.url()}
}

// --- status, read_back, retract, list_owned ---

func (w *wrapper) lookup(call wp.VerbCall) (*apiPost, *wp.VerbResult) {
	if call.Ref == "" {
		r := refused("no ref was given for the post")
		return nil, &r
	}
	p, err := w.c.getPost(call.Ref)
	if errors.Is(err, errNoPost) {
		r := refused("Ayrshare holds no post %s", call.Ref)
		return nil, &r
	}
	if err != nil {
		r := fromErr("reading the post", err)
		return nil, &r
	}
	return p, nil
}

func (w *wrapper) status(call wp.VerbCall) wp.VerbResult {
	p, bad := w.lookup(call)
	if bad != nil {
		return *bad
	}
	st := &wp.AssetStatus{Configured: p.Status, Effective: wp.EffectiveNotLive}
	switch strings.ToLower(p.Status) {
	case stSuccess:
		st.Effective = wp.EffectiveLive
	case stDeleted:
		st.Effective = wp.EffectiveGone
	case stAwaiting:
		st.Review = stAwaiting
	case "scheduled":
		if p.ScheduleDate != "" {
			st.Configured = "scheduled for " + p.ScheduleDate
		}
	}
	return wp.VerbResult{Status: st, URL: p.url()}
}

func (w *wrapper) readBack(call wp.VerbCall) wp.VerbResult {
	p, bad := w.lookup(call)
	if bad != nil {
		return *bad
	}
	if strings.EqualFold(p.Status, stDeleted) {
		return refused("post %s is gone: it was deleted at Ayrshare", p.ID)
	}
	b, _ := json.Marshal(assetOf(p))
	return wp.VerbResult{Asset: b}
}

// retract deletes the post (DELETE /post). Ayrshare keeps a deleted post in
// its history, marked deleted, so no history is lost; a post already deleted is
// answered as deleted without a second delete.
func (w *wrapper) retract(call wp.VerbCall) wp.VerbResult {
	p, bad := w.lookup(call)
	if bad != nil {
		return *bad
	}
	if !strings.EqualFold(p.Status, stDeleted) {
		if err := w.c.do(http.MethodDelete, "/post", nil, map[string]any{"id": p.ID}, nil); err != nil {
			return fromErr("deleting the post", err)
		}
	}
	return wp.VerbResult{Retracted: wp.RetractedDeleted}
}

func (w *wrapper) listOwned(call wp.VerbCall) wp.VerbResult {
	list, err := w.c.history(historyCount)
	if err != nil {
		return fromErr("reading the history", err)
	}
	var owned []string
	for _, p := range list {
		if strings.EqualFold(p.Status, stDeleted) || p.ID == "" {
			continue
		}
		if name, _ := parseNotes(p.Notes); strings.HasPrefix(name, call.Prefix) {
			owned = append(owned, p.ID)
		}
	}
	sort.Strings(owned)
	return wp.VerbResult{Owned: owned}
}
