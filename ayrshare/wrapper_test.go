package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mendelbuild/mendel-integrations/ayrshare/fakeapi"
	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

// fake serves the fake under a path prefix, and records every path asked, so a
// test can show the wrapper kept the endpoint's path.
type fake struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

const prefix = "/mock/ayrshare/api"

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{}
	inner := http.StripPrefix(prefix, fakeapi.Handler())
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fake) run(t *testing.T, calls ...wp.VerbCall) []wp.VerbResult {
	t.Helper()
	req := wp.WrapperRequest{Contract: "2", Calls: calls, Connection: wp.Connection{
		AccountID: "acme", Endpoint: f.URL + prefix, Credentials: map[string]string{credentialName: "made-up-key-123456"}}}
	resp := run(req, nil)
	raw, _ := json.Marshal(resp)
	if _, err := wp.ParseWrapperResponse(raw, len(calls)); err != nil {
		t.Fatalf("the response is not one Mendel accepts: %v: %s", err, raw)
	}
	return resp.Results
}

func (f *fake) one(t *testing.T, c wp.VerbCall) wp.VerbResult {
	t.Helper()
	return f.run(t, c)[0]
}

func good(t *testing.T, r wp.VerbResult) wp.VerbResult {
	t.Helper()
	if !r.Succeeded() {
		t.Fatalf("%s: %s", r.Verb, r.Why())
	}
	return r
}

func payload(text string, link any, media any) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"text": text, "link": link, "media": media})
	return b
}

func TestManifestIsWellFormed(t *testing.T) {
	m := manifest()
	if why := m.Check(); why != "" {
		t.Fatal(why)
	}
	// Probe answers it, against the fake.
	f := newFake(t)
	res := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbProbe}))
	if why := res.Manifest.Check(); why != "" {
		t.Fatal(why)
	}
	honoured := []wp.Verb{wp.VerbDraft, wp.VerbListOwned, wp.VerbProbe, wp.VerbPublish, wp.VerbReadBack, wp.VerbRetract, wp.VerbStatus}
	for _, v := range wp.ContractVerbs() {
		want := wp.VerbDeclined
		for _, h := range honoured {
			if h == v {
				want = "honoured"
			}
		}
		got := m.Verbs[v].Level
		if (want == "honoured") == (got == wp.VerbDeclined) {
			t.Errorf("verb %s is %s", v, got)
		}
	}
	if _, ok := m.Kinds["social_post"]; !ok || len(m.Kinds) != 1 {
		t.Errorf("kinds = %v", m.Kinds)
	}
}

func TestWrapperJSONAgreesWithTheWrapper(t *testing.T) {
	raw, err := os.ReadFile("wrapper.json")
	if err != nil {
		t.Fatal(err)
	}
	var d wp.Description
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if why := d.Check(); why != "" {
		t.Fatal(why)
	}
	m := manifest()
	if d.Tool.Slug != "ayrshare" || d.Tool.Name != "Ayrshare" || d.Version != m.Wrapper.Version ||
		d.Contract != m.Contract || d.Image != "mendel-tool-ayrshare:dev" || d.SpecSource != m.Wrapper.SpecSource {
		t.Errorf("wrapper.json disagrees with the manifest: %+v", d)
	}
	for verb, level := range d.Claims {
		got := m.Verbs[wp.Verb(verb)].Level
		if got == wp.VerbDeclined || (level == "supported" && got != wp.VerbSupported) {
			t.Errorf("wrapper.json claims %s %s, the probe answers %s", verb, level, got)
		}
	}
	creds := false
	for _, c := range d.Connection.Credentials {
		creds = creds || c.Name == credentialName
	}
	if !creds {
		t.Errorf("wrapper.json does not ask for %s", credentialName)
	}
	df, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	mm := regexp.MustCompile(`(?m)^ENTRYPOINT\s+(\[.*\])\s*$`).FindSubmatch(df)
	if mm == nil {
		t.Fatal("the Dockerfile has no exec-form ENTRYPOINT")
	}
	var entry []string
	if err := json.Unmarshal(mm[1], &entry); err != nil || !reflect.DeepEqual(entry, d.Command) {
		t.Errorf("ENTRYPOINT %v, command %v", entry, d.Command)
	}
}

