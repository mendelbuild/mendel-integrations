// Command mastodon is Mendel's External Tool Wrapper for Mastodon
// (docs.joinmastodon.org, fetched 2026-09-28 at commit bbf263446459; the
// fetched pages are cited in full in spec/mastodon of the mendel-integrations
// repository and reproduced beside this wrapper's tests).
//
// It acts on one account's own posts as the `social_post` External Asset
// Kind (see social_post.family.schema.json): it registers an OAuth
// application and exchanges a code for a token (authorize), posts a status
// (publish), edits its text (append_update), deletes it (retract), reads it
// back in the family's shape (read_back), reads its engagement counts
// (read_metrics), lists the account's own recent posts (list_owned), and
// reports what is configured and in effect for one post (status). Mastodon
// has no unpublished form of a status distinct from posting it live, so
// draft is declined; it offers no time-windowed analytics for an account's
// engagement, so read_series, read_total and search are declined too; and it
// exposes nothing Mendel could set a numeric cap on, so set_cap is declined.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// wrapperVersion is this wrapper's own version, restated in wrapper.json.
const wrapperVersion = "0.1.0"

// specSource is the spec this wrapper was written against, fed into every
// manifest and never recalled.
const specSource = "https://docs.joinmastodon.org/ (fetched 2026-09-28 at commit bbf263446459)"

// specHash identifies exactly which pages that was: the sha256 of the
// concatenation of every fetched page under spec/mastodon, in the tree's
// order (`cat spec/mastodon/*.md spec/mastodon/*.json | sha256sum`).
const specHash = "sha256:13025cc3adb16a31288af9423eda10f299d588f2e8b49b8ae3d1f05aa0f86e22"

// socialPostKind is the External Asset Kind this wrapper acts on.
const socialPostKind = "social_post"

// Credential names, as wrapper.json's connection.credentials declares them.
const (
	credAccessToken  = "MASTODON_ACCESS_TOKEN"
	credClientID     = "MASTODON_CLIENT_ID"
	credClientSecret = "MASTODON_CLIENT_SECRET"
)

// oauthScopes is what the OAuth application registered by authorize asks
// for: just enough to verify the credential, and read and write the
// account's own posts (api/oauth-scopes.md).
const oauthScopes = "profile read:statuses write:statuses"

// requestTimeout bounds one HTTP call to the instance.
const requestTimeout = 20 * time.Second

// httpClient is shared across calls; Mastodon instances are reached over
// plain HTTPS, nothing here needs cookies or redirects beyond the default.
var httpClient = &http.Client{Timeout: requestTimeout}

// --- Wire shapes read from a Mastodon instance ---

type mstAccount struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	Acct           string `json:"acct"`
	DisplayName    string `json:"display_name"`
	FollowersCount int    `json:"followers_count"`
	FollowingCount int    `json:"following_count"`
	StatusesCount  int    `json:"statuses_count"`
}

type mstApplication struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type mstToken struct {
	AccessToken string `json:"access_token"`
}

type mstCard struct {
	URL string `json:"url"`
}

type mstMediaAttachment struct {
	Description string `json:"description"`
}

type mstStatus struct {
	ID               string               `json:"id"`
	URL              string               `json:"url"`
	Visibility       string               `json:"visibility"`
	FavouritesCount  int                  `json:"favourites_count"`
	ReblogsCount     int                  `json:"reblogs_count"`
	RepliesCount     int                  `json:"replies_count"`
	QuotesCount      int                  `json:"quotes_count"`
	Card             *mstCard             `json:"card"`
	MediaAttachments []mstMediaAttachment `json:"media_attachments"`
}

type mstStatusSource struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	SpoilerText string `json:"spoiler_text"`
}

type mstError struct {
	Error string `json:"error"`
}

// socialPost is the `social_post` family shape (social_post.family.schema.json).
type socialPost struct {
	Text  string  `json:"text"`
	Link  *string `json:"link"`
	Media *string `json:"media"`
}

// --- HTTP plumbing ---

