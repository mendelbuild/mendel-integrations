package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// fakeInstance plays the parts of a Mastodon instance the wrapper uses, as
// the documentation cited in README.md describes them. Tests never call a
// real instance.
type fakeInstance struct {
	mu        sync.Mutex
	apps      int
	token     string
	revoked   bool
	statuses  map[string]map[string]any
	byKey     map[string]string
	lastBody  map[string]any
	lastKey   string
	next      int
	maxChars  int
}

func newFake() *fakeInstance {
	return &fakeInstance{token: "tok-FAKE-SECRET", statuses: map[string]map[string]any{}, byKey: map[string]string{}, maxChars: 500}
}

func (f *fakeInstance) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	authed := r.Header.Get("Authorization") == "Bearer "+f.token && !f.revoked
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v2/instance":
		reply(200, map[string]any{"version": "4.4.0", "configuration": map[string]any{
			"statuses": map[string]any{"max_characters": f.maxChars, "characters_reserved_per_url": 23}}})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/apps":
		f.apps++
		reply(200, map[string]any{"client_id": "cid", "client_secret": "csecret-FAKE"})
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/token":
		r.ParseForm()
		if r.Form.Get("code") != "good-code" || r.Form.Get("client_secret") != "csecret-FAKE" {
			reply(400, map[string]any{"error": "invalid_grant", "error_description": "The provided authorization grant is invalid"})
			return
		}
		reply(200, map[string]any{"access_token": f.token, "token_type": "Bearer", "scope": r.Form.Get("scope")})
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/revoke":
		f.revoked = true
		reply(200, map[string]any{})
	case !authed:
		reply(401, map[string]any{"error": "The access token is invalid"})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/accounts/verify_credentials":
		reply(200, map[string]any{"acct": "mdl_test", "locked": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/statuses":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.lastBody, f.lastKey = body, r.Header.Get("Idempotency-Key")
		if id, ok := f.byKey[f.lastKey]; ok && f.lastKey != "" {
			reply(200, f.statuses[id])
			return
		}
		f.next++
		id := fmt.Sprint(1000 + f.next)
		f.statuses[id] = map[string]any{"id": id, "url": "https://fake.example/@mdl_test/" + id,
			"visibility": body["visibility"], "text": body["status"],
			"favourites_count": 2, "reblogs_count": 1, "replies_count": 0, "quotes_count": 0}
		if f.lastKey != "" {
			f.byKey[f.lastKey] = id
		}
		reply(200, f.statuses[id])
	case strings.HasPrefix(r.URL.Path, "/api/v1/statuses/"):
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/statuses/")
		id, source := strings.CutSuffix(rest, "/source")
		st, ok := f.statuses[id]
		if !ok {
			reply(404, map[string]any{"error": "Record not found"})
			return
		}
		switch {
		case r.Method == http.MethodDelete:
			delete(f.statuses, id)
			reply(200, st)
		case source:
			reply(200, map[string]any{"id": id, "text": st["text"], "spoiler_text": ""})
		default:
			reply(200, st)
		}
	default:
		reply(404, map[string]any{"error": "Record not found"})
	}
}

// do runs calls through the wrapper against the fake and returns the checked
// response and everything it printed.
func do(t *testing.T, f *fakeInstance, creds map[string]string, config map[string]any, calls ...wp.VerbCall) (wp.WrapperResponse, string) {
	t.Helper()
	srv := httptest.NewServer(f)
	defer srv.Close()
	body, _ := json.Marshal(wp.WrapperRequest{Contract: wp.ContractVersion,
		Connection: wp.Connection{AccountID: "fake.example", Endpoint: srv.URL, Credentials: creds, Config: config}, Calls: calls})
	var out bytes.Buffer
	if err := run(context.Background(), bytes.NewReader(body), &out, srv.Client()); err != nil {
		t.Fatal(err)
	}
	resp, err := wp.ParseWrapperResponse(out.Bytes(), len(calls))
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return resp, out.String()
}

