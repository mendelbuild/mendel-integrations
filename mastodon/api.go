package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// specSource is the documentation the wrapper was written against.
const specSource = "https://docs.joinmastodon.org/methods/statuses/"

// The names the connection carries what authorize produced under. The client
// is registered on the person's own instance, so it is theirs to keep, like
// the token.
const (
	credClientID     = "MASTODON_CLIENT_ID"
	credClientSecret = "MASTODON_CLIENT_SECRET"
	credRedirectURI  = "MASTODON_REDIRECT_URI"
	credAccessToken  = "MASTODON_ACCESS_TOKEN"
)

// scopes are the least this wrapper needs: who the account is, and reading
// and writing its own statuses (docs.joinmastodon.org/api/oauth-scopes).
var scopes = []string{"profile", "read:statuses", "write:statuses"}

// readme is the record of the cited spec. Its hash is the wrapper's spec hash,
// so a change to what was cited is a change of provenance.
//
//go:embed README.md
var readme []byte

func specHash() string {
	sum := sha256.Sum256(readme)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Quality flags every count carries: Mastodon reports lifetime totals, as
// this instance has seen them, which for a federated post is not everyone's.
var countQuality = []string{"lifetime", "as_seen_by_this_instance"}

// counts are the Status entity's counters read_metrics reads, by the names
// the manifest gives them.
var counts = map[string]string{
	"favourites": "favourites_count",
	"reblogs":    "reblogs_count",
	"replies":    "replies_count",
	"quotes":     "quotes_count",
}

type api struct {
	base   string
	conn   wp.Connection
	client *http.Client
}

func newAPI(c wp.Connection, client *http.Client) *api {
	base := strings.TrimRight(c.Endpoint, "/")
	if base == "" && c.AccountID != "" {
		base = "https://" + strings.TrimPrefix(strings.TrimPrefix(c.AccountID, "https://"), "@")
	}
	return &api{base: base, conn: c, client: client}
}

// apiError is a non-2xx answer: its status and Mastodon's own sentence.
type apiError struct {
	status int
	says   string
}

func (e *apiError) Error() string {
	if e.status == 0 { // nothing was sent
		return e.says
	}
	return fmt.Sprintf("the instance answered %d: %s", e.status, e.says)
}

// call makes one request and decodes a 2xx answer into out. The token, when
// held, goes in the Authorization header and nowhere else.
func (a *api) call(ctx context.Context, method, path string, body any, form url.Values, header map[string]string, out any) error {
	if a.base == "" {
		return &apiError{status: 0, says: "the connection names no instance"}
	}
	var rd io.Reader
	contentType := ""
	switch {
	case body != nil:
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd, contentType = bytes.NewReader(raw), "application/json"
	case form != nil:
		rd, contentType = strings.NewReader(form.Encode()), "application/x-www-form-urlencoded"
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	if tok := a.conn.Credentials[credAccessToken]; tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		// The URL holds nothing secret; the error may name it.
		return fmt.Errorf("reaching %s: %v", a.base, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(raw, &e)
		says := strings.TrimSpace(e.Error + " " + e.Description)
		if says == "" {
			says = http.StatusText(resp.StatusCode)
		}
		return &apiError{status: resp.StatusCode, says: says}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("the instance's answer to %s %s is not readable: %v", method, path, err)
		}
	}
	return nil
}

// failed turns an error into a result: a 404 on something the call named is
// the caller's problem; everything else went wrong.
func failed(v wp.Verb, err error) wp.VerbResult {
	return wp.VerbResult{Verb: v, Failed: err.Error()}
}

// --- probe ---

func verbs() map[wp.Verb]wp.VerbSupport {
	declined := func(why string) wp.VerbSupport { return wp.VerbSupport{Level: wp.VerbDeclined, Reason: why} }
	return map[wp.Verb]wp.VerbSupport{
		wp.VerbProbe:     {Level: wp.VerbSupported},
		wp.VerbAuthorize: {Level: wp.VerbSupported},
		wp.VerbDraft: declined("A status is public to its audience the moment it is posted. The only state before " +
			"that is a status scheduled at least five minutes ahead, which this version does not use."),
		wp.VerbPublish: {Level: wp.VerbPartial, Caveat: "Now only; at and announce are refused. Visibility is the " +
			"connection's config (visibility), and private (followers only) when it is not set."},
		wp.VerbStatus:       {Level: wp.VerbSupported},
		wp.VerbAppendUpdate: declined("A social_post is not log-shaped."),
		wp.VerbRetract:      {Level: wp.VerbSupported},
		wp.VerbReadBack:     {Level: wp.VerbSupported},
		wp.VerbReadMetrics:  {Level: wp.VerbSupported},
		wp.VerbSetCap:       declined("Posting spends nothing."),
		wp.VerbListOwned: declined("A status has no name to carry a prefix, and Mastodon has no search of an " +
			"account's own statuses by text; publish is made idempotent with the instance's Idempotency-Key instead."),
		wp.VerbReadSeries: declined("Mastodon keeps no series of a status's counts, only the current totals."),
		wp.VerbReadTotal:  declined("Mastodon reports per status (read_metrics), not totals over a window."),
		wp.VerbSearch:     declined("Not in this version."),
	}
}

func metrics() map[string]wp.MetricSupport {
	out := map[string]wp.MetricSupport{}
	for name := range counts {
		out[name] = wp.MetricSupport{Level: wp.MetricAvailable, Kind: wp.KindCount, Aggregations: []string{"count"}, Quality: countQuality}
	}
	out["impressions"] = wp.MetricSupport{Level: wp.MetricUnavailable,
		Reason: "Mastodon does not count who saw a status, by design."}
	return out
}

// instance is what the probe reads of the server's own configuration.
type instance struct {
	Version       string `json:"version"`
	Configuration struct {
		Statuses struct {
			MaxCharacters            int `json:"max_characters"`
			CharactersReservedPerURL int `json:"characters_reserved_per_url"`
		} `json:"statuses"`
	} `json:"configuration"`
}

// postShape is a social_post as this instance takes it: the family's text
// and optional link (doc 35 §7), text refined to what fits beside a link.
// Media is not offered.
func postShape(inst instance) json.RawMessage {
	max := inst.Configuration.Statuses.MaxCharacters
	if max == 0 {
		max = 500 // the documented default
	}
	perURL := inst.Configuration.Statuses.CharactersReservedPerURL
	if perURL == 0 {
		perURL = 23
	}
	shape := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"text"},
		"properties": map[string]any{
			"text": map[string]any{"type": "string", "minLength": 1, "maxLength": max - perURL - 2,
				"description": fmt.Sprintf("Up to %d characters, so a link fits beside it (a link counts as %d).", max-perURL-2, perURL)},
			"link": map[string]any{"type": "string", "format": "uri",
				"description": "Appended after a blank line; the instance counts it as a fixed length."},
		},
	}
	raw, _ := json.Marshal(shape)
	return raw
}