// instanceBase is the API base URL for one run's connection: the endpoint
// override for a self-hosted mismatch, or https:// the account's instance
// domain (connection.account_id).
func instanceBase(conn wp.Connection) (string, error) {
	if strings.TrimSpace(conn.Endpoint) != "" {
		return strings.TrimRight(conn.Endpoint, "/"), nil
	}
	domain := strings.TrimSpace(conn.AccountID)
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimRight(domain, "/")
	if domain == "" {
		return "", fmt.Errorf("no instance domain was given (connection.account_id)")
	}
	return "https://" + domain, nil
}

// apiCall makes one HTTP call against the instance and returns the response
// (for headers and status), the decoded body's raw bytes, and any transport
// error. body, if non-nil, is marshalled as a JSON request body.
func apiCall(ctx context.Context, method, rawURL, token string, body any) (*http.Response, []byte, error) {
	return apiCallWithIdempotency(ctx, method, rawURL, token, body, "")
}

// apiCallWithIdempotency is apiCall with an optional Idempotency-Key header
// (methods/statuses#create), the one write this wrapper's manifest claims
// native idempotency for.
func apiCallWithIdempotency(ctx context.Context, method, rawURL, token string, body any, key string) (*http.Response, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, err
	}
	return resp, data, nil
}

// apiErrorSentence reads Mastodon's {"error": "..."} shape for a failure
// message, falling back to the bare HTTP status.
func apiErrorSentence(resp *http.Response, data []byte) string {
	var e mstError
	_ = json.Unmarshal(data, &e)
	if e.Error != "" {
		return fmt.Sprintf("%s: %s", resp.Status, e.Error)
	}
	return resp.Status
}

func ok(resp *http.Response) bool { return resp.StatusCode >= 200 && resp.StatusCode < 300 }

// --- Declined verbs, and why: named once, used in both the manifest and the
// defensive dispatch below, so the two cannot say different things. ---

var declinedReasons = map[wp.Verb]string{
	wp.VerbDraft: "Mastodon posts a status live the moment it is created; the API has no " +
		"unpublished form of one to hold before that (its separate scheduled-post feature is " +
		"not covered by the spec this wrapper was written against).",
	wp.VerbReadSeries: "Mastodon's API reports an account's or a post's counts as they stand now; " +
		"it offers no time-windowed analytics to bucket by granularity.",
	wp.VerbReadTotal: "Mastodon's API reports an account's or a post's counts as they stand now, " +
		"not a total over a window; read a post's counts with read_metrics instead.",
	wp.VerbSearch: "this wrapper acts on one account's own posts; it does not search Mastodon's " +
		"federated content on the account's behalf.",
	wp.VerbSetCap: "Mastodon exposes nothing this wrapper could set a numeric cap on.",
}

// --- The Capability Manifest ---

