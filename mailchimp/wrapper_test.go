package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"

	"github.com/mendelbuild/mendel-integrations/mailchimp/fakeapi"
)

// testConn starts a fresh fake server and returns a connection pointed at
// it, as Mendel would run the wrapper against a stand-in for Mailchimp on
// the loopback (GUIDE.md: "every request the wrapper makes to the tool
// goes to it, keeping its path").
func testConn(t *testing.T) wp.Connection {
	t.Helper()
	srv := httptest.NewServer(fakeapi.Handler())
	t.Cleanup(srv.Close)
	return wp.Connection{
		Credentials: map[string]string{"MAILCHIMP_API_KEY": "conformance-test-key"},
		AccountID:   "audience-" + t.Name(),
		Endpoint:    srv.URL,
	}
}

func call(t *testing.T, conn wp.Connection, calls ...wp.VerbCall) wp.WrapperResponse {
	t.Helper()
	return handle(context.Background(), wp.WrapperRequest{Contract: wp.ContractVersion, Connection: conn, Calls: calls})
}

func one(t *testing.T, conn wp.Connection, c wp.VerbCall) wp.VerbResult {
	t.Helper()
	resp := call(t, conn, c)
	if len(resp.Results) != 1 {
		t.Fatalf("%s: got %d results, want 1: %+v", c.Verb, len(resp.Results), resp.Results)
	}
	return resp.Results[0]
}

func requireSucceeded(t *testing.T, res wp.VerbResult) {
	t.Helper()
	if !res.Succeeded() {
		t.Fatalf("%s did not succeed: %s", res.Verb, res.Why())
	}
}

// --- wrapper.json agrees with the code ---

func loadDescription(t *testing.T) wp.Description {
	t.Helper()
	data, err := os.ReadFile("wrapper.json")
	if err != nil {
		t.Fatalf("reading wrapper.json: %v", err)
	}
	var d wp.Description
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatalf("wrapper.json is not readable: %v", err)
	}
	if why := d.Check(); why != "" {
		t.Fatalf("wrapper.json: %s", why)
	}
	return d
}

func TestWrapperJSONAgreesWithProbe(t *testing.T) {
	d := loadDescription(t)
	conn := testConn(t)
	res := one(t, conn, wp.VerbCall{Verb: wp.VerbProbe})
	requireSucceeded(t, res)
	m := res.Manifest
	if why := m.Check(); why != "" {
		t.Fatalf("the manifest does not pass CapabilityManifest.Check: %s", why)
	}
	if m.Wrapper.Version != d.Version {
		t.Errorf("manifest version %q; wrapper.json version %q", m.Wrapper.Version, d.Version)
	}
	if m.Contract != d.Contract {
		t.Errorf("manifest contract %q; wrapper.json contract %q", m.Contract, d.Contract)
	}
	// A wrapper's probe must not decline anything wrapper.json claims
	// (GUIDE.md: "claims are shortlisting evidence ... the wrapper's own
	// tests check its probe does not decline anything this file claims").
	for verb, level := range d.Claims {
		if level == string(wp.VerbDeclined) {
			continue
		}
		if m.Verbs[wp.Verb(verb)].Level == wp.VerbDeclined {
			t.Errorf("wrapper.json claims %s %s, and probe declines it: %s", verb, level, m.Verbs[wp.Verb(verb)].Reason)
		}
	}
	if m.IsDataSource() {
		t.Error("this wrapper writes; it must not present itself as a data source")
	}
}

// --- probe ---

func TestProbeFailsWithoutAnAudience(t *testing.T) {
	conn := testConn(t)
	conn.AccountID = ""
	res := one(t, conn, wp.VerbCall{Verb: wp.VerbProbe})
	if res.Succeeded() || res.Failed == "" {
		t.Fatalf("probe without an audience id: %+v", res)
	}
}

// --- declined verbs are refused, not failed, with no request made ---

func TestDeclinedVerbsAreRefused(t *testing.T) {
	conn := testConn(t)
	// Malformed, with no "-dc" suffix: fine, since the endpoint override
	// means the key's data center is never derived from it.
	conn.Credentials["MAILCHIMP_API_KEY"] = "not-a-usable-key"
	for _, v := range []wp.Verb{wp.VerbAuthorize, wp.VerbAppendUpdate, wp.VerbSetCap, wp.VerbReadSeries, wp.VerbReadTotal, wp.VerbSearch} {
		res := one(t, conn, wp.VerbCall{Verb: v})
		if res.Succeeded() || res.Refused == "" {
			t.Errorf("%s: want refused, got %+v", v, res)
		}
	}
}

