package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

// mailer is a small wrapper for an email broadcast tool: a kind that cannot
// be recalled, so it declines retract. Switches each break one rule.
type mailer struct {
	sent        map[string]json.RawMessage
	next        int
	acceptsLong bool // sends a subject past the shape's maxLength
	altersBody  bool // reads back HTML other than what was sent
	kind        string
}

func (f *mailer) manifest() *wp.CapabilityManifest {
	shape := `{"type":"object","required":["subject","preheader","body"],"properties":{"subject":{"type":"string","maxLength":70},` +
		`"preheader":{"type":"string"},"body":{"type":"object","required":["html","text"],"properties":{"html":{"type":"string"},"text":{"type":"string"}}}}}`
	if f.kind == "listing" {
		shape = `{"type":"object","required":["name","tagline","description","media","url"],"properties":{"name":{"type":"string","maxLength":60}}}`
	}
	m := &wp.CapabilityManifest{
		Contract: wp.ContractVersion,
		Wrapper:  wp.WrapperProvenance{Version: "1.0", SpecSource: "https://mail.example/api", SpecHash: "sha256:x"},
		Verbs:    map[wp.Verb]wp.VerbSupport{},
		Metrics:  map[string]wp.MetricSupport{"opens": {Level: wp.MetricAvailable, Kind: wp.KindCount, Aggregations: []string{"count"}}},
		Venue:    "test_account", Idempotency: "none", Entitlements: map[string]any{},
		StoragePolicy: "The sender owns it.",
		Kinds:         map[string]wp.KindSupport{f.kind: {Level: wp.VerbSupported, Shape: json.RawMessage(shape)}},
	}
	for _, v := range wp.ContractVerbs() {
		m.Verbs[v] = wp.VerbSupport{Level: wp.VerbDeclined, Reason: "not here"}
	}
	for _, v := range []wp.Verb{wp.VerbProbe, wp.VerbPublish, wp.VerbStatus, wp.VerbReadBack} {
		m.Verbs[v] = wp.VerbSupport{Level: wp.VerbSupported}
	}
	return m
}

func (f *mailer) run(_ context.Context, req wp.WrapperRequest) (wp.WrapperResponse, string, error) {
	var out wp.WrapperResponse
	if req.Contract != wp.ContractVersion {
		out.Results = append(out.Results, wp.VerbResult{Verb: req.Calls[0].Verb, Failed: "written against contract " + wp.ContractVersion})
		return out, "", nil
	}
	for _, c := range req.Calls {
		r := wp.VerbResult{Verb: c.Verb}
		m := f.manifest()
		switch {
		case m.Verbs[c.Verb].Level == wp.VerbDeclined || m.Verbs[c.Verb].Level == "":
			r.Refused = "declared absent"
		case c.Verb == wp.VerbProbe:
			r.Manifest = m
		case c.Verb == wp.VerbPublish:
			var p map[string]any
			json.Unmarshal(c.Payload, &p)
			subject, _ := p["subject"].(string)
			_, hasBody := p["body"]
			switch {
			case c.AssetKind != f.kind:
				r.Refused = "not a kind this tool sends"
			case f.kind == "email_broadcast" && (subject == "" || !hasBody):
				r.Refused = "an email needs a subject and a body"
			case f.kind == "email_broadcast" && len(subject) > 70 && !f.acceptsLong:
				r.Refused = "subject too long"
			default:
				f.next++
				r.Ref = fmt.Sprint(f.next)
				f.sent[r.Ref] = c.Payload
			}
		case c.Verb == wp.VerbStatus:
			r.Status = &wp.AssetStatus{Configured: "sent", Effective: wp.EffectiveLive}
		case c.Verb == wp.VerbReadBack:
			body := f.sent[c.Ref]
			if f.altersBody {
				body = json.RawMessage(strings.Replace(string(body), "Sent by", "Sent to you by", 1))
			}
			r.Asset = body
		}
		out.Results = append(out.Results, r)
		if !r.Succeeded() {
			break
		}
	}
	return out, "", nil
}

func runMailer(f *mailer) Report {
	f.sent = map[string]json.RawMessage{}
	if f.kind == "" {
		f.kind = "email_broadcast"
	}
	d := wp.Description{Tool: wp.Tool{Slug: "mail", Name: "Mail"}, Version: "1.0", Contract: wp.ContractVersion,
		Image: "mail:dev", Command: []string{"/mail"}, SpecSource: "https://mail.example/api",
		Connection: wp.ConnectionSpec{Credentials: []wp.Field{{Name: "MAIL_KEY", Label: "Key"}}},
		Claims:     map[string]string{"publish": "supported"}}
	return Run(context.Background(), Target{Description: d, Run: f.run, FakeVenue: true,
		Connection: wp.Connection{AccountID: "list-1", Credentials: map[string]string{"MAIL_KEY": "mail-key-SECRET"}}}, now)
}

// A kind that cannot be recalled is published and judged without retract,
// and a wrapper that keeps the rules passes; one that sends too long a
// subject, or reads back other than what was sent, fails.
func TestAnEmailThatCannotBeRecalledIsJudgedWithoutRetract(t *testing.T) {
	if r := runMailer(&mailer{}); !r.Passed() {
		t.Fatalf("a good mailer: %+v", r.Checks)
	}
	for name, f := range map[string]*mailer{
		"sends a subject past its maxLength": {acceptsLong: true},
		"reads back other HTML":             {altersBody: true},
	} {
		if runMailer(f).Passed() {
			t.Errorf("%s: passed", name)
		}
	}
}

// A kind that can be taken back is still never published by a wrapper that
// declines retract.
func TestAListingWithoutRetractIsNotPublished(t *testing.T) {
	f := &mailer{kind: "listing"}
	r := runMailer(f)
	if r.Passed() || len(f.sent) != 0 {
		t.Errorf("a listing without retract: passed=%v, sent %d", r.Passed(), len(f.sent))
	}
}

// Every seeded kind has payloads, and each valid payload survives the JSON
// round trip read_back is compared after.
func TestEverySeededKindHasPayloadsThatRoundTrip(t *testing.T) {
	known := knownKinds()
	for kind := range seededKinds {
		p, ok := known[kind]
		if !ok {
			t.Errorf("no payloads for %s", kind)
			continue
		}
		v := p.valid(60)
		var back map[string]any
		json.Unmarshal(raw(v), &back)
		if fmt.Sprint(back) != fmt.Sprint(v) {
			t.Errorf("%s does not round-trip: %v vs %v", kind, v, back)
		}
	}
}