func (a *api) probe(ctx context.Context) wp.VerbResult {
	var inst instance
	if err := a.call(ctx, http.MethodGet, "/api/v2/instance", nil, nil, nil, &inst); err != nil {
		return failed(wp.VerbProbe, err)
	}
	var me struct {
		Acct   string `json:"acct"`
		Locked bool   `json:"locked"`
	}
	entitlements := map[string]any{"instance_version": inst.Version,
		"rate_limit": "300 calls in 5 minutes per account; 30 deletions in 30 minutes",
		"max_characters": inst.Configuration.Statuses.MaxCharacters}
	if a.conn.Credentials[credAccessToken] != "" {
		if err := a.call(ctx, http.MethodGet, "/api/v1/accounts/verify_credentials", nil, nil, nil, &me); err != nil {
			return failed(wp.VerbProbe, err)
		}
		entitlements["account"] = me.Acct
		entitlements["locked"] = me.Locked
	}
	return wp.VerbResult{Verb: wp.VerbProbe, Manifest: &wp.CapabilityManifest{
		Contract: wp.ContractVersion,
		Wrapper:  wp.WrapperProvenance{Version: wrapperVersion, SpecSource: specSource, SpecHash: specHash()},
		Verbs:    verbs(),
		Metrics:  metrics(),
		// A post exists to its audience once posted and is deleted cleanly;
		// with visibility private on a locked account, the audience is the
		// account's approved followers.
		Venue:        "reversible_writes",
		Idempotency:  "Native: an Idempotency-Key header on posting, which the instance keeps for an hour.",
		Entitlements: entitlements,
		StoragePolicy: "A status's counts are the account holder's own statistics, read with their authorization; " +
			"the instance's terms govern the content, which read_back returns only to its author.",
		Authorization: &wp.AuthorizationSupport{
			Steps:   []string{wp.AuthorizeBegin, wp.AuthorizeComplete, wp.AuthorizeRevoke},
			Scopes:  scopes,
			Expires: "never: Mastodon's tokens do not expire, so there is no refresh; revoke ends one",
		},
		Kinds: map[string]wp.KindSupport{
			"social_post": {Level: wp.VerbSupported, Shape: postShape(inst)},
		},
	}}
}