func TestSpecHash(t *testing.T) {
	names := []string{"01-docs-ayrshare-com-rest-api-endpoints-post.txt", "02-www-ayrshare-com-docs-media-guidelines-reddit.txt",
		"03-www-ayrshare-com-docs-whatsnew-archive.txt"}
	h := sha256.New()
	for _, n := range names {
		b, err := os.ReadFile("../spec/ayrshare/" + n)
		if err != nil {
			t.Skip("the spec is not beside the wrapper")
		}
		h.Write(b)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != specHash {
		t.Errorf("specHash is %s, the spec hashes to %s", specHash, got)
	}
}

func TestPublishReadBackRetract(t *testing.T) {
	f := newFake(t)
	pl := payload("Which stroller did you pick?\n\nAsking before we buy.", "https://mendel.build/?x=1", nil)
	key := "key-1"
	pub := wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "social_post", Payload: pl, IdempotencyKey: key,
		Name: "mendel-t-1-post", When: &wp.When{Mode: wp.WhenNow}}
	first := good(t, f.one(t, pub))
	if first.Ref == "" || first.URL == "" {
		t.Fatalf("published as %+v", first)
	}
	again := good(t, f.one(t, pub))
	if again.Ref != first.Ref {
		t.Errorf("the same key made %s beside %s", again.Ref, first.Ref)
	}

	st := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbStatus, Ref: first.Ref}))
	if st.Status.Effective != wp.EffectiveLive {
		t.Errorf("status %+v", st.Status)
	}
	rb := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbReadBack, Ref: first.Ref}))
	var want, got map[string]any
	_ = json.Unmarshal(pl, &want)
	_ = json.Unmarshal(rb.Asset, &got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back %s, published %s", rb.Asset, pl)
	}

	own := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbListOwned, Prefix: "mendel-t-1"}))
	if !reflect.DeepEqual(own.Owned, []string{first.Ref}) {
		t.Errorf("owned %v", own.Owned)
	}
	if other := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbListOwned, Prefix: "mendel-t-2"})); len(other.Owned) != 0 {
		t.Errorf("owned for another prefix: %v", other.Owned)
	}

	rs := f.run(t, wp.VerbCall{Verb: wp.VerbRetract, Ref: first.Ref}, wp.VerbCall{Verb: wp.VerbRetract, Ref: first.Ref})
	if len(rs) != 2 || rs[0].Retracted != "deleted" || rs[1].Retracted != "deleted" {
		t.Fatalf("retract twice: %+v", rs)
	}
	if st := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbStatus, Ref: first.Ref})); st.Status.Effective != wp.EffectiveGone {
		t.Errorf("status after retract %+v", st.Status)
	}
	if r := f.one(t, wp.VerbCall{Verb: wp.VerbReadBack, Ref: first.Ref}); r.Refused == "" {
		t.Errorf("read_back of a retracted post: %+v", r)
	}
	if own := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbListOwned, Prefix: "mendel-t-1"})); len(own.Owned) != 0 {
		t.Errorf("a retracted post is listed: %v", own.Owned)
	}
	// The key of a retracted post is not reusable at Ayrshare; the wrapper says so.
	if r := f.one(t, pub); r.Refused == "" {
		t.Errorf("a key of a retracted post: %+v", r)
	}
}

