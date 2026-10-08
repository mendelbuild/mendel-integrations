package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
	"github.com/mendelbuild/mendel-integrations/resend/fakeapi"
)

type env struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	auth  []string
}

// newEnv serves the fake under a path prefix, recording what is requested.
func newEnv(t *testing.T) *env {
	e := &env{}
	fake := fakeapi.Handler()
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.paths = append(e.paths, r.URL.Path)
		e.auth = append(e.auth, r.Header.Get("Authorization"))
		e.mu.Unlock()
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) conn() wp.Connection {
	return wp.Connection{AccountID: "acme.example", Endpoint: e.srv.URL + "/relay/v1/",
		Credentials: map[string]string{"RESEND_API_KEY": "re_made_up_key"}}
}

func (e *env) run(t *testing.T, calls ...wp.VerbCall) []wp.VerbResult {
	t.Helper()
	req := wp.WrapperRequest{Contract: "2", Connection: e.conn(), Calls: calls}
	resp := run(req, e.srv.Client())
	raw, _ := json.Marshal(resp)
	if _, err := wp.ParseWrapperResponse(raw, len(calls)); err != nil && len(resp.Results) == len(calls) {
		t.Fatalf("response not accepted: %v", err)
	}
	return resp.Results
}

func (e *env) one(t *testing.T, c wp.VerbCall) wp.VerbResult {
	t.Helper()
	r := e.run(t, c)
	if len(r) != 1 {
		t.Fatalf("want one result, got %d", len(r))
	}
	return r[0]
}

func payload(recipient, subject string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"recipient": recipient, "subject": subject, "body": "Hi, how is it going so far?"})
	return b
}

func publishCall(name, key string) wp.VerbCall {
	return wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "direct_message", Name: name, IdempotencyKey: key,
		Payload: payload("family@example.com", "Two weeks in"), When: &wp.When{Mode: wp.WhenNow}}
}

func TestManifestIsAcceptedAndAgreesWithWrapperJSON(t *testing.T) {
	if why := manifest().Check(); why != "" {
		t.Fatal(why)
	}
	body, err := os.ReadFile("wrapper.json")
	if err != nil {
		t.Fatal(err)
	}
	var d wp.Description
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	if why := d.Check(); why != "" {
		t.Fatal(why)
	}
	m := manifest()
	if d.Tool.Slug != "resend" || d.Version != m.Wrapper.Version || d.Contract != m.Contract || d.SpecSource != m.Wrapper.SpecSource {
		t.Fatalf("wrapper.json and the manifest disagree: %+v", d)
	}
	for verb, level := range d.Claims {
		got := m.Verbs[wp.Verb(verb)].Level
		if string(got) != level {
			t.Errorf("%s: wrapper.json claims %s, the manifest says %s", verb, level, got)
		}
	}
	for _, v := range wp.ContractVerbs() {
		if _, ok := d.Claims[string(v)]; !ok {
			t.Errorf("wrapper.json says nothing of %s", v)
		}
	}
	df, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	mt := regexp.MustCompile(`(?m)^ENTRYPOINT (\[.*\])\s*$`).FindSubmatch(df)
	if mt == nil {
		t.Fatal("the Dockerfile has no exec-form ENTRYPOINT")
	}
	var entry []string
	if err := json.Unmarshal(mt[1], &entry); err != nil || strings.Join(entry, "\x00") != strings.Join(d.Command, "\x00") {
		t.Fatalf("ENTRYPOINT %v and command %v differ", entry, d.Command)
	}
}

func TestProbeAnswersAManifest(t *testing.T) {
	e := newEnv(t)
	r := e.one(t, wp.VerbCall{Verb: wp.VerbProbe})
	if !r.Succeeded() || r.Manifest.Check() != "" {
		t.Fatalf("%+v", r)
	}
}

func TestEveryRequestGoesToTheEndpointKeepingItsPath(t *testing.T) {
	e := newEnv(t)
	e.run(t, wp.VerbCall{Verb: wp.VerbProbe})
	ref := e.one(t, publishCall("mendel-x-post", "k1")).Ref
	e.one(t, wp.VerbCall{Verb: wp.VerbStatus, Ref: ref})
	e.one(t, wp.VerbCall{Verb: wp.VerbListOwned, Prefix: "mendel-x"})
	if len(e.paths) < 5 {
		t.Fatalf("paths %v", e.paths)
	}
	for i, p := range e.paths {
		if !strings.HasPrefix(p, "/relay/v1/emails") {
			t.Errorf("request %d went to %s", i, p)
		}
		if e.auth[i] != "Bearer re_made_up_key" {
			t.Errorf("request %d authorization %q", i, e.auth[i])
		}
	}
}