// --- authorize ---

func (a *api) authorize(ctx context.Context, c wp.VerbCall) wp.VerbResult {
	switch c.Step {
	case wp.AuthorizeBegin:
		return a.begin(ctx, c)
	case wp.AuthorizeComplete:
		return a.complete(ctx, c)
	case wp.AuthorizeRevoke:
		return a.revoke(ctx)
	case wp.AuthorizeRefresh:
		return wp.VerbResult{Verb: wp.VerbAuthorize, Refused: "Mastodon's tokens do not expire, so there is nothing to refresh"}
	}
	return wp.VerbResult{Verb: wp.VerbAuthorize, Refused: fmt.Sprintf("authorize has no step %q", c.Step)}
}

// begin registers a client on the person's instance, unless the connection
// already holds one registered for this redirect, and answers the URL the
// person opens to grant it.
func (a *api) begin(ctx context.Context, c wp.VerbCall) wp.VerbResult {
	v := wp.VerbAuthorize
	if c.RedirectURI == "" || c.State == "" {
		return wp.VerbResult{Verb: v, Refused: "begin needs the redirect URI Mendel hosts and the state it generated"}
	}
	creds := map[string]string{}
	id, secret := a.conn.Credentials[credClientID], a.conn.Credentials[credClientSecret]
	if id == "" || secret == "" || a.conn.Credentials[credRedirectURI] != c.RedirectURI {
		var app struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		}
		if err := a.call(ctx, http.MethodPost, "/api/v1/apps", map[string]any{
			"client_name": "Mendel", "redirect_uris": []string{c.RedirectURI},
			"scopes": strings.Join(scopes, " "), "website": "https://mendel.build",
		}, nil, nil, &app); err != nil {
			return failed(v, err)
		}
		if app.ClientID == "" || app.ClientSecret == "" {
			return wp.VerbResult{Verb: v, Failed: "the instance registered the client without a client id and secret"}
		}
		id, secret = app.ClientID, app.ClientSecret
	}
	creds[credClientID], creds[credClientSecret], creds[credRedirectURI] = id, secret, c.RedirectURI
	q := url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {c.RedirectURI},
		"scope": {strings.Join(scopes, " ")}, "state": {c.State}, "force_login": {"true"}}
	return wp.VerbResult{Verb: v, AuthorizeURL: a.base + "/oauth/authorize?" + q.Encode(), Credentials: creds}
}

// complete exchanges the code for a token, and answers everything a later run
// needs: the client and the token.
func (a *api) complete(ctx context.Context, c wp.VerbCall) wp.VerbResult {
	v := wp.VerbAuthorize
	id, secret, redirect := a.conn.Credentials[credClientID], a.conn.Credentials[credClientSecret], a.conn.Credentials[credRedirectURI]
	switch {
	case c.Code == "":
		return wp.VerbResult{Verb: v, Refused: "complete needs the code from the redirect"}
	case id == "" || secret == "" || redirect == "":
		return wp.VerbResult{Verb: v, Refused: "complete needs the client begin registered; the connection does not hold it"}
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
	}
	err := a.call(ctx, http.MethodPost, "/oauth/token", nil, url.Values{
		"grant_type": {"authorization_code"}, "code": {c.Code}, "client_id": {id},
		"client_secret": {secret}, "redirect_uri": {redirect},
	}, nil, &tok)
	if e, ok := err.(*apiError); ok && e.status == http.StatusBadRequest {
		return wp.VerbResult{Verb: v, Refused: "the instance did not accept the code: " + e.says}
	}
	if err != nil {
		return failed(v, err)
	}
	if tok.AccessToken == "" {
		return wp.VerbResult{Verb: v, Failed: "the instance answered no access token"}
	}
	return wp.VerbResult{Verb: v, Credentials: map[string]string{
		credClientID: id, credClientSecret: secret, credRedirectURI: redirect, credAccessToken: tok.AccessToken}}
}

