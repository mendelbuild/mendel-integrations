package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
	"github.com/mendelbuild/mendel-integrations/gmail-api/fakeapi"
)

func conn(endpoint string) wp.Connection {
	return wp.Connection{AccountID: "owner@example.com", Endpoint: endpoint,
		Credentials: map[string]string{tokenCredential: "made-up-token-0123456789"}}
}

// do runs the calls against a fake and returns the results.
func do(t *testing.T, endpoint string, calls ...wp.VerbCall) []wp.VerbResult {
	t.Helper()
	req := wp.WrapperRequest{Contract: "2", Connection: conn(endpoint), Calls: calls}
	resp := run(context.Background(), req)
	raw, _ := json.Marshal(resp)
	if _, err := wp.ParseWrapperResponse(raw, len(calls)); err != nil {
		t.Fatalf("the protocol refuses the answer: %v\n%s", err, raw)
	}
	return resp.Results
}

func one(t *testing.T, endpoint string, call wp.VerbCall) wp.VerbResult {
	t.Helper()
	return do(t, endpoint, call)[0]
}

func payload(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func msg(recipient, subject, body string) map[string]any {
	return map[string]any{"recipient": recipient, "subject": subject, "body": body}
}

func publishCall(p any) wp.VerbCall {
	return wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "direct_message", Payload: payload(p),
		When: &wp.When{Mode: wp.WhenNow}, IdempotencyKey: "k1"}
}