func TestAnUnknownVerbIsRefused(t *testing.T) {
	conn := testConn(t)
	res := one(t, conn, wp.VerbCall{Verb: "conformance_not_a_verb"})
	if res.Succeeded() || res.Refused == "" {
		t.Fatalf("an unknown verb: %+v", res)
	}
}

func TestAnotherContractVersionStopsAtOnce(t *testing.T) {
	conn := testConn(t)
	resp := handle(context.Background(), wp.WrapperRequest{Contract: "0-conformance", Connection: conn,
		Calls: []wp.VerbCall{{Verb: wp.VerbProbe}, {Verb: wp.VerbStatus}}})
	if len(resp.Results) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(resp.Results), resp.Results)
	}
	if resp.Results[0].Succeeded() {
		t.Fatalf("a request for another contract acted: %+v", resp.Results[0])
	}
}

// --- the email_broadcast lifecycle ---

func validPayload() map[string]any {
	return map[string]any{
		"subject":   "Reminder: the vote is tomorrow",
		"preheader": "One more thing before the vote",
		"body": map[string]any{
			"html": "<p>Please remember to vote tomorrow.</p>",
			"text": "Please remember to vote tomorrow.",
		},
	}
}

func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPublishReadBackReadMetricsListOwnedRetract(t *testing.T) {
	conn := testConn(t)
	prefix := "mendel-test-" + t.Name()
	name := prefix + "-post"
	valid := validPayload()

	pub := wp.VerbCall{Verb: wp.VerbPublish, AssetKind: kindEmailBroadcast, Payload: raw(t, valid),
		When: &wp.When{Mode: wp.WhenNow}, IdempotencyKey: "idem-1", Name: name}
	res := one(t, conn, pub)
	requireSucceeded(t, res)
	if res.Ref == "" {
		t.Fatal("publish made no ref")
	}
	ref := res.Ref

	// Publishing again with the same idempotency key is the same asset.
	again := one(t, conn, pub)
	requireSucceeded(t, again)
	if again.Ref != ref {
		t.Fatalf("a repeat publish with the same idempotency key made %s, not %s", again.Ref, ref)
	}

	st := one(t, conn, wp.VerbCall{Verb: wp.VerbStatus, Ref: ref})
	requireSucceeded(t, st)
	if st.Status == nil || st.Status.Effective != wp.EffectiveLive {
		t.Fatalf("status after publish: %+v", st.Status)
	}

	back := one(t, conn, wp.VerbCall{Verb: wp.VerbReadBack, Ref: ref})
	requireSucceeded(t, back)
	var got map[string]any
	if err := json.Unmarshal(back.Asset, &got); err != nil {
		t.Fatalf("read_back's asset is not JSON: %v", err)
	}
	if !jsonEqual(t, got, valid) {
		t.Fatalf("read_back = %s; want the field-for-field equal of %v", back.Asset, valid)
	}

	metrics := one(t, conn, wp.VerbCall{Verb: wp.VerbReadMetrics, Ref: ref})
	requireSucceeded(t, metrics)
	for _, m := range []string{"opens", "clicks", "bounces"} {
		got, ok := metrics.AssetMetrics[m]
		if !ok || got.Value == nil {
			t.Errorf("read_metrics did not answer %s with a value: %+v", m, got)
		}
	}

	owned := one(t, conn, wp.VerbCall{Verb: wp.VerbListOwned, Prefix: prefix})
	requireSucceeded(t, owned)
	if !containsStr(owned.Owned, ref) {
		t.Fatalf("list_owned(%q) = %v; want it to include %s", prefix, owned.Owned, ref)
	}
	notOwned := one(t, conn, wp.VerbCall{Verb: wp.VerbListOwned, Prefix: prefix + "-other"})
	requireSucceeded(t, notOwned)
	if containsStr(notOwned.Owned, ref) {
		t.Fatalf("list_owned ignored its prefix: %v", notOwned.Owned)
	}

	// retract, twice, same outcome.
	rs := call(t, conn, wp.VerbCall{Verb: wp.VerbRetract, Ref: ref}, wp.VerbCall{Verb: wp.VerbRetract, Ref: ref})
	if len(rs.Results) != 2 {
		t.Fatalf("retract twice: %+v", rs.Results)
	}
	requireSucceeded(t, rs.Results[0])
	requireSucceeded(t, rs.Results[1])
	if rs.Results[0].Retracted == "" || rs.Results[0].Retracted != rs.Results[1].Retracted {
		t.Fatalf("retract twice answered %q then %q", rs.Results[0].Retracted, rs.Results[1].Retracted)
	}

	gone := one(t, conn, wp.VerbCall{Verb: wp.VerbStatus, Ref: ref})
	requireSucceeded(t, gone)
	if gone.Status == nil || gone.Status.Effective != wp.EffectiveGone {
		t.Fatalf("status after retract: %+v", gone.Status)
	}

	refused := one(t, conn, wp.VerbCall{Verb: wp.VerbReadBack, Ref: ref})
	if refused.Succeeded() || refused.Refused == "" {
		t.Fatalf("read_back of a retracted asset: %+v", refused)
	}
}

