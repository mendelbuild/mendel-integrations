package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
	"github.com/mendelbuild/mendel-integrations/mailgun/fakeapi"
)

var fixedNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// venue serves a fake Mailgun under a path prefix, as a stand-in reached
// through the connection's endpoint.
func venue(t *testing.T) (*fakeapi.Fake, wp.Connection) {
	t.Helper()
	f := fakeapi.New()
	srv := httptest.NewServer(http.StripPrefix("/mg", f.Handler()))
	t.Cleanup(srv.Close)
	return f, wp.Connection{AccountID: "mg.example.com", Endpoint: srv.URL + "/mg",
		Credentials: map[string]string{"MAILGUN_API_KEY": "key-made-up-0123456789"}}
}

func do(t *testing.T, conn wp.Connection, calls ...wp.VerbCall) []wp.VerbResult {
	t.Helper()
	req := wp.WrapperRequest{Contract: "2", Connection: conn, Calls: calls}
	resp := handle(context.Background(), req, http.DefaultClient, func() time.Time { return fixedNow })
	raw, _ := json.Marshal(resp)
	if _, err := wp.ParseWrapperResponse(raw, len(calls)); err != nil {
		t.Fatalf("the protocol refuses the answer: %v", err)
	}
	return resp.Results
}

func publish(payload string, when *wp.When) wp.VerbCall {
	return wp.VerbCall{Verb: wp.VerbPublish, AssetKind: "direct_message", Payload: json.RawMessage(payload),
		When: when, IdempotencyKey: "k1", Name: "mendel-checkin-1"}
}

const valid = `{"recipient":"parent@example.org","subject":"How are things?","body":"Hello! How is your family settling in?"}`

func TestProbeManifestIsAcceptedAndSendsNothingForReal(t *testing.T) {
	f, conn := venue(t)
	res := do(t, conn, wp.VerbCall{Verb: wp.VerbProbe})
	if !res[0].Succeeded() {
		t.Fatal(res[0].Why())
	}
	if why := res[0].Manifest.Check(); why != "" {
		t.Fatal(why)
	}
	if n := len(f.Sent()); n != 0 {
		t.Fatalf("the probe delivered %d messages; it should use test mode", n)
	}
}

func TestPublishSendsThroughTheEndpointKeepingItsPath(t *testing.T) {
	f, conn := venue(t)
	conn.Config = map[string]any{"from": "Care Team <care@mg.example.com>", "reply_to": "replies@mg.example.com"}
	res := do(t, conn, publish(valid, &wp.When{Mode: wp.WhenNow}))
	if !res[0].Succeeded() || res[0].Ref == "" {
		t.Fatalf("not published: %s", res[0].Why())
	}
	sent := f.Sent()
	if len(sent) != 1 {
		t.Fatalf("%d messages sent", len(sent))
	}
	m := sent[0]
	if m.ID != res[0].Ref || m.Domain != "mg.example.com" || m.Subject != "How are things?" ||
		m.Text != "Hello! How is your family settling in?" || len(m.To) != 1 || m.To[0] != "parent@example.org" {
		t.Fatalf("sent %+v", m)
	}
	if m.From != "Care Team <care@mg.example.com>" || m.Headers["Reply-To"] != "replies@mg.example.com" {
		t.Fatalf("from %q, headers %v", m.From, m.Headers)
	}
	if m.Options["tracking"] != "no" || m.Variables["mendel-name"] != "mendel-checkin-1" {
		t.Fatalf("options %v variables %v", m.Options, m.Variables)
	}
	if !m.DeliverAt.IsZero() {
		t.Fatal("scheduled though published now")
	}
}

func TestDefaultSenderIsPostmasterAtTheDomain(t *testing.T) {
	f, conn := venue(t)
	do(t, conn, publish(valid, nil))
	if got := f.Sent()[0].From; got != "postmaster@mg.example.com" {
		t.Fatalf("from %q", got)
	}
}

func TestPublishAtAnInstantSchedulesIt(t *testing.T) {
	f, conn := venue(t)
	at := fixedNow.Add(48 * time.Hour)
	res := do(t, conn, publish(valid, &wp.When{Mode: wp.WhenAt, At: &at}))
	if !res[0].Succeeded() {
		t.Fatal(res[0].Why())
	}
	if got := f.Sent()[0].DeliverAt; !got.Equal(at) {
		t.Fatalf("scheduled for %v, want %v", got, at)
	}
}