func TestProbeManifestIsAccepted(t *testing.T) {
	srv := httptest.NewServer(fakeapi.Handler())
	defer srv.Close()
	res := one(t, srv.URL, wp.VerbCall{Verb: wp.VerbProbe})
	if !res.Succeeded() {
		t.Fatal(res.Why())
	}
	if why := res.Manifest.Check(); why != "" {
		t.Fatal(why)
	}
	m := res.Manifest
	for _, v := range []wp.Verb{wp.VerbProbe, wp.VerbDraft, wp.VerbPublish, wp.VerbReadBack} {
		if m.Verbs[v].Level == wp.VerbDeclined {
			t.Errorf("%s is declined", v)
		}
	}
	for _, v := range []wp.Verb{wp.VerbAuthorize, wp.VerbStatus, wp.VerbAppendUpdate, wp.VerbRetract, wp.VerbReadMetrics,
		wp.VerbSetCap, wp.VerbListOwned, wp.VerbReadSeries, wp.VerbReadTotal, wp.VerbSearch} {
		if m.Verbs[v].Level != wp.VerbDeclined || m.Verbs[v].Reason == "" {
			t.Errorf("%s should be declined with a reason: %+v", v, m.Verbs[v])
		}
	}
	for _, v := range []wp.Verb{wp.VerbStatus, wp.VerbRetract, wp.VerbReadMetrics, wp.VerbListOwned} {
		if !strings.Contains(m.Verbs[v].Reason, "documentation was not found") {
			t.Errorf("%s's reason does not say its documentation was not found: %s", v, m.Verbs[v].Reason)
		}
	}
	if m.Kinds["direct_message"].Level != wp.VerbSupported || len(m.Kinds) != 1 {
		t.Errorf("kinds: %+v", m.Kinds)
	}
	if m.Idempotency != "none" {
		t.Errorf("idempotency %q", m.Idempotency)
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
	if d.Tool.Slug != "gmail-api" || d.Tool.Name != "Gmail API" || d.Version != "0.1.0" || d.Contract != "2" ||
		d.Image != "mendel-tool-gmail-api:dev" {
		t.Errorf("wrapper.json: %+v", d)
	}
	m := manifest()
	if d.Version != m.Wrapper.Version || d.Contract != m.Contract || d.SpecSource != m.Wrapper.SpecSource {
		t.Error("wrapper.json and the manifest disagree on version, contract or spec source")
	}
	for verb, level := range d.Claims {
		got := m.Verbs[wp.Verb(verb)].Level
		if got == wp.VerbDeclined && level != "declined" {
			t.Errorf("wrapper.json claims %s %s, and the probe declines it", verb, level)
		}
	}
	// command is the Dockerfile's ENTRYPOINT.
	df, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	mm := regexp.MustCompile(`(?m)^ENTRYPOINT\s+(\[.*\])\s*$`).FindSubmatch(df)
	if mm == nil {
		t.Fatal("no exec-form ENTRYPOINT in the Dockerfile")
	}
	var entry []string
	if err := json.Unmarshal(mm[1], &entry); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(entry, d.Command) {
		t.Errorf("command %v is not the ENTRYPOINT %v", d.Command, entry)
	}
}

func TestPublishAndReadBackRoundTrip(t *testing.T) {
	srv := httptest.NewServer(fakeapi.Handler())
	defer srv.Close()
	cases := []map[string]any{
		msg("friend@example.com", "Hello there", "Hi,\n\nA short note.\n"),
		msg("friend+tag@example.com", "Café — ünïcode ✓ and a much longer subject than one encoded word can carry, "+
			strings.Repeat("ß", 40), "Ünïcode body ✓\r\nline two\n\n\n  indented, trailing space "),
		msg("a@b.example", "=?utf-8?q?looks encoded?=", strings.Repeat("long line ", 100)),
	}
	for _, want := range cases {
		pub := one(t, srv.URL, publishCall(want))
		if !pub.Succeeded() || !strings.HasPrefix(pub.Ref, "message:") {
			t.Fatalf("publish: %+v", pub)
		}
		back := one(t, srv.URL, wp.VerbCall{Verb: wp.VerbReadBack, Ref: pub.Ref})
		if !back.Succeeded() {
			t.Fatal(back.Why())
		}
		var got map[string]any
		if err := json.Unmarshal(back.Asset, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("read back %v; sent %v", got, want)
		}
	}
}

func TestDraftIsNotSentUntilPublished(t *testing.T) {
	fake := fakeapi.Handler()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	want := msg("friend@example.com", "Draft subject", "Draft body")
	d := one(t, srv.URL, wp.VerbCall{Verb: wp.VerbDraft, AssetKind: "direct_message", Payload: payload(want), Name: "x-draft"})
	if !d.Succeeded() || !strings.HasPrefix(d.Ref, "draft:") {
		t.Fatalf("draft: %+v", d)
	}
	if n := countLabel(t, srv.URL, "SENT"); n != 0 {
		t.Fatalf("%d messages sent by a draft", n)
	}
	back := one(t, srv.URL, wp.VerbCall{Verb: wp.VerbReadBack, Ref: d.Ref})
	var got map[string]any
	_ = json.Unmarshal(back.Asset, &got)
	if !back.Succeeded() || !reflect.DeepEqual(got, want) {
		t.Fatalf("draft read back %s (%s)", back.Asset, back.Why())
	}
	pub := one(t, srv.URL, wp.VerbCall{Verb: wp.VerbPublish, Ref: d.Ref, When: &wp.When{Mode: wp.WhenNow}})
	if !pub.Succeeded() || !strings.HasPrefix(pub.Ref, "message:") {
		t.Fatalf("publish by ref: %+v", pub)
	}
	if n := countLabel(t, srv.URL, "SENT"); n != 1 {
		t.Fatalf("%d messages sent after publishing a draft; want 1", n)
	}
	// The draft is gone, as the drafts guide says: read back and publish are refused.
	for _, c := range []wp.VerbCall{{Verb: wp.VerbReadBack, Ref: d.Ref}, {Verb: wp.VerbPublish, Ref: d.Ref}} {
		if r := one(t, srv.URL, c); r.Refused == "" {
			t.Errorf("%s of a sent draft was not refused: %+v", c.Verb, r)
		}
	}
	back = one(t, srv.URL, wp.VerbCall{Verb: wp.VerbReadBack, Ref: pub.Ref})
	got = nil
	_ = json.Unmarshal(back.Asset, &got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sent message read back %s", back.Asset)
	}
}

// countLabel counts the messages the fake holds with a label, through the
// documented messages.list and messages.get.
func countLabel(t *testing.T, endpoint, label string) int {
	t.Helper()
	cl := newClient(conn(endpoint))
	var list struct {
		Messages []struct{ ID string } `json:"messages"`
	}
	if err := cl.do(context.Background(), http.MethodGet, "/messages", nil, nil, &list); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range list.Messages {
		var got messageResource
		if err := cl.do(context.Background(), http.MethodGet, "/messages/"+m.ID, map[string][]string{"format": {"minimal"}}, nil, &got); err != nil {
			t.Fatal(err)
		}
		for _, l := range got.LabelIDs {
			if l == label {
				n++
			}
		}
	}
	return n
}

func TestRefusals(t *testing.T) {
	srv := httptest.NewServer(fakeapi.Handler())
	defer srv.Close()
	long := strings.Repeat("x", maxSubject+1)
	bad := map[string]any{
		"no subject":          map[string]any{"recipient": "a@example.com", "body": "b"},
		"no recipient":        map[string]any{"subject": "s", "body": "b"},
		"no body":             map[string]any{"recipient": "a@example.com", "subject": "s"},
		"subject too long":    msg("a@example.com", long, "b"),
		"empty body":          msg("a@example.com", "s", ""),
		"two recipients":      msg("a@example.com, b@example.com", "s", "b"),
		"display name":        msg("A <a@example.com>", "s", "b"),
		"header injection":    msg("a@example.com\r\nBcc: x@example.com", "s", "b"),
		"subject line break":  msg("a@example.com", "s\r\nBcc: x@example.com", "b"),
		"subject trailing sp": msg("a@example.com", "s ", "b"),
		"not an address":      msg("nobody", "s", "b"),
		"not text":            map[string]any{"recipient": "a@example.com", "subject": 3, "body": "b"},
		"unknown field":       map[string]any{"recipient": "a@example.com", "subject": "s", "body": "b", "cc": "c@example.com"},
	}
	for name, p := range bad {
		for _, verb := range []wp.Verb{wp.VerbPublish, wp.VerbDraft} {
			res := one(t, srv.URL, wp.VerbCall{Verb: verb, AssetKind: "direct_message", Payload: payload(p), When: &wp.When{Mode: wp.WhenNow}})
			if res.Refused == "" {
				t.Errorf("%s (%s) was not refused: %+v", name, verb, res)
			}
		}
	}
	if n := countLabel(t, srv.URL, "SENT"); n != 0 {
		t.Errorf("%d messages were sent by refused publishes", n)
	}
	ok := msg("a@example.com", "s", "b")
	for name, c := range map[string]wp.VerbCall{
		"another kind":  {Verb: wp.VerbPublish, AssetKind: "social_post", Payload: payload(ok)},
		"scheduled":     {Verb: wp.VerbPublish, AssetKind: "direct_message", Payload: payload(ok), When: &wp.When{Mode: wp.WhenAt}},
		"announced":     {Verb: wp.VerbPublish, AssetKind: "direct_message", Payload: payload(ok), When: &wp.When{Mode: wp.WhenAnnounce}},
		"no payload":    {Verb: wp.VerbPublish, AssetKind: "direct_message"},
		"a foreign ref": {Verb: wp.VerbReadBack, Ref: "nonsense"},
		"missing msg":   {Verb: wp.VerbReadBack, Ref: "message:00ff"},
	} {
		if res := one(t, srv.URL, c); res.Refused == "" {
			t.Errorf("%s was not refused: %+v", name, res)
		}
	}
	if n := countLabel(t, srv.URL, "SENT"); n != 0 {
		t.Errorf("%d messages were sent", n)
	}
}

func TestDeclinedVerbsAreRefusedWithTheirReason(t *testing.T) {
	srv := httptest.NewServer(fakeapi.Handler())
	defer srv.Close()
	for _, v := range []wp.Verb{wp.VerbAuthorize, wp.VerbStatus, wp.VerbAppendUpdate, wp.VerbRetract, wp.VerbReadMetrics,
		wp.VerbSetCap, wp.VerbListOwned, wp.VerbReadSeries, wp.VerbReadTotal, wp.VerbSearch, "not_a_verb"} {
		res := one(t, srv.URL, wp.VerbCall{Verb: v})
		if res.Refused == "" || res.Failed != "" {
			t.Errorf("%s: %+v", v, res)
		}
	}
	if r := one(t, srv.URL, wp.VerbCall{Verb: wp.VerbRetract, Ref: "message:1"}); !strings.Contains(r.Refused, "documentation was not found") {
		t.Errorf("retract refusal: %s", r.Refused)
	}
}

func TestStopsAtTheFirstThatDoesNotSucceed(t *testing.T) {
	srv := httptest.NewServer(fakeapi.Handler())
	defer srv.Close()
	res := do(t, srv.URL, wp.VerbCall{Verb: wp.VerbProbe}, wp.VerbCall{Verb: wp.VerbRetract, Ref: "x"}, wp.VerbCall{Verb: wp.VerbProbe})
	if len(res) != 2 || res[1].Refused == "" {
		t.Fatalf("results: %+v", res)
	}
}

func TestAnotherContractIsAnsweredWithoutActing(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	resp := run(context.Background(), wp.WrapperRequest{Contract: "1", Connection: conn(srv.URL),
		Calls: []wp.VerbCall{{Verb: wp.VerbProbe}}})
	if len(resp.Results) != 1 || resp.Results[0].Failed == "" || hits != 0 {
		t.Fatalf("%+v, %d requests", resp, hits)
	}
}

// Every request goes to the endpoint, keeping its path.
func TestRequestsGoToTheEndpointKeepingItsPath(t *testing.T) {
	var mu sync.Mutex
	var paths, auth []string
	inner := fakeapi.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	defer srv.Close()
	endpoint := srv.URL + "/some/prefix/"
	pub := one(t, endpoint, publishCall(msg("a@example.com", "s", "b")))
	if !pub.Succeeded() {
		t.Fatal(pub.Why())
	}
	d := one(t, endpoint, wp.VerbCall{Verb: wp.VerbDraft, AssetKind: "direct_message", Payload: payload(msg("a@example.com", "s", "b"))})
	one(t, endpoint, wp.VerbCall{Verb: wp.VerbPublish, Ref: d.Ref})
	one(t, endpoint, wp.VerbCall{Verb: wp.VerbReadBack, Ref: pub.Ref})
	one(t, endpoint, wp.VerbCall{Verb: wp.VerbProbe})
	if len(paths) < 5 {
		t.Fatalf("paths %v", paths)
	}
	for i, p := range paths {
		if !strings.HasPrefix(p, "/some/prefix/gmail/v1/users/owner@example.com/") {
			t.Errorf("request %d went to %s", i, p)
		}
		if auth[i] != "Bearer made-up-token-0123456789" {
			t.Errorf("request %d carried %q", i, auth[i])
		}
	}
}

func TestFailures(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":401,"message":"Invalid Credentials","status":"UNAUTHENTICATED"}}`))
	}))
	defer broken.Close()
	for _, c := range []wp.VerbCall{{Verb: wp.VerbProbe}, publishCall(msg("a@example.com", "s", "b"))} {
		res := one(t, broken.URL, c)
		if res.Failed == "" || res.Refused != "" || !strings.Contains(res.Failed, "Invalid Credentials") {
			t.Errorf("%s: %+v", c.Verb, res)
		}
		if strings.Contains(res.Failed, "made-up-token") {
			t.Error("the credential is in the failure")
		}
	}
}