func buildManifest(account mstAccount, entitlements map[string]any, storagePolicy string) *wp.CapabilityManifest {
	m := &wp.CapabilityManifest{
		Contract: wp.ContractVersion,
		Wrapper: wp.WrapperProvenance{
			Version:    wrapperVersion,
			SpecSource: specSource,
			SpecHash:   specHash,
		},
		Verbs:            map[wp.Verb]wp.VerbSupport{},
		Metrics:          map[string]wp.MetricSupport{},
		Granularities:    nil,
		FilterDimensions: nil,
		Venue:            "reversible_writes",
		Idempotency: "native for publish only, via the Idempotency-Key header, kept for up to " +
			"one hour (methods/statuses#create); append_update, retract and the rest have none.",
		Entitlements:  entitlements,
		StoragePolicy: storagePolicy,
		Kinds: map[string]wp.KindSupport{
			socialPostKind: {
				Level: wp.VerbSupported,
				Shape: socialPostSchema,
			},
		},
		Authorization: &wp.AuthorizationSupport{
			Steps:  []string{wp.AuthorizeBegin, wp.AuthorizeComplete, wp.AuthorizeRevoke},
			Scopes: strings.Fields(oauthScopes),
			Expires: "never by default: Mastodon access tokens obtained this way do not expire " +
				"unless a person or the instance's admin revokes them (no refresh grant is offered).",
		},
	}

	m.Verbs[wp.VerbProbe] = wp.VerbSupport{Level: wp.VerbSupported}
	m.Verbs[wp.VerbAuthorize] = wp.VerbSupport{Level: wp.VerbSupported}
	m.Verbs[wp.VerbPublish] = wp.VerbSupport{
		Level: wp.VerbPartial,
		Caveat: "only When.Mode \"now\" is offered; Mastodon's scheduled posts are not covered " +
			"by the spec this wrapper was written against, so \"at\" and \"announce\" are refused.",
	}
	m.Verbs[wp.VerbAppendUpdate] = wp.VerbSupport{
		Level: wp.VerbPartial,
		Caveat: "changes only the post's text; Mastodon's edit endpoint cannot change a " +
			"status's visibility or its media once posted.",
	}
	m.Verbs[wp.VerbRetract] = wp.VerbSupport{Level: wp.VerbSupported}
	m.Verbs[wp.VerbStatus] = wp.VerbSupport{Level: wp.VerbSupported}
	m.Verbs[wp.VerbReadBack] = wp.VerbSupport{
		Level: wp.VerbPartial,
		Caveat: "the text is read back with the link publish appended to it (since Mastodon has no " +
			"separate field for one) taken back off when it matches; the link is the post's preview " +
			"card URL if Mastodon found one, and the media description is the first attachment's, " +
			"both approximations rather than what was originally given.",
	}
	m.Verbs[wp.VerbReadMetrics] = wp.VerbSupport{Level: wp.VerbSupported}
	m.Verbs[wp.VerbListOwned] = wp.VerbSupport{
		Level:  wp.VerbPartial,
		Caveat: "returns the most recent page of the account's own posts, newest first, boosts excluded.",
	}
	for verb, reason := range declinedReasons {
		m.Verbs[verb] = wp.VerbSupport{Level: wp.VerbDeclined, Reason: reason}
	}

	for _, metric := range []string{"favourites_count", "reblogs_count", "replies_count", "quotes_count"} {
		m.Metrics[metric] = wp.MetricSupport{
			Level:        wp.MetricAvailable,
			Kind:         wp.KindCount,
			Aggregations: []string{"count"},
		}
	}

	return m
}

// storagePolicyFor cites the instance's own terms where the probe found one,
// and a generic sentence otherwise (doc 35 §12: the source must be cited).
func storagePolicyFor(termsURL string) string {
	if termsURL != "" {
		return fmt.Sprintf("this instance's terms of service (%s, read from GET /api/v2/instance) "+
			"govern what may be kept of content posted there; Mendel keeps only the reference "+
			"(the post's id and URL) to what it wrote, not a copy of anyone else's posts.", termsURL)
	}
	return "this instance did not name a terms-of-service URL (GET /api/v2/instance); Mendel keeps " +
		"only the reference (the post's id and URL) to what it wrote, not a copy of anyone else's posts."
}

func entitlementsFrom(resp *http.Response, account mstAccount) map[string]any {
	e := map[string]any{
		// api/rate-limits.md: the hardcoded default, unless the response said otherwise.
		"requests_per_5min": 300,
		"statuses_count":    account.StatusesCount,
		"followers_count":   account.FollowersCount,
	}
	if v := resp.Header.Get("X-RateLimit-Limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			e["requests_per_5min"] = n
		}
	}
	if v := resp.Header.Get("X-RateLimit-Remaining"); v != "" {
		e["rate_limit_remaining"] = v
	}
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		e["rate_limit_reset"] = v
	}
	return e
}

// --- probe ---

func handleProbe(ctx context.Context, conn wp.Connection) wp.VerbResult {
	base, err := instanceBase(conn)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbProbe, Failed: err.Error()}
	}
	token := conn.Credentials[credAccessToken]
	if token == "" {
		return wp.VerbResult{Verb: wp.VerbProbe, Failed: "no " + credAccessToken +
			" in the connection; connect through authorize first"}
	}
	resp, data, err := apiCall(ctx, http.MethodGet, base+"/api/v1/accounts/verify_credentials", token, nil)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbProbe, Failed: "reaching " + base + ": " + err.Error()}
	}
	if !ok(resp) {
		return wp.VerbResult{Verb: wp.VerbProbe, Failed: "verify_credentials: " + apiErrorSentence(resp, data)}
	}
	var account mstAccount
	if err := json.Unmarshal(data, &account); err != nil {
		return wp.VerbResult{Verb: wp.VerbProbe, Failed: "reading verify_credentials's answer: " + err.Error()}
	}

	termsURL := ""
	if instResp, instData, err := apiCall(ctx, http.MethodGet, base+"/api/v2/instance", "", nil); err == nil && ok(instResp) {
		var inst struct {
			Configuration struct {
				URLs struct {
					TermsOfService *string `json:"terms_of_service"`
				} `json:"urls"`
			} `json:"configuration"`
		}
		if json.Unmarshal(instData, &inst) == nil && inst.Configuration.URLs.TermsOfService != nil {
			termsURL = *inst.Configuration.URLs.TermsOfService
		}
	}

	manifest := buildManifest(account, entitlementsFrom(resp, account), storagePolicyFor(termsURL))
	return wp.VerbResult{Verb: wp.VerbProbe, Manifest: manifest}
}