func TestPublishRefusalsSendNothing(t *testing.T) {
	past := fixedNow.Add(-time.Hour)
	far := fixedNow.Add(14 * 24 * time.Hour)
	long := `{"recipient":"a@example.org","subject":"` + strings.Repeat("x", 201) + `","body":"b"}`
	cases := map[string]wp.VerbCall{
		"missing subject":    publish(`{"recipient":"a@example.org","body":"b"}`, nil),
		"missing recipient":  publish(`{"subject":"s","body":"b"}`, nil),
		"missing body":       publish(`{"recipient":"a@example.org","subject":"s"}`, nil),
		"subject too long":   publish(long, nil),
		"two recipients":     publish(`{"recipient":"a@example.org, b@example.org","subject":"s","body":"b"}`, nil),
		"named recipient":    publish(`{"recipient":"A <a@example.org>","subject":"s","body":"b"}`, nil),
		"not an address":     publish(`{"recipient":"nobody","subject":"s","body":"b"}`, nil),
		"unknown field":      publish(`{"recipient":"a@example.org","subject":"s","body":"b","cc":"c@example.org"}`, nil),
		"subject with break": publish("{\"recipient\":\"a@example.org\",\"subject\":\"s\\nBcc: x@example.org\",\"body\":\"b\"}", nil),
		"no payload":         publish(``, nil),
		"announced":          publish(valid, &wp.When{Mode: wp.WhenAnnounce}),
		"at without instant": publish(valid, &wp.When{Mode: wp.WhenAt}),
		"at in the past":     publish(valid, &wp.When{Mode: wp.WhenAt, At: &past}),
		"at too far":         publish(valid, &wp.When{Mode: wp.WhenAt, At: &far}),
		"another kind":       {Verb: wp.VerbPublish, AssetKind: "social_post", Payload: json.RawMessage(valid)},
		"a draft by its ref": {Verb: wp.VerbPublish, Ref: "<x@mg.example.com>"},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			f, conn := venue(t)
			res := do(t, conn, call)
			if res[0].Refused == "" {
				t.Fatalf("not refused: %+v", res[0])
			}
			if len(f.Sent()) != 0 {
				t.Fatal("something was sent")
			}
		})
	}
}

func TestPayloadAtTheSubjectBoundaryIsSent(t *testing.T) {
	f, conn := venue(t)
	p := `{"recipient":"a@example.org","subject":"` + strings.Repeat("é", 200) + `","body":"b"}`
	if res := do(t, conn, publish(p, nil)); !res[0].Succeeded() {
		t.Fatal(res[0].Why())
	}
	if len(f.Sent()) != 1 {
		t.Fatal("not sent")
	}
}

func TestDeclinedVerbsAreRefusedWithTheirReason(t *testing.T) {
	m := manifest()
	for _, v := range wp.ContractVerbs() {
		if m.Verbs[v].Level != wp.VerbDeclined {
			continue
		}
		_, conn := venue(t)
		res := do(t, conn, wp.VerbCall{Verb: v})
		if res[0].Refused == "" || !strings.Contains(res[0].Refused, m.Verbs[v].Reason) {
			t.Errorf("%s: %+v", v, res[0])
		}
	}
	_, conn := venue(t)
	if res := do(t, conn, wp.VerbCall{Verb: "not_a_verb"}); res[0].Refused == "" {
		t.Error("an unknown verb was not refused")
	}
}

func TestEveryVerbIsAnswered(t *testing.T) {
	m := manifest()
	for _, v := range wp.ContractVerbs() {
		if _, ok := m.Verbs[v]; !ok {
			t.Errorf("verb %s is not answered", v)
		}
	}
	if m.Verbs[wp.VerbProbe].Level != wp.VerbSupported || m.Verbs[wp.VerbPublish].Level != wp.VerbSupported {
		t.Error("probe and publish should be supported")
	}
}

func TestAnotherContractFailsFirstCallAndStops(t *testing.T) {
	f, conn := venue(t)
	resp := handle(context.Background(), wp.WrapperRequest{Contract: "1", Connection: conn,
		Calls: []wp.VerbCall{publish(valid, nil), {Verb: wp.VerbProbe}}}, http.DefaultClient, time.Now)
	if len(resp.Results) != 1 || resp.Results[0].Failed == "" || len(f.Sent()) != 0 {
		t.Fatalf("%+v", resp)
	}
}

func TestACallStopsTheRunWhenItDoesNotSucceed(t *testing.T) {
	_, conn := venue(t)
	res := do(t, conn, wp.VerbCall{Verb: wp.VerbProbe}, wp.VerbCall{Verb: wp.VerbRetract, Ref: "x"}, wp.VerbCall{Verb: wp.VerbProbe})
	if len(res) != 2 || res[1].Refused == "" {
		t.Fatalf("%+v", res)
	}
}