func TestPublishedMessageIsLiveReadBackCountedAndListed(t *testing.T) {
	e := newEnv(t)
	pub := e.one(t, publishCall("mendel-fam1-post", "key-1"))
	if !pub.Succeeded() || pub.Ref == "" {
		t.Fatalf("%+v", pub)
	}
	if again := e.one(t, publishCall("mendel-fam1-post", "key-1")); again.Ref != pub.Ref {
		t.Fatalf("the same key made %s beside %s", again.Ref, pub.Ref)
	}
	if st := e.one(t, wp.VerbCall{Verb: wp.VerbStatus, Ref: pub.Ref}); st.Status == nil || st.Status.Effective != wp.EffectiveLive {
		t.Fatalf("%+v", st)
	}
	rb := e.one(t, wp.VerbCall{Verb: wp.VerbReadBack, Ref: pub.Ref})
	var got, want map[string]any
	_ = json.Unmarshal(rb.Asset, &got)
	_ = json.Unmarshal(payload("family@example.com", "Two weeks in"), &want)
	if len(got) != 3 || got["recipient"] != want["recipient"] || got["subject"] != want["subject"] || got["body"] != want["body"] {
		t.Fatalf("read back %s", rb.Asset)
	}
	mt := e.one(t, wp.VerbCall{Verb: wp.VerbReadMetrics, Ref: pub.Ref})
	if v := mt.AssetMetrics["delivered"].Value; v == nil || *v != 1 {
		t.Fatalf("%+v", mt.AssetMetrics)
	}
	for _, name := range []string{"opened", "clicked", "replies"} {
		if f := mt.AssetMetrics[name]; f.Value != nil || f.Unavailable == "" {
			t.Errorf("%s: %+v", name, f)
		}
	}
	e.one(t, publishCall("mendel-fam2-post", "key-2"))
	l := e.one(t, wp.VerbCall{Verb: wp.VerbListOwned, Prefix: "mendel-fam1"})
	if len(l.Owned) != 1 || l.Owned[0] != pub.Ref {
		t.Fatalf("%+v", l.Owned)
	}
	if l := e.one(t, wp.VerbCall{Verb: wp.VerbListOwned, Prefix: "mendel-other"}); len(l.Owned) != 0 {
		t.Fatalf("%+v", l.Owned)
	}
	if r := e.one(t, wp.VerbCall{Verb: wp.VerbListOwned}); r.Refused == "" {
		t.Fatal("an empty prefix was answered")
	}
}

func TestListOwnedWalksMoreThanOnePage(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 103; i++ {
		e.one(t, wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "direct_message", Payload: payload("a@example.com", "x")})
	}
	ref := e.one(t, publishCall("mendel-old-post", "")).Ref
	for i := 0; i < 103; i++ {
		e.one(t, wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "direct_message", Payload: payload("a@example.com", "x")})
	}
	if l := e.one(t, wp.VerbCall{Verb: wp.VerbListOwned, Prefix: "mendel-old"}); len(l.Owned) != 1 || l.Owned[0] != ref {
		t.Fatalf("%+v", l)
	}
}

func TestScheduledMessageIsNotLiveUntilItsTime(t *testing.T) {
	e := newEnv(t)
	at := time.Now().Add(14 * 24 * time.Hour)
	c := publishCall("mendel-later-post", "")
	c.When = &wp.When{Mode: wp.WhenAt, At: &at}
	pub := e.one(t, c)
	if !pub.Succeeded() {
		t.Fatalf("%+v", pub)
	}
	if st := e.one(t, wp.VerbCall{Verb: wp.VerbStatus, Ref: pub.Ref}); st.Status.Effective != wp.EffectiveNotLive {
		t.Fatalf("%+v", st.Status)
	}
	past := time.Now().Add(-time.Hour)
	c.When = &wp.When{Mode: wp.WhenAt, At: &past}
	if r := e.one(t, c); r.Refused == "" {
		t.Fatalf("%+v", r)
	}
	c.When = &wp.When{Mode: wp.WhenAnnounce}
	if r := e.one(t, c); r.Refused == "" {
		t.Fatalf("%+v", r)
	}
}