// --- authorize ---

func handleAuthorize(ctx context.Context, conn wp.Connection, call wp.VerbCall) wp.VerbResult {
	switch call.Step {
	case wp.AuthorizeBegin:
		return authorizeBegin(ctx, conn, call)
	case wp.AuthorizeComplete:
		return authorizeComplete(ctx, conn, call)
	case wp.AuthorizeRevoke:
		return authorizeRevoke(ctx, conn, call)
	default:
		return wp.VerbResult{Verb: wp.VerbAuthorize,
			Refused: fmt.Sprintf("step %q is not offered; this wrapper answers begin, complete and revoke", call.Step)}
	}
}

func redirectOrOOB(redirect string) string {
	if strings.TrimSpace(redirect) == "" {
		return wp.OutOfBandRedirect
	}
	return redirect
}

func authorizeBegin(ctx context.Context, conn wp.Connection, call wp.VerbCall) wp.VerbResult {
	base, err := instanceBase(conn)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbAuthorize, Failed: err.Error()}
	}
	redirect := redirectOrOOB(call.RedirectURI)
	clientID, clientSecret := conn.Credentials[credClientID], conn.Credentials[credClientSecret]
	if clientID == "" || clientSecret == "" {
		app := map[string]any{
			"client_name":   "Mendel",
			"redirect_uris": redirect,
			"scopes":        oauthScopes,
		}
		resp, data, err := apiCall(ctx, http.MethodPost, base+"/api/v1/apps", "", app)
		if err != nil {
			return wp.VerbResult{Verb: wp.VerbAuthorize, Failed: "registering an application: " + err.Error()}
		}
		if !ok(resp) {
			return wp.VerbResult{Verb: wp.VerbAuthorize, Failed: "registering an application: " + apiErrorSentence(resp, data)}
		}
		var creds mstApplication
		if err := json.Unmarshal(data, &creds); err != nil {
			return wp.VerbResult{Verb: wp.VerbAuthorize, Failed: "reading the registered application: " + err.Error()}
		}
		clientID, clientSecret = creds.ClientID, creds.ClientSecret
	}

	values := url.Values{
		"response_type": {"code"},
		"client_id":     {clientID},
		"redirect_uri":  {redirect},
		"scope":         {oauthScopes},
	}
	if call.State != "" {
		values.Set("state", call.State)
	}
	authorizeURL := base + "/oauth/authorize?" + values.Encode()

	return wp.VerbResult{
		Verb:         wp.VerbAuthorize,
		AuthorizeURL: authorizeURL,
		Credentials: map[string]string{
			credClientID:     clientID,
			credClientSecret: clientSecret,
		},
	}
}

func authorizeComplete(ctx context.Context, conn wp.Connection, call wp.VerbCall) wp.VerbResult {
	base, err := instanceBase(conn)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbAuthorize, Failed: err.Error()}
	}
	clientID, clientSecret := conn.Credentials[credClientID], conn.Credentials[credClientSecret]
	if clientID == "" || clientSecret == "" {
		return wp.VerbResult{Verb: wp.VerbAuthorize,
			Failed: "no application was registered for this connection; call authorize's begin step first"}
	}
	if call.Code == "" {
		return wp.VerbResult{Verb: wp.VerbAuthorize, Failed: "no code was given to exchange"}
	}
	form := map[string]any{
		"grant_type":    "authorization_code",
		"code":          call.Code,
		"client_id":     clientID,
		"client_secret": clientSecret,
		"redirect_uri":  redirectOrOOB(call.RedirectURI),
	}
	resp, data, err := apiCall(ctx, http.MethodPost, base+"/oauth/token", "", form)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbAuthorize, Failed: "exchanging the code: " + err.Error()}
	}
	if !ok(resp) {
		return wp.VerbResult{Verb: wp.VerbAuthorize, Failed: "exchanging the code: " + apiErrorSentence(resp, data)}
	}
	var tok mstToken
	if err := json.Unmarshal(data, &tok); err != nil {
		return wp.VerbResult{Verb: wp.VerbAuthorize, Failed: "reading the token: " + err.Error()}
	}
	return wp.VerbResult{
		Verb: wp.VerbAuthorize,
		Credentials: map[string]string{
			credAccessToken:  tok.AccessToken,
			credClientID:     clientID,
			credClientSecret: clientSecret,
		},
	}
}