func TestDraftIsNotLiveUntilPublished(t *testing.T) {
	f := newFake(t)
	pl := payload("Is a 529 plan worth it?", nil, []string{"https://img.example.com/a.png"})
	d := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbDraft, AssetKind: "social_post", Payload: pl, Name: "mendel-d-draft"}))
	st := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbStatus, Ref: d.Ref}))
	if st.Status.Effective != wp.EffectiveNotLive || st.Status.Review != "awaiting approval" {
		t.Fatalf("a draft's status is %+v", st.Status)
	}
	if st.URL != "" {
		t.Errorf("a draft has a URL %s", st.URL)
	}
	if own := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbListOwned, Prefix: "mendel-d"})); len(own.Owned) != 1 {
		t.Errorf("a draft is not listed: %v", own.Owned)
	}
	pub := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbPublish, Ref: d.Ref, When: &wp.When{Mode: wp.WhenNow}}))
	if pub.Ref != d.Ref || pub.URL == "" {
		t.Errorf("published the draft as %+v", pub)
	}
	if st := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbStatus, Ref: d.Ref})); st.Status.Effective != wp.EffectiveLive {
		t.Errorf("status after publish %+v", st.Status)
	}
	rb := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbReadBack, Ref: d.Ref}))
	var got map[string]any
	_ = json.Unmarshal(rb.Asset, &got)
	if !reflect.DeepEqual(got["media"], []any{"https://img.example.com/a.png"}) || got["link"] != nil {
		t.Errorf("read back %s", rb.Asset)
	}
	good(t, f.one(t, wp.VerbCall{Verb: wp.VerbRetract, Ref: d.Ref}))
	// A gone post is not published.
	if r := f.one(t, wp.VerbCall{Verb: wp.VerbPublish, Ref: d.Ref}); r.Refused == "" {
		t.Errorf("publishing a retracted draft: %+v", r)
	}
}

func TestDraftRetractedWithoutEverGoingLive(t *testing.T) {
	f := newFake(t)
	d := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbDraft, AssetKind: "social_post", Payload: payload("x", nil, nil)}))
	if r := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbRetract, Ref: d.Ref})); r.Retracted != "deleted" {
		t.Errorf("%+v", r)
	}
}

func TestScheduledPublishIsNotLiveYet(t *testing.T) {
	f := newFake(t)
	at := time.Now().Add(48 * time.Hour)
	p := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "social_post", Payload: payload("later", nil, nil),
		When: &wp.When{Mode: wp.WhenAt, At: &at}}))
	st := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbStatus, Ref: p.Ref}))
	if st.Status.Effective != wp.EffectiveNotLive || !strings.HasPrefix(st.Status.Configured, "scheduled") {
		t.Errorf("status %+v", st.Status)
	}
	// A draft published at an instant is scheduled, not sent.
	d := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbDraft, AssetKind: "social_post", Payload: payload("later too", nil, nil)}))
	good(t, f.one(t, wp.VerbCall{Verb: wp.VerbPublish, Ref: d.Ref, When: &wp.When{Mode: wp.WhenAt, At: &at}}))
	if st := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbStatus, Ref: d.Ref})); st.Status.Effective != wp.EffectiveNotLive {
		t.Errorf("a draft scheduled for later is %+v", st.Status)
	}
}

func TestBoundariesAreRefusedAndPostNothing(t *testing.T) {
	f := newFake(t)
	bad := map[string]json.RawMessage{
		"missing text":   json.RawMessage(`{"link":"https://mendel.build/","media":null}`),
		"too long":       payload(strings.Repeat("x", maxText+1), nil, nil),
		"empty text":     payload("  ", nil, nil),
		"bad link":       payload("x", "not a url", nil),
		"video":          payload("x", nil, []string{"https://v.example.com/a.mp4"}),
		"empty media":    payload("x", nil, []string{}),
		"extra field":    json.RawMessage(`{"text":"x","link":null,"media":null,"title":"y"}`),
		"no media field": json.RawMessage(`{"text":"x","link":null}`),
		"not an object":  json.RawMessage(`"x"`),
	}
	for name, pl := range bad {
		r := f.one(t, wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "social_post", Payload: pl, IdempotencyKey: "k-" + name})
		if r.Refused == "" {
			t.Errorf("%s: %+v", name, r)
		}
		if r := f.one(t, wp.VerbCall{Verb: wp.VerbDraft, AssetKind: "social_post", Payload: pl}); r.Refused == "" {
			t.Errorf("draft, %s: %+v", name, r)
		}
	}
	if r := f.one(t, wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "listing", Payload: payload("x", nil, nil)}); r.Refused == "" {
		t.Errorf("another kind: %+v", r)
	}
	if r := f.one(t, wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "social_post", Payload: payload("x", nil, nil),
		When: &wp.When{Mode: wp.WhenAnnounce}}); r.Refused == "" {
		t.Errorf("announce: %+v", r)
	}
	if own := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbListOwned})); len(own.Owned) != 0 {
		t.Errorf("something was posted: %v", own.Owned)
	}
}