func TestBoundariesAreRefusedAndNothingIsSent(t *testing.T) {
	e := newEnv(t)
	bad := map[string]json.RawMessage{
		"no subject":     json.RawMessage(`{"recipient":"a@example.com","body":"x"}`),
		"long subject":   payload("a@example.com", strings.Repeat("x", maxSubject+1)),
		"two recipients": payload("a@example.com, b@example.com", "x"),
		"display name":   payload("Ann <a@example.com>", "x"),
		"extra field":    json.RawMessage(`{"recipient":"a@example.com","subject":"s","body":"x","cc":"b@example.com"}`),
		"not an object":  json.RawMessage(`"hello"`),
	}
	for name, p := range bad {
		r := e.one(t, wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "direct_message", Payload: p})
		if r.Refused == "" {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if r := e.one(t, wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "social_post", Payload: payload("a@example.com", "x")}); r.Refused == "" {
		t.Error("another kind was published")
	}
	if l := e.one(t, wp.VerbCall{Verb: wp.VerbListOwned, Prefix: "m"}); len(l.Owned) != 0 {
		t.Error("something was sent")
	}
}

func TestDeclinedVerbsAreRefusedWithTheirReason(t *testing.T) {
	e := newEnv(t)
	m := manifest()
	for _, v := range wp.ContractVerbs() {
		if m.Verbs[v].Level != wp.VerbDeclined {
			continue
		}
		r := e.one(t, wp.VerbCall{Verb: v})
		if r.Refused == "" || !strings.Contains(r.Refused, m.Verbs[v].Reason) {
			t.Errorf("%s: %+v", v, r)
		}
	}
	if r := e.one(t, wp.VerbCall{Verb: "nonsense"}); r.Refused == "" {
		t.Error("an unknown verb was not refused")
	}
}

func TestUnknownRefIsGoneAndNotReadable(t *testing.T) {
	e := newEnv(t)
	if st := e.one(t, wp.VerbCall{Verb: wp.VerbStatus, Ref: "nope"}); st.Status == nil || st.Status.Effective != wp.EffectiveGone {
		t.Fatalf("%+v", st)
	}
	if r := e.one(t, wp.VerbCall{Verb: wp.VerbReadBack, Ref: "nope"}); r.Refused == "" {
		t.Fatalf("%+v", r)
	}
}

func TestAnotherContractIsNotActedOn(t *testing.T) {
	e := newEnv(t)
	resp := run(wp.WrapperRequest{Contract: "1", Connection: e.conn(), Calls: []wp.VerbCall{{Verb: wp.VerbProbe}, {Verb: wp.VerbProbe}}}, e.srv.Client())
	if len(resp.Results) != 1 || resp.Results[0].Succeeded() || len(e.paths) != 0 {
		t.Fatalf("%+v %v", resp, e.paths)
	}
}

func TestAnUnreachableResendFailsWithoutTheKey(t *testing.T) {
	conn := wp.Connection{AccountID: "acme.example", Endpoint: "http://127.0.0.1:1/",
		Credentials: map[string]string{"RESEND_API_KEY": "re_secret_value"}}
	resp := run(wp.WrapperRequest{Contract: "2", Connection: conn, Calls: []wp.VerbCall{{Verb: wp.VerbProbe}}}, &http.Client{Timeout: time.Second})
	if len(resp.Results) != 1 || resp.Results[0].Failed == "" || strings.Contains(resp.Results[0].Failed, "re_secret_value") {
		t.Fatalf("%+v", resp)
	}
}

func TestFakeCancelAndUpdateKeepState(t *testing.T) {
	e := newEnv(t)
	at := time.Now().Add(time.Hour)
	c := publishCall("mendel-c-post", "")
	c.When = &wp.When{Mode: wp.WhenAt, At: &at}
	ref := e.one(t, c).Ref
	do := func(method, path, body string) int {
		req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
		resp, err := e.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if s := do("PATCH", "/emails/"+ref, `{"scheduled_at":"`+at.Add(time.Hour).Format(time.RFC3339)+`"}`); s != 200 {
		t.Fatal(s)
	}
	if s := do("POST", "/emails/"+ref+"/cancel", ""); s != 200 {
		t.Fatal(s)
	}
	if st := e.one(t, wp.VerbCall{Verb: wp.VerbStatus, Ref: ref}); st.Status.Effective != wp.EffectiveGone {
		t.Fatalf("%+v", st.Status)
	}
	if s := do("POST", "/emails/"+ref+"/cancel", ""); s != 422 {
		t.Fatal(s)
	}
}