func authorizeRevoke(ctx context.Context, conn wp.Connection, _ wp.VerbCall) wp.VerbResult {
	base, err := instanceBase(conn)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbAuthorize, Failed: err.Error()}
	}
	clientID, clientSecret := conn.Credentials[credClientID], conn.Credentials[credClientSecret]
	token := conn.Credentials[credAccessToken]
	if clientID == "" || clientSecret == "" || token == "" {
		// Nothing to revoke is as good as revoked: retreat is never conditional.
		return wp.VerbResult{Verb: wp.VerbAuthorize}
	}
	form := map[string]any{
		"client_id":     clientID,
		"client_secret": clientSecret,
		"token":         token,
	}
	resp, data, err := apiCall(ctx, http.MethodPost, base+"/oauth/revoke", "", form)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbAuthorize, Failed: "revoking the token: " + err.Error()}
	}
	// 403 means the token was already not this application's (or already gone),
	// which for revoke's purposes is the outcome asked for.
	if !ok(resp) && resp.StatusCode != http.StatusForbidden {
		return wp.VerbResult{Verb: wp.VerbAuthorize, Failed: "revoking the token: " + apiErrorSentence(resp, data)}
	}
	return wp.VerbResult{Verb: wp.VerbAuthorize}
}

// --- the social_post action surface ---

func decodePost(payload []byte) (socialPost, error) {
	var post socialPost
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&post); err != nil {
		return socialPost{}, err
	}
	if strings.TrimSpace(post.Text) == "" {
		return socialPost{}, fmt.Errorf("text is empty")
	}
	return post, nil
}

// composeText is what is actually posted: the post's text, with its link
// appended if Mastodon would not otherwise show it (Mastodon has no
// separate field for a status's link; a URL in the text is auto-linked).
func composeText(post socialPost) string {
	text := post.Text
	if post.Link != nil && *post.Link != "" && !strings.Contains(text, *post.Link) {
		text = strings.TrimRight(text, "\n") + "\n\n" + *post.Link
	}
	return text
}

// decomposeText undoes composeText's appending of the link, so read_back
// answers the same text field publish was given rather than the link glued
// onto it for the API that has no separate place to put one. A link
// composeText found already inline is left alone: the full text is what was
// originally given.
func decomposeText(fullText string, link *string) string {
	if link == nil || *link == "" {
		return fullText
	}
	suffix := "\n\n" + *link
	if strings.HasSuffix(fullText, suffix) {
		return strings.TrimSuffix(fullText, suffix)
	}
	return fullText
}

func handlePublish(ctx context.Context, conn wp.Connection, call wp.VerbCall) wp.VerbResult {
	if call.AssetKind != "" && call.AssetKind != socialPostKind {
		return wp.VerbResult{Verb: wp.VerbPublish,
			Refused: fmt.Sprintf("asset kind %q is not offered; this wrapper acts on %s", call.AssetKind, socialPostKind)}
	}
	if call.When == nil || call.When.Mode != wp.WhenNow {
		mode := "none"
		if call.When != nil {
			mode = call.When.Mode
		}
		return wp.VerbResult{Verb: wp.VerbPublish,
			Refused: fmt.Sprintf("when.mode %q is not offered; only \"now\" is", mode)}
	}
	base, err := instanceBase(conn)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbPublish, Failed: err.Error()}
	}
	token := conn.Credentials[credAccessToken]
	if token == "" {
		return wp.VerbResult{Verb: wp.VerbPublish, Failed: "no " + credAccessToken + " in the connection"}
	}
	post, err := decodePost(call.Payload)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbPublish, Refused: "the payload does not match the social_post shape: " + err.Error()}
	}
	form := map[string]any{"status": composeText(post), "visibility": "public"}
	resp, data, err := apiCallWithIdempotency(ctx, http.MethodPost, base+"/api/v1/statuses", token, form, call.IdempotencyKey)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbPublish, Failed: "posting: " + err.Error()}
	}
	if !ok(resp) {
		return wp.VerbResult{Verb: wp.VerbPublish, Failed: "posting: " + apiErrorSentence(resp, data)}
	}
	var status mstStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return wp.VerbResult{Verb: wp.VerbPublish, Failed: "reading the posted status: " + err.Error()}
	}
	return wp.VerbResult{Verb: wp.VerbPublish, Ref: status.ID, URL: status.URL}
}