func authorized(f *fakeInstance) map[string]string {
	return map[string]string{credClientID: "cid", credClientSecret: "csecret-FAKE",
		credRedirectURI: wp.OutOfBandRedirect, credAccessToken: f.token}
}

func TestProbeAnswersADraftManifestWithTheInstancesLimits(t *testing.T) {
	f := newFake()
	f.maxChars = 1000
	resp, _ := do(t, f, authorized(f), nil, wp.VerbCall{Verb: wp.VerbProbe})
	m := resp.Results[0].Manifest
	if why := m.Check(); why != "" {
		t.Fatalf("the manifest is refused: %s", why)
	}
	if m.Entitlements["account"] != "mdl_test" {
		t.Errorf("the probe does not say whose account: %v", m.Entitlements)
	}
	if !strings.Contains(string(m.Kinds["social_post"].Shape), `"maxLength":975`) {
		t.Errorf("the shape is not refined to this instance's limit: %s", m.Kinds["social_post"].Shape)
	}
}

func TestAuthorizeRegistersOnceExchangesTheCodeAndRevokes(t *testing.T) {
	f := newFake()
	resp, _ := do(t, f, nil, nil, wp.VerbCall{Verb: wp.VerbAuthorize, Step: wp.AuthorizeBegin,
		RedirectURI: wp.OutOfBandRedirect, State: "st-123"})
	begun := resp.Results[0]
	u, err := url.Parse(begun.AuthorizeURL)
	if err != nil || u.Query().Get("state") != "st-123" || u.Query().Get("client_id") != "cid" ||
		u.Query().Get("scope") != "profile read:statuses write:statuses" {
		t.Fatalf("the authorize URL: %s", begun.AuthorizeURL)
	}
	if begun.Credentials[credClientSecret] != "csecret-FAKE" || f.apps != 1 {
		t.Fatalf("begin did not hand back the client it registered: %v, %d apps", begun.Credentials, f.apps)
	}
	// Begun again with the client in hand, nothing is registered.
	if _, _ = do(t, f, begun.Credentials, nil, wp.VerbCall{Verb: wp.VerbAuthorize, Step: wp.AuthorizeBegin,
		RedirectURI: wp.OutOfBandRedirect, State: "st-456"}); f.apps != 1 {
		t.Errorf("begin registered a second client: %d", f.apps)
	}

	bad, _ := do(t, f, begun.Credentials, nil, wp.VerbCall{Verb: wp.VerbAuthorize, Step: wp.AuthorizeComplete, Code: "stale"})
	if !strings.Contains(bad.Results[0].Refused, "did not accept the code") {
		t.Errorf("a refused code: %+v", bad.Results[0])
	}
	done, _ := do(t, f, begun.Credentials, nil, wp.VerbCall{Verb: wp.VerbAuthorize, Step: wp.AuthorizeComplete, Code: "good-code"})
	if done.Results[0].Credentials[credAccessToken] != f.token || done.Results[0].Credentials[credClientID] != "cid" {
		t.Fatalf("complete: %+v", done.Results[0])
	}

	refresh, _ := do(t, f, done.Results[0].Credentials, nil, wp.VerbCall{Verb: wp.VerbAuthorize, Step: wp.AuthorizeRefresh})
	if refresh.Results[0].Refused == "" {
		t.Error("refresh was not refused, though tokens do not expire")
	}
	if _, _ = do(t, f, done.Results[0].Credentials, nil, wp.VerbCall{Verb: wp.VerbAuthorize, Step: wp.AuthorizeRevoke}); !f.revoked {
		t.Error("revoke did not reach the instance")
	}
	again, _ := do(t, f, done.Results[0].Credentials, nil, wp.VerbCall{Verb: wp.VerbAuthorize, Step: wp.AuthorizeRevoke})
	if !again.Results[0].Succeeded() {
		t.Errorf("revoking twice: %+v", again.Results[0])
	}
}