// revoke asks the instance to forget the token. Safe to call twice: the
// instance answers 200 for a token already revoked.
func (a *api) revoke(ctx context.Context) wp.VerbResult {
	v := wp.VerbAuthorize
	tok := a.conn.Credentials[credAccessToken]
	if tok == "" {
		return wp.VerbResult{Verb: v}
	}
	if err := a.call(ctx, http.MethodPost, "/oauth/revoke", nil, url.Values{
		"client_id": {a.conn.Credentials[credClientID]}, "client_secret": {a.conn.Credentials[credClientSecret]},
		"token": {tok},
	}, nil, nil); err != nil {
		return failed(v, err)
	}
	return wp.VerbResult{Verb: v}
}

// --- the action surface ---

// post is a social_post as the family has it.
type post struct {
	Text string `json:"text"`
	Link string `json:"link,omitempty"`
}

// posted is what the wrapper reads of the Status a post creates.
type posted struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func (a *api) publish(ctx context.Context, c wp.VerbCall) wp.VerbResult {
	v := wp.VerbPublish
	switch {
	case c.AssetKind != "social_post":
		return wp.VerbResult{Verb: v, Refused: fmt.Sprintf("this wrapper publishes social_post, not %q", c.AssetKind)}
	case c.Ref != "":
		return wp.VerbResult{Verb: v, Refused: "there are no drafts here to publish by reference; publish the payload"}
	case c.When != nil && c.When.Mode != wp.WhenNow:
		return wp.VerbResult{Verb: v, Refused: fmt.Sprintf("this wrapper publishes now only, not %q", c.When.Mode)}
	}
	var p post
	dec := json.NewDecoder(bytes.NewReader(c.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return wp.VerbResult{Verb: v, Refused: "the payload is not a social_post: " + err.Error()}
	}
	if strings.TrimSpace(p.Text) == "" {
		return wp.VerbResult{Verb: v, Refused: "a social_post needs text"}
	}
	var inst instance
	if err := a.call(ctx, http.MethodGet, "/api/v2/instance", nil, nil, nil, &inst); err != nil {
		return failed(v, err)
	}
	var shape struct {
		Properties struct {
			Text struct {
				MaxLength int `json:"maxLength"`
			} `json:"text"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(postShape(inst), &shape)
	if n := len([]rune(p.Text)); n > shape.Properties.Text.MaxLength {
		return wp.VerbResult{Verb: v, Refused: fmt.Sprintf("the text is %d characters; this instance takes %d beside a link",
			n, shape.Properties.Text.MaxLength)}
	}
	if p.Link != "" {
		if u, err := url.Parse(p.Link); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return wp.VerbResult{Verb: v, Refused: "the link is not an http(s) URL"}
		}
	}
	text := p.Text
	if p.Link != "" {
		text += "\n\n" + p.Link
	}
	visibility := "private"
	if cfg, ok := a.conn.Config["visibility"].(string); ok && cfg != "" {
		visibility = cfg
	}
	header := map[string]string{}
	if c.IdempotencyKey != "" {
		header["Idempotency-Key"] = c.IdempotencyKey
	}
	var st posted
	err := a.call(ctx, http.MethodPost, "/api/v1/statuses", map[string]any{"status": text, "visibility": visibility},
		nil, header, &st)
	if e, ok := err.(*apiError); ok && e.status == http.StatusUnprocessableEntity {
		return wp.VerbResult{Verb: v, Refused: "the instance refused the post: " + e.says}
	}
	if err != nil {
		return failed(v, err)
	}
	return wp.VerbResult{Verb: v, Ref: st.ID, URL: st.URL}
}

// needRef refuses a call that names no status.
func needRef(v wp.Verb, c wp.VerbCall) (wp.VerbResult, bool) {
	if c.Ref == "" || strings.ContainsAny(c.Ref, "/?#") {
		return wp.VerbResult{Verb: v, Refused: "the call names no status (ref)"}, false
	}
	return wp.VerbResult{}, true
}

func (a *api) getStatus(ctx context.Context, ref string) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	err := a.call(ctx, http.MethodGet, "/api/v1/statuses/"+url.PathEscape(ref), nil, nil, nil, &raw)
	return raw, err
}

func isGone(err error) bool {
	e, ok := err.(*apiError)
	return ok && e.status == http.StatusNotFound
}

func (a *api) status(ctx context.Context, c wp.VerbCall) wp.VerbResult {
	v := wp.VerbStatus
	if r, ok := needRef(v, c); !ok {
		return r
	}
	raw, err := a.getStatus(ctx, c.Ref)
	if isGone(err) {
		return wp.VerbResult{Verb: v, Status: &wp.AssetStatus{Configured: "deleted", Effective: wp.EffectiveGone}}
	}
	if err != nil {
		return failed(v, err)
	}
	var visibility string
	_ = json.Unmarshal(raw["visibility"], &visibility)
	return wp.VerbResult{Verb: v, Status: &wp.AssetStatus{Configured: "visibility " + visibility, Effective: wp.EffectiveLive}}
}

// readBack is the post as the instance now holds it, from the status's
// source, so it is the text as posted rather than the instance's HTML.
func (a *api) readBack(ctx context.Context, c wp.VerbCall) wp.VerbResult {
	v := wp.VerbReadBack
	if r, ok := needRef(v, c); !ok {
		return r
	}
	var src struct {
		Text string `json:"text"`
	}
	err := a.call(ctx, http.MethodGet, "/api/v1/statuses/"+url.PathEscape(c.Ref)+"/source", nil, nil, nil, &src)
	if isGone(err) {
		return wp.VerbResult{Verb: v, Refused: "the status is gone"}
	}
	if err != nil {
		return failed(v, err)
	}
	p := post{Text: src.Text}
	// A link was appended after a blank line; take it back off.
	if i := strings.LastIndex(src.Text, "\n\n"); i >= 0 {
		last := src.Text[i+2:]
		if u, err := url.Parse(last); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && !strings.ContainsAny(last, " \n") {
			p = post{Text: src.Text[:i], Link: last}
		}
	}
	asset, _ := json.Marshal(p)
	return wp.VerbResult{Verb: v, Asset: asset}
}

func (a *api) readMetrics(ctx context.Context, c wp.VerbCall) wp.VerbResult {
	v := wp.VerbReadMetrics
	if r, ok := needRef(v, c); !ok {
		return r
	}
	raw, err := a.getStatus(ctx, c.Ref)
	if isGone(err) {
		return wp.VerbResult{Verb: v, Refused: "the status is gone"}
	}
	if err != nil {
		return failed(v, err)
	}
	out := map[string]wp.FieldReading{}
	for name, field := range counts {
		var n float64
		if err := json.Unmarshal(raw[field], &n); err != nil {
			out[name] = wp.FieldReading{Unavailable: "this instance's answer carries no " + field}
			continue
		}
		out[name] = wp.FieldReading{Value: &n, Quality: countQuality}
	}
	out["impressions"] = wp.FieldReading{Unavailable: metrics()["impressions"].Reason}
	return wp.VerbResult{Verb: v, AssetMetrics: out}
}

// retract deletes the status. A status already gone is retracted: the call
// is safe twice and answers the same.
func (a *api) retract(ctx context.Context, c wp.VerbCall) wp.VerbResult {
	v := wp.VerbRetract
	if r, ok := needRef(v, c); !ok {
		return r
	}
	err := a.call(ctx, http.MethodDelete, "/api/v1/statuses/"+url.PathEscape(c.Ref), nil, nil, nil, nil)
	if err != nil && !isGone(err) {
		return failed(v, err)
	}
	return wp.VerbResult{Verb: v, Retracted: wp.RetractedDeleted}
}