// jsonEqual compares two values by what they marshal to, which for
// map[string]any is deterministic (encoding/json sorts object keys), and
// so is safe for comparing "the same JSON object" regardless of key order.
func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	ab, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	// Round-trip b through map[string]any too, so "valid" (built with
	// nested map[string]any already) and "got" (decoded from JSON) compare
	// the same way.
	var bv any
	_ = json.Unmarshal(bb, &bv)
	bb2, _ := json.Marshal(bv)
	return string(ab) == string(bb2)
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// --- boundaries ---

func TestPublishRefusesAnIncompletePayload(t *testing.T) {
	conn := testConn(t)
	incomplete := map[string]any{"subject": "No body", "preheader": "Missing its body"}
	res := one(t, conn, wp.VerbCall{Verb: wp.VerbPublish, AssetKind: kindEmailBroadcast, Payload: raw(t, incomplete),
		When: &wp.When{Mode: wp.WhenNow}, IdempotencyKey: "idem-incomplete"})
	if res.Succeeded() || res.Refused == "" {
		t.Fatalf("an incomplete payload: %+v", res)
	}
}

func TestPublishRefusesASubjectPastItsMaxLength(t *testing.T) {
	conn := testConn(t)
	tooLong := map[string]any{"subject": strings.Repeat("x", subjectMaxLength+1), "preheader": "Too long a subject",
		"body": map[string]any{"html": "<p>Refused.</p>", "text": "Refused."}}
	res := one(t, conn, wp.VerbCall{Verb: wp.VerbPublish, AssetKind: kindEmailBroadcast, Payload: raw(t, tooLong),
		When: &wp.When{Mode: wp.WhenNow}, IdempotencyKey: "idem-too-long"})
	if res.Succeeded() || res.Refused == "" {
		t.Fatalf("an over-length subject: %+v", res)
	}
}

func TestPublishRefusesAnnounce(t *testing.T) {
	conn := testConn(t)
	res := one(t, conn, wp.VerbCall{Verb: wp.VerbPublish, AssetKind: kindEmailBroadcast, Payload: raw(t, validPayload()),
		When: &wp.When{Mode: wp.WhenAnnounce}})
	if res.Succeeded() || res.Refused == "" {
		t.Fatalf("an announce publish: %+v", res)
	}
}

func TestPublishAtSchedulesRatherThanSending(t *testing.T) {
	conn := testConn(t)
	at := time.Now().Add(48 * time.Hour)
	res := one(t, conn, wp.VerbCall{Verb: wp.VerbPublish, AssetKind: kindEmailBroadcast, Payload: raw(t, validPayload()),
		When: &wp.When{Mode: wp.WhenAt, At: &at}, Name: "mendel-test-scheduled"})
	requireSucceeded(t, res)
	st := one(t, conn, wp.VerbCall{Verb: wp.VerbStatus, Ref: res.Ref})
	requireSucceeded(t, st)
	if st.Status.Effective != wp.EffectiveNotLive {
		t.Fatalf("a campaign scheduled for the future: %+v", st.Status)
	}
}

// --- draft, then publish by ref ---