func handleAppendUpdate(ctx context.Context, conn wp.Connection, call wp.VerbCall) wp.VerbResult {
	if call.Ref == "" {
		return wp.VerbResult{Verb: wp.VerbAppendUpdate, Failed: "no ref was given to update"}
	}
	base, err := instanceBase(conn)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbAppendUpdate, Failed: err.Error()}
	}
	token := conn.Credentials[credAccessToken]
	if token == "" {
		return wp.VerbResult{Verb: wp.VerbAppendUpdate, Failed: "no " + credAccessToken + " in the connection"}
	}
	post, err := decodePost(call.Payload)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbAppendUpdate, Refused: "the payload does not match the social_post shape: " + err.Error()}
	}
	form := map[string]any{"status": composeText(post)}
	resp, data, err := apiCall(ctx, http.MethodPut, base+"/api/v1/statuses/"+call.Ref, token, form)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbAppendUpdate, Failed: "editing: " + err.Error()}
	}
	if !ok(resp) {
		return wp.VerbResult{Verb: wp.VerbAppendUpdate, Failed: "editing: " + apiErrorSentence(resp, data)}
	}
	var status mstStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return wp.VerbResult{Verb: wp.VerbAppendUpdate, Failed: "reading the edited status: " + err.Error()}
	}
	return wp.VerbResult{Verb: wp.VerbAppendUpdate, Ref: status.ID, URL: status.URL}
}

func handleRetract(ctx context.Context, conn wp.Connection, call wp.VerbCall) wp.VerbResult {
	if call.Ref == "" {
		return wp.VerbResult{Verb: wp.VerbRetract, Failed: "no ref was given to retract"}
	}
	base, err := instanceBase(conn)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbRetract, Failed: err.Error()}
	}
	token := conn.Credentials[credAccessToken]
	if token == "" {
		return wp.VerbResult{Verb: wp.VerbRetract, Failed: "no " + credAccessToken + " in the connection"}
	}
	resp, data, err := apiCall(ctx, http.MethodDelete, base+"/api/v1/statuses/"+call.Ref, token, nil)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbRetract, Failed: "deleting: " + err.Error()}
	}
	// Retract is safe to call twice (§6): a 404 here means it is already gone.
	if !ok(resp) && resp.StatusCode != http.StatusNotFound {
		return wp.VerbResult{Verb: wp.VerbRetract, Failed: "deleting: " + apiErrorSentence(resp, data)}
	}
	return wp.VerbResult{Verb: wp.VerbRetract, Retracted: wp.RetractedDeleted}
}

func handleStatus(ctx context.Context, conn wp.Connection, call wp.VerbCall) wp.VerbResult {
	if call.Ref == "" {
		return wp.VerbResult{Verb: wp.VerbStatus, Failed: "no ref was given"}
	}
	base, err := instanceBase(conn)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbStatus, Failed: err.Error()}
	}
	token := conn.Credentials[credAccessToken]
	resp, data, err := apiCall(ctx, http.MethodGet, base+"/api/v1/statuses/"+call.Ref, token, nil)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbStatus, Failed: "reading: " + err.Error()}
	}
	if resp.StatusCode == http.StatusNotFound {
		return wp.VerbResult{Verb: wp.VerbStatus, Status: &wp.AssetStatus{Effective: wp.EffectiveGone}}
	}
	if !ok(resp) {
		return wp.VerbResult{Verb: wp.VerbStatus, Failed: "reading: " + apiErrorSentence(resp, data)}
	}
	var status mstStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return wp.VerbResult{Verb: wp.VerbStatus, Failed: "reading: " + err.Error()}
	}
	return wp.VerbResult{Verb: wp.VerbStatus, Status: &wp.AssetStatus{
		Configured: status.Visibility,
		Effective:  wp.EffectiveLive,
	}}
}