func TestAPostsLifeFromPublishToRetract(t *testing.T) {
	f := newFake()
	payload := json.RawMessage(`{"text":"Hello from a conformance run","link":"https://mendel.build/x","media":null}`)
	pub := wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "social_post", Payload: payload,
		When: &wp.When{Mode: wp.WhenNow}, IdempotencyKey: "key-1"}
	resp, _ := do(t, f, authorized(f), nil, pub)
	ref := resp.Results[0].Ref
	if ref == "" || !strings.HasPrefix(resp.Results[0].URL, "https://") {
		t.Fatalf("publish: %+v", resp.Results[0])
	}
	if f.lastBody["visibility"] != "private" || f.lastKey != "key-1" ||
		f.lastBody["status"] != "Hello from a conformance run\n\nhttps://mendel.build/x" {
		t.Errorf("what was posted: %v, key %q", f.lastBody, f.lastKey)
	}
	// The same key is the same status.
	if again, _ := do(t, f, authorized(f), nil, pub); again.Results[0].Ref != ref {
		t.Errorf("a repeated publish made %s, not %s", again.Results[0].Ref, ref)
	}
	if resp, _ := do(t, f, authorized(f), map[string]any{"visibility": "public"},
		wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "social_post", Payload: json.RawMessage(`{"text":"public"}`)}); !resp.Results[0].Succeeded() ||
		f.lastBody["visibility"] != "public" {
		t.Errorf("configured visibility: %v", f.lastBody)
	}

	calls := []wp.VerbCall{{Verb: wp.VerbStatus, Ref: ref}, {Verb: wp.VerbReadBack, Ref: ref}, {Verb: wp.VerbReadMetrics, Ref: ref},
		{Verb: wp.VerbRetract, Ref: ref}, {Verb: wp.VerbRetract, Ref: ref}, {Verb: wp.VerbStatus, Ref: ref}}
	resp, _ = do(t, f, authorized(f), nil, calls...)
	r := resp.Results
	if r[0].Status.Effective != wp.EffectiveLive || r[0].Status.Configured != "visibility private" {
		t.Errorf("status: %+v", r[0].Status)
	}
	if got := string(r[1].Asset); got != string(payload) {
		t.Errorf("read_back %s; approved %s", got, payload)
	}
	if v := r[2].AssetMetrics["favourites"].Value; v == nil || *v != 2 || r[2].AssetMetrics["impressions"].Unavailable == "" {
		t.Errorf("read_metrics: %+v", r[2].AssetMetrics)
	}
	if r[3].Retracted != wp.RetractedDeleted || r[4].Retracted != wp.RetractedDeleted {
		t.Errorf("retracting twice: %q, %q", r[3].Retracted, r[4].Retracted)
	}
	if r[5].Status.Effective != wp.EffectiveGone {
		t.Errorf("status after retract: %+v", r[5].Status)
	}
	gone, _ := do(t, f, authorized(f), nil, wp.VerbCall{Verb: wp.VerbReadBack, Ref: ref})
	if gone.Results[0].Refused == "" {
		t.Errorf("read_back of a retracted post: %+v", gone.Results[0])
	}
}

func TestPublishRefusesWhatTheShapeDoesNotAllow(t *testing.T) {
	f := newFake()
	for name, c := range map[string]wp.VerbCall{
		"too long":       {Payload: json.RawMessage(`{"text":"` + strings.Repeat("x", 476) + `"}`)},
		"no text":        {Payload: json.RawMessage(`{"text":" "}`)},
		"an extra field": {Payload: json.RawMessage(`{"text":"hi","poll":[]}`)},
		"media":          {Payload: json.RawMessage(`{"text":"hi","link":null,"media":"a photo of the launch"}`)},
		"a bad link":     {Payload: json.RawMessage(`{"text":"hi","link":"not a url"}`)},
		"scheduled":      {Payload: json.RawMessage(`{"text":"hi"}`), When: &wp.When{Mode: wp.WhenAt}},
		"another kind":   {Payload: json.RawMessage(`{"text":"hi"}`), AssetKind: "listing"},
	} {
		c.Verb = wp.VerbPublish
		if c.AssetKind == "" {
			c.AssetKind = "social_post"
		}
		resp, _ := do(t, f, authorized(f), nil, c)
		if resp.Results[0].Refused == "" {
			t.Errorf("%s: %+v", name, resp.Results[0])
		}
	}
	if f.next != 0 {
		t.Errorf("%d posts were made by refused calls", f.next)
	}
}