func TestDraftIsNotLiveUntilPublishedByRef(t *testing.T) {
	conn := testConn(t)
	d := one(t, conn, wp.VerbCall{Verb: wp.VerbDraft, AssetKind: kindEmailBroadcast, Payload: raw(t, validPayload()),
		Name: "mendel-test-draft"})
	requireSucceeded(t, d)
	if d.Ref == "" {
		t.Fatal("draft made no ref")
	}
	st := one(t, conn, wp.VerbCall{Verb: wp.VerbStatus, Ref: d.Ref})
	requireSucceeded(t, st)
	if st.Status.Effective != wp.EffectiveNotLive {
		t.Fatalf("a draft's status: %+v", st.Status)
	}
	pub := one(t, conn, wp.VerbCall{Verb: wp.VerbPublish, Ref: d.Ref, When: &wp.When{Mode: wp.WhenNow}})
	requireSucceeded(t, pub)
	if pub.Ref != d.Ref {
		t.Fatalf("publish by ref answered a different ref: %s, not %s", pub.Ref, d.Ref)
	}
	live := one(t, conn, wp.VerbCall{Verb: wp.VerbStatus, Ref: d.Ref})
	requireSucceeded(t, live)
	if live.Status.Effective != wp.EffectiveLive {
		t.Fatalf("status after publishing a draft: %+v", live.Status)
	}
}

func TestRetractOfADraftDeletesIt(t *testing.T) {
	conn := testConn(t)
	d := one(t, conn, wp.VerbCall{Verb: wp.VerbDraft, AssetKind: kindEmailBroadcast, Payload: raw(t, validPayload()),
		Name: "mendel-test-draft-delete"})
	requireSucceeded(t, d)
	rs := call(t, conn, wp.VerbCall{Verb: wp.VerbRetract, Ref: d.Ref}, wp.VerbCall{Verb: wp.VerbRetract, Ref: d.Ref})
	if len(rs.Results) != 2 {
		t.Fatalf("retract twice: %+v", rs.Results)
	}
	requireSucceeded(t, rs.Results[0])
	requireSucceeded(t, rs.Results[1])
	if rs.Results[0].Retracted != wp.RetractedDeleted {
		t.Fatalf("retracting a never-sent draft: %q", rs.Results[0].Retracted)
	}
	if rs.Results[1].Retracted != wp.RetractedDeleted {
		t.Fatalf("the second retract answered %q, not %q", rs.Results[1].Retracted, wp.RetractedDeleted)
	}
}

func TestReadMetricsBeforeSendingIsUnavailableNeverZero(t *testing.T) {
	conn := testConn(t)
	d := one(t, conn, wp.VerbCall{Verb: wp.VerbDraft, AssetKind: kindEmailBroadcast, Payload: raw(t, validPayload()),
		Name: "mendel-test-draft-metrics"})
	requireSucceeded(t, d)
	m := one(t, conn, wp.VerbCall{Verb: wp.VerbReadMetrics, Ref: d.Ref})
	requireSucceeded(t, m)
	for _, name := range []string{"opens", "clicks", "bounces"} {
		got := m.AssetMetrics[name]
		if got.Value != nil {
			t.Errorf("%s before sending was read as %v; it should be unavailable, not zero", name, *got.Value)
		}
		if got.Unavailable == "" {
			t.Errorf("%s before sending did not say why it is unavailable", name)
		}
	}
}

// --- an unknown asset kind ---

func TestPublishRefusesAnUnknownKind(t *testing.T) {
	conn := testConn(t)
	res := one(t, conn, wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "direct_message", Payload: raw(t, validPayload()),
		When: &wp.When{Mode: wp.WhenNow}})
	if res.Succeeded() || res.Refused == "" {
		t.Fatalf("publish of an unknown kind: %+v", res)
	}
}

// --- credentials never leak ---

func TestCredentialNeverAppearsInAFailure(t *testing.T) {
	conn := testConn(t)
	conn.Credentials["MAILCHIMP_API_KEY"] = "super-secret-credential-value"
	conn.AccountID = ""
	res := one(t, conn, wp.VerbCall{Verb: wp.VerbProbe})
	if res.Succeeded() {
		t.Fatal("probe without an audience id unexpectedly succeeded")
	}
	blob, _ := json.Marshal(res)
	if strings.Contains(string(blob), "super-secret-credential-value") {
		t.Fatalf("the credential leaked into the result: %s", blob)
	}
}