func handleReadBack(ctx context.Context, conn wp.Connection, call wp.VerbCall) wp.VerbResult {
	if call.Ref == "" {
		return wp.VerbResult{Verb: wp.VerbReadBack, Failed: "no ref was given"}
	}
	base, err := instanceBase(conn)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbReadBack, Failed: err.Error()}
	}
	token := conn.Credentials[credAccessToken]
	if token == "" {
		return wp.VerbResult{Verb: wp.VerbReadBack, Failed: "no " + credAccessToken + " in the connection"}
	}
	sResp, sData, err := apiCall(ctx, http.MethodGet, base+"/api/v1/statuses/"+call.Ref+"/source", token, nil)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbReadBack, Failed: "reading the source: " + err.Error()}
	}
	if sResp.StatusCode == http.StatusNotFound {
		return wp.VerbResult{Verb: wp.VerbReadBack, Refused: "the asset has been retracted; there is nothing left to read back"}
	}
	if !ok(sResp) {
		return wp.VerbResult{Verb: wp.VerbReadBack, Failed: "reading the source: " + apiErrorSentence(sResp, sData)}
	}
	var source mstStatusSource
	if err := json.Unmarshal(sData, &source); err != nil {
		return wp.VerbResult{Verb: wp.VerbReadBack, Failed: "reading the source: " + err.Error()}
	}

	var link, media *string
	if resp, data, err := apiCall(ctx, http.MethodGet, base+"/api/v1/statuses/"+call.Ref, token, nil); err == nil && ok(resp) {
		var status mstStatus
		if json.Unmarshal(data, &status) == nil {
			if status.Card != nil && status.Card.URL != "" {
				u := status.Card.URL
				link = &u
			}
			if len(status.MediaAttachments) > 0 && status.MediaAttachments[0].Description != "" {
				d := status.MediaAttachments[0].Description
				media = &d
			}
		}
	}

	post := socialPost{Text: decomposeText(source.Text, link), Link: link, Media: media}
	raw, err := json.Marshal(post)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbReadBack, Failed: "encoding the asset: " + err.Error()}
	}
	return wp.VerbResult{Verb: wp.VerbReadBack, Asset: raw}
}

func handleReadMetrics(ctx context.Context, conn wp.Connection, call wp.VerbCall) wp.VerbResult {
	if call.Ref == "" {
		return wp.VerbResult{Verb: wp.VerbReadMetrics, Failed: "no ref was given"}
	}
	base, err := instanceBase(conn)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbReadMetrics, Failed: err.Error()}
	}
	token := conn.Credentials[credAccessToken]
	resp, data, err := apiCall(ctx, http.MethodGet, base+"/api/v1/statuses/"+call.Ref, token, nil)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbReadMetrics, Failed: "reading: " + err.Error()}
	}
	if !ok(resp) {
		return wp.VerbResult{Verb: wp.VerbReadMetrics, Failed: "reading: " + apiErrorSentence(resp, data)}
	}
	var status mstStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return wp.VerbResult{Verb: wp.VerbReadMetrics, Failed: "reading: " + err.Error()}
	}
	fav, reblogs, replies, quotes := float64(status.FavouritesCount), float64(status.ReblogsCount),
		float64(status.RepliesCount), float64(status.QuotesCount)
	return wp.VerbResult{Verb: wp.VerbReadMetrics, AssetMetrics: map[string]wp.FieldReading{
		"favourites_count": {Value: &fav},
		"reblogs_count":    {Value: &reblogs},
		"replies_count":    {Value: &replies},
		"quotes_count":     {Value: &quotes},
	}}
}