func TestFakeAnswersLikeGmail(t *testing.T) {
	srv := httptest.NewServer(fakeapi.Handler())
	defer srv.Close()
	cl := newClient(conn(srv.URL))
	ctx := context.Background()
	// A message with no recipient is not sent.
	_, err := cl.sendMessage(ctx, []byte("Subject: x\r\n\r\nbody"))
	if e, ok := err.(*apiError); !ok || e.Status != 400 {
		t.Errorf("send without recipient: %v", err)
	}
	// An unknown id is a 404.
	if _, err := cl.getMessageRaw(ctx, "nope"); err == nil || !err.(*apiError).notFound() {
		t.Errorf("get unknown: %v", err)
	}
	if _, err := cl.sendDraft(ctx, "nope"); err == nil || !err.(*apiError).notFound() {
		t.Errorf("send unknown draft: %v", err)
	}
	// A draft's message has the DRAFT label; a sent one has SENT and a new id.
	d, err := cl.createDraft(ctx, composeMIME("", directMessage{"a@example.com", "s", "b"}))
	if err != nil || d.ID == "" || !reflect.DeepEqual(d.Message.LabelIDs, []string{"DRAFT"}) {
		t.Fatalf("draft: %+v %v", d, err)
	}
	sent, err := cl.sendDraft(ctx, d.ID)
	if err != nil || sent.ID == d.Message.ID || !reflect.DeepEqual(sent.LabelIDs, []string{"SENT"}) {
		t.Fatalf("sent: %+v %v (draft message %s)", sent, err, d.Message.ID)
	}
	if _, err := cl.getDraftRaw(ctx, d.ID); err == nil {
		t.Error("the draft is still held after it was sent")
	}
}