func TestMailgunErrors(t *testing.T) {
	status := 0
	body := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	conn := wp.Connection{AccountID: "mg.example.com", Endpoint: srv.URL, Credentials: map[string]string{"MAILGUN_API_KEY": "key-made-up-0123456789"}}

	status, body = 401, `{"message":"Forbidden"}`
	if r := do(t, conn, publish(valid, nil)); r[0].Failed == "" || strings.Contains(r[0].Failed, "key-made-up") {
		t.Errorf("401: %+v", r[0])
	}
	if r := do(t, conn, wp.VerbCall{Verb: wp.VerbProbe}); r[0].Failed == "" {
		t.Errorf("probe with a rejected key: %+v", r[0])
	}
	status, body = 500, `oops`
	if r := do(t, conn, publish(valid, nil)); r[0].Failed == "" {
		t.Errorf("500: %+v", r[0])
	}
	status, body = 429, `{"message":"slow down"}`
	if r := do(t, conn, publish(valid, nil)); r[0].Failed == "" {
		t.Errorf("429: %+v", r[0])
	}
	status, body = 400, `{"message":"'to' parameter is not a valid address"}`
	if r := do(t, conn, publish(valid, nil)); r[0].Refused == "" {
		t.Errorf("400: %+v", r[0])
	}
	status, body = 200, `{"message":"Queued. Thank you."}`
	if r := do(t, conn, publish(valid, nil)); r[0].Failed == "" {
		t.Errorf("200 without an id: %+v", r[0])
	}
}

func TestNoKeyOrDomainFailsWithoutCalling(t *testing.T) {
	f, conn := venue(t)
	conn.Credentials = nil
	if r := do(t, conn, publish(valid, nil)); r[0].Failed == "" {
		t.Errorf("no key: %+v", r[0])
	}
	_, conn = venue(t)
	conn.AccountID = ""
	if r := do(t, conn, wp.VerbCall{Verb: wp.VerbProbe}); r[0].Failed == "" {
		t.Errorf("no domain: %+v", r[0])
	}
	if len(f.Sent()) != 0 {
		t.Fatal("sent")
	}
}

func TestBadSenderSettingIsRefused(t *testing.T) {
	_, conn := venue(t)
	conn.Config = map[string]any{"from": "not an address"}
	if r := do(t, conn, publish(valid, nil)); r[0].Refused == "" {
		t.Errorf("%+v", r[0])
	}
}

// --- wrapper.json and the Dockerfile agree with the wrapper ---

func readDescription(t *testing.T) wp.Description {
	t.Helper()
	raw, err := os.ReadFile("wrapper.json")
	if err != nil {
		t.Fatal(err)
	}
	var d wp.Description
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestWrapperJSONAgreesWithTheProbe(t *testing.T) {
	d := readDescription(t)
	if why := d.Check(); why != "" {
		t.Fatal(why)
	}
	if d.Tool.Slug != "mailgun" || d.Tool.Name != "Mailgun" || d.Version != "0.1.0" || d.Contract != "2" ||
		d.Image != "mendel-tool-mailgun:dev" {
		t.Fatalf("%+v", d)
	}
	m := manifest()
	if m.Wrapper.Version != d.Version || m.Contract != d.Contract || m.Wrapper.SpecSource != d.SpecSource {
		t.Error("the manifest and wrapper.json disagree on version, contract or spec")
	}
	for verb, level := range d.Claims {
		if level != "declined" && m.Verbs[wp.Verb(verb)].Level == wp.VerbDeclined {
			t.Errorf("%s is claimed %s and declined by the probe", verb, level)
		}
	}
	if why := d.Connection.CheckConfig(map[string]any{"from": "a@b.example", "reply_to": "c@d.example"}); why != "" {
		t.Error(why)
	}
	for name := range manifestConfigRead() {
		found := false
		for _, s := range d.Connection.Config {
			found = found || s.Name == name
		}
		if !found {
			t.Errorf("the wrapper reads setting %s and wrapper.json does not declare it", name)
		}
	}
}

// manifestConfigRead is the settings the wrapper reads.
func manifestConfigRead() map[string]bool { return map[string]bool{"from": true, "reply_to": true} }

func TestCommandIsTheDockerfilesEntrypoint(t *testing.T) {
	raw, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^ENTRYPOINT\s+(\[.*\])\s*$`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("no exec-form ENTRYPOINT")
	}
	var entry []string
	if err := json.Unmarshal(m[1], &entry); err != nil {
		t.Fatal(err)
	}
	d := readDescription(t)
	if strings.Join(entry, "\x00") != strings.Join(d.Command, "\x00") {
		t.Fatalf("ENTRYPOINT %v, command %v", entry, d.Command)
	}
}

func TestSpecHashMatchesThePagesWhenPresent(t *testing.T) {
	// The spec pages sit beside the wrapper in the repository; a build
	// context without them skips this.
	files, _ := os.ReadDir("../spec/mailgun")
	if len(files) != 3 {
		t.Skip("spec pages not present")
	}
	h := sha256.New()
	for _, f := range files {
		b, err := os.ReadFile("../spec/mailgun/" + f.Name())
		if err != nil {
			t.Fatal(err)
		}
		h.Write(b)
	}
	if got := "sha256:" + hex.EncodeToString(h.Sum(nil)); got != specHash {
		t.Fatalf("specHash is %s, the pages hash to %s", specHash, got)
	}
}