func handleListOwned(ctx context.Context, conn wp.Connection, call wp.VerbCall) wp.VerbResult {
	base, err := instanceBase(conn)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbListOwned, Failed: err.Error()}
	}
	token := conn.Credentials[credAccessToken]
	if token == "" {
		return wp.VerbResult{Verb: wp.VerbListOwned, Failed: "no " + credAccessToken + " in the connection"}
	}
	resp, data, err := apiCall(ctx, http.MethodGet, base+"/api/v1/accounts/verify_credentials", token, nil)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbListOwned, Failed: "finding the account: " + err.Error()}
	}
	if !ok(resp) {
		return wp.VerbResult{Verb: wp.VerbListOwned, Failed: "finding the account: " + apiErrorSentence(resp, data)}
	}
	var account mstAccount
	if err := json.Unmarshal(data, &account); err != nil {
		return wp.VerbResult{Verb: wp.VerbListOwned, Failed: "finding the account: " + err.Error()}
	}

	limit := call.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 40 {
		limit = 40
	}
	values := url.Values{
		"exclude_reblogs": {"true"},
		"limit":           {strconv.Itoa(limit)},
	}
	if call.Prefix != "" {
		values.Set("tagged", call.Prefix)
	}
	listURL := base + "/api/v1/accounts/" + account.ID + "/statuses?" + values.Encode()
	lResp, lData, err := apiCall(ctx, http.MethodGet, listURL, token, nil)
	if err != nil {
		return wp.VerbResult{Verb: wp.VerbListOwned, Failed: "listing: " + err.Error()}
	}
	if !ok(lResp) {
		return wp.VerbResult{Verb: wp.VerbListOwned, Failed: "listing: " + apiErrorSentence(lResp, lData)}
	}
	var statuses []mstStatus
	if err := json.Unmarshal(lData, &statuses); err != nil {
		return wp.VerbResult{Verb: wp.VerbListOwned, Failed: "listing: " + err.Error()}
	}
	owned := make([]string, 0, len(statuses))
	for _, s := range statuses {
		owned = append(owned, s.ID)
	}
	return wp.VerbResult{Verb: wp.VerbListOwned, Owned: owned}
}

// --- dispatch ---

// handle answers one call. Declined verbs are refused here defensively, with
// the same reason the manifest gave, in case a caller sends one anyway: a
// wrapper never does what its own probe said it would not.
func handle(ctx context.Context, conn wp.Connection, call wp.VerbCall) wp.VerbResult {
	if reason, declined := declinedReasons[call.Verb]; declined {
		return wp.VerbResult{Verb: call.Verb, Refused: reason}
	}
	switch call.Verb {
	case wp.VerbProbe:
		return handleProbe(ctx, conn)
	case wp.VerbAuthorize:
		return handleAuthorize(ctx, conn, call)
	case wp.VerbPublish:
		return handlePublish(ctx, conn, call)
	case wp.VerbAppendUpdate:
		return handleAppendUpdate(ctx, conn, call)
	case wp.VerbRetract:
		return handleRetract(ctx, conn, call)
	case wp.VerbStatus:
		return handleStatus(ctx, conn, call)
	case wp.VerbReadBack:
		return handleReadBack(ctx, conn, call)
	case wp.VerbReadMetrics:
		return handleReadMetrics(ctx, conn, call)
	case wp.VerbListOwned:
		return handleListOwned(ctx, conn, call)
	default:
		return wp.VerbResult{Verb: call.Verb, Refused: fmt.Sprintf("verb %q is not one this wrapper answers", call.Verb)}
	}
}

// run answers one WrapperRequest, stopping at the first result that does not
// succeed, per the protocol.
func run(ctx context.Context, req wp.WrapperRequest) wp.WrapperResponse {
	if req.Contract != wp.ContractVersion {
		verb := wp.Verb("probe")
		if len(req.Calls) > 0 {
			verb = req.Calls[0].Verb
		}
		return wp.WrapperResponse{Results: []wp.VerbResult{{
			Verb: verb,
			Failed: fmt.Sprintf("this wrapper speaks contract %q; the request named %q",
				wp.ContractVersion, req.Contract),
		}}}
	}
	results := make([]wp.VerbResult, 0, len(req.Calls))
	for _, call := range req.Calls {
		res := handle(ctx, req.Connection, call)
		results = append(results, res)
		if !res.Succeeded() {
			break
		}
	}
	return wp.WrapperResponse{Results: results}
}