// Nothing the wrapper prints carries a credential, except authorize's own
// answer, where the protocol keeps them apart.
func TestCredentialsReachStdoutOnlyInAuthorizesAnswer(t *testing.T) {
	f := newFake()
	_, out := do(t, f, authorized(f), nil, wp.VerbCall{Verb: wp.VerbProbe},
		wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "social_post", Payload: json.RawMessage(`{"text":"hi"}`)})
	for _, secret := range []string{f.token, "csecret-FAKE"} {
		if strings.Contains(out, secret) {
			t.Errorf("a credential was printed outside authorize: %s", out)
		}
	}
	f.token = "tok-REJECTED"
	_, out = do(t, f, map[string]string{credAccessToken: "tok-WRONG-SECRET"}, nil, wp.VerbCall{Verb: wp.VerbReadBack, Ref: "1"})
	if strings.Contains(out, "tok-WRONG-SECRET") {
		t.Errorf("a failure quoted the token: %s", out)
	}
}

func TestWrapperJSONAgreesWithWhatTheWrapperAnswers(t *testing.T) {
	raw, err := os.ReadFile("wrapper.json")
	if err != nil {
		t.Fatal(err)
	}
	var d wp.Description
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		t.Fatal(err)
	}
	if why := d.Check(); why != "" {
		t.Fatal(why)
	}
	dockerfile, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	var entrypoint []string
	for _, line := range strings.Split(string(dockerfile), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ENTRYPOINT "); ok {
			if err := json.Unmarshal([]byte(rest), &entrypoint); err != nil {
				t.Fatalf("the ENTRYPOINT is not in exec form: %s", rest)
			}
		}
	}
	if strings.Join(entrypoint, "\x00") != strings.Join(d.Command, "\x00") || len(entrypoint) == 0 {
		t.Errorf("wrapper.json's command is %q; the Dockerfile's ENTRYPOINT is %q", d.Command, entrypoint)
	}
	if d.Version != wrapperVersion || d.Contract != wp.ContractVersion || d.SpecSource != specSource || !d.Connection.Authorize {
		t.Errorf("wrapper.json says %q %q %q authorize=%v", d.Version, d.Contract, d.SpecSource, d.Connection.Authorize)
	}
	// The one setting the wrapper reads is declared, with the default it
	// applies, and Mendel will send only the values it declares.
	spec := d.Connection
	if len(spec.Config) != 1 || spec.Config[0].Name != "visibility" || spec.Config[0].Default != "private" {
		t.Errorf("wrapper.json's config is %+v; the wrapper reads visibility and defaults to private", spec.Config)
	}
	if why := spec.CheckConfig(map[string]any{"visibility": "direct"}); why == "" {
		t.Error("direct is declared, and a direct post that mentions nobody reaches nobody")
	}
	names := map[string]bool{}
	for _, c := range d.Connection.Credentials {
		names[c.Name] = true
	}
	for _, n := range []string{credClientID, credClientSecret, credRedirectURI, credAccessToken} {
		if !names[n] {
			t.Errorf("wrapper.json does not name %s, which authorize produces", n)
		}
	}
	for verb, claimed := range d.Claims {
		got, ok := verbs()[wp.Verb(verb)]
		if !ok || (claimed != "declined" && got.Level == wp.VerbDeclined) {
			t.Errorf("wrapper.json claims %s %s; the probe answers %+v", verb, claimed, got)
		}
	}
}