func TestDeclinedVerbsAreRefusedWithTheirReason(t *testing.T) {
	f := newFake(t)
	for v, why := range declined {
		r := f.one(t, wp.VerbCall{Verb: v})
		if r.Refused != why || r.Failed != "" {
			t.Errorf("%s: %+v", v, r)
		}
	}
}

func TestEveryRequestGoesToTheEndpointKeepingItsPath(t *testing.T) {
	f := newFake(t)
	d := good(t, f.one(t, wp.VerbCall{Verb: wp.VerbDraft, AssetKind: "social_post", Payload: payload("x", nil, nil), Name: "n"}))
	f.run(t, wp.VerbCall{Verb: wp.VerbProbe}, wp.VerbCall{Verb: wp.VerbStatus, Ref: d.Ref},
		wp.VerbCall{Verb: wp.VerbPublish, Ref: d.Ref}, wp.VerbCall{Verb: wp.VerbListOwned, Prefix: "n"},
		wp.VerbCall{Verb: wp.VerbReadBack, Ref: d.Ref}, wp.VerbCall{Verb: wp.VerbRetract, Ref: d.Ref})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.paths) < 8 {
		t.Fatalf("only %d requests", len(f.paths))
	}
	for _, p := range f.paths {
		if !strings.HasPrefix(p, prefix+"/post") && !strings.HasPrefix(p, prefix+"/history") {
			t.Errorf("a request went to %s", p)
		}
	}
}

func TestFailuresAndRefusalsStopTheRun(t *testing.T) {
	f := newFake(t)
	rs := f.run(t, wp.VerbCall{Verb: wp.VerbStatus, Ref: "nope"}, wp.VerbCall{Verb: wp.VerbProbe})
	if len(rs) != 1 || rs[0].Refused == "" {
		t.Errorf("an unknown ref: %+v", rs)
	}
	// An unreachable endpoint fails, and does not leak the key.
	req := wp.WrapperRequest{Contract: "2", Calls: []wp.VerbCall{{Verb: wp.VerbProbe}},
		Connection: wp.Connection{Endpoint: "http://127.0.0.1:1/x", Credentials: map[string]string{credentialName: "secret-key-abcdef"}}}
	resp := run(req, nil)
	if len(resp.Results) != 1 || resp.Results[0].Failed == "" || strings.Contains(resp.Results[0].Failed, "secret-key-abcdef") {
		t.Errorf("%+v", resp)
	}
	// A request for another contract is failed at its first call.
	req.Contract = "1"
	if resp := run(req, nil); len(resp.Results) != 1 || resp.Results[0].Failed == "" {
		t.Errorf("%+v", resp)
	}
	// No key, no call.
	req.Contract, req.Connection.Credentials = "2", nil
	if resp := run(req, nil); len(resp.Results) != 1 || resp.Results[0].Failed == "" {
		t.Errorf("%+v", resp)
	}
}

func TestRejectedKeyFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status":"error","code":101,"message":"Invalid API Key"}`))
	}))
	defer srv.Close()
	req := wp.WrapperRequest{Contract: "2", Calls: []wp.VerbCall{{Verb: wp.VerbProbe}},
		Connection: wp.Connection{Endpoint: srv.URL, Credentials: map[string]string{credentialName: "k-123456789"}}}
	resp := run(req, nil)
	if len(resp.Results) != 1 || resp.Results[0].Failed == "" {
		t.Errorf("%+v", resp)
	}
}

func TestTextRoundTripsLineBreaks(t *testing.T) {
	for _, s := range []string{"a\nb", "a\u2063\nb", "\n\n", "plain", "x\u2063"} {
		if got := decodeText(encodeText(s)); got != s {
			t.Errorf("%q came back as %q", s, got)
		}
	}
}
