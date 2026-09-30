package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

// Defaults for the two settings a campaign needs beyond the kind's own
// family fields, applied when a project's connection.config leaves them
// unset (wrapper.json declares both, with these as their defaults).
const (
	defaultFromName = "Mendel Reminders"
	defaultReplyTo  = "mendel-reminders@example.org"
)

// wrapper is one run's worth of state: the connection it was given, and the
// client it talks to Mailchimp (or its stand-in) through. Nothing here
// outlives the run (GUIDE.md: "A wrapper never stores it").
type wrapper struct {
	conn wp.Connection
	cl   *client
}

func newWrapper(conn wp.Connection) (*wrapper, error) {
	cl, err := newClient(conn)
	if err != nil {
		return nil, err
	}
	return &wrapper{conn: conn, cl: cl}, nil
}

func refuse(v wp.Verb, why string) wp.VerbResult { return wp.VerbResult{Verb: v, Refused: why} }
func fail(v wp.Verb, why string) wp.VerbResult   { return wp.VerbResult{Verb: v, Failed: why} }

func describeErr(what string, err error) string { return fmt.Sprintf("%s: %s", what, err.Error()) }

// dispatch answers one call. Every verb the manifest declines is refused
// here with no request made, so a credential that cannot even reach
// Mailchimp still answers "a verb declared absent is refused" correctly.
func (w *wrapper) dispatch(ctx context.Context, call wp.VerbCall) wp.VerbResult {
	switch call.Verb {
	case wp.VerbProbe:
		return w.probe(ctx)
	case wp.VerbAuthorize:
		return refuse(call.Verb, "Mailchimp is connected with an account API key; this wrapper drives no OAuth "+
			"flow for authorize to answer.")
	case wp.VerbDraft:
		return w.draft(ctx, call)
	case wp.VerbPublish:
		return w.publish(ctx, call)
	case wp.VerbStatus:
		return w.status(ctx, call)
	case wp.VerbAppendUpdate:
		return refuse(call.Verb, "email_broadcast is not log-shaped; sending again creates a new campaign rather "+
			"than appending to one already sent.")
	case wp.VerbRetract:
		return w.retract(ctx, call)
	case wp.VerbReadBack:
		return w.readBack(ctx, call)
	case wp.VerbReadMetrics:
		return w.readMetrics(ctx, call)
	case wp.VerbSetCap:
		return refuse(call.Verb, "a Mailchimp campaign has no spend to cap.")
	case wp.VerbListOwned:
		return w.listOwned(ctx, call)
	case wp.VerbReadSeries:
		return refuse(call.Verb, "this wrapper is not a data source; it has no series to read.")
	case wp.VerbReadTotal:
		return refuse(call.Verb, "this wrapper is not a data source; it has no total to read.")
	case wp.VerbSearch:
		return refuse(call.Verb, "this wrapper answers no article-shaped search results.")
	default:
		return refuse(call.Verb, fmt.Sprintf("%q is not a verb this wrapper recognises.", call.Verb))
	}
}

// --- probe ---

func (w *wrapper) probe(ctx context.Context) wp.VerbResult {
	if strings.TrimSpace(w.conn.AccountID) == "" {
		return fail(wp.VerbProbe, "no audience id was given (connection.account); probe cannot show the "+
			"credential reads an audience without one")
	}
	var list mcList
	if err := w.cl.do(ctx, http.MethodGet, "/lists/"+url.PathEscape(w.conn.AccountID), nil, &list); err != nil {
		return fail(wp.VerbProbe, describeErr("GET /lists/"+w.conn.AccountID, err))
	}
	entitlements := map[string]any{"audience_member_count": list.Stats.MemberCount}
	if list.Name != "" {
		entitlements["audience_name"] = list.Name
	}
	return wp.VerbResult{Verb: wp.VerbProbe, Manifest: buildManifest(entitlements)}
}

// --- draft and publish ---

func (w *wrapper) draft(ctx context.Context, call wp.VerbCall) wp.VerbResult {
	if call.AssetKind != kindEmailBroadcast {
		return refuse(call.Verb, fmt.Sprintf("this wrapper knows no asset kind %q; it acts on %s only.",
			call.AssetKind, kindEmailBroadcast))
	}
	asset, why := parseEmailBroadcast(call.Payload)
	if why != "" {
		return refuse(call.Verb, why)
	}
	if strings.TrimSpace(w.conn.AccountID) == "" {
		return fail(call.Verb, "no audience id was given (connection.account)")
	}
	name := call.Name
	if name == "" {
		name = "mendel-campaign"
	}
	id, apiErr := w.createCampaign(ctx, name, asset)
	if apiErr != "" {
		return fail(call.Verb, apiErr)
	}
	return wp.VerbResult{Verb: call.Verb, Ref: id}
}

func (w *wrapper) publish(ctx context.Context, call wp.VerbCall) wp.VerbResult {
	if call.When == nil {
		return refuse(call.Verb, "publish needs to know when: now, at or announce.")
	}
	switch call.When.Mode {
	case wp.WhenNow, wp.WhenAt:
	case wp.WhenAnnounce:
		return refuse(call.Verb, "Mailchimp has no announced or preview-before-send mode for email_broadcast; "+
			"use now or at.")
	default:
		return refuse(call.Verb, fmt.Sprintf("%q is not a publish mode this wrapper knows.", call.When.Mode))
	}
	if call.When.Mode == wp.WhenAt && (call.When.At == nil || !call.When.At.After(time.Now())) {
		return refuse(call.Verb, "an `at` publish needs a schedule time in the future.")
	}

	var id string
	if call.Ref != "" {
		camp, apiErr := w.getCampaign(ctx, call.Ref)
		if apiErr != "" {
			return fail(call.Verb, apiErr)
		}
		if camp == nil || camp.MendelRetracted {
			return refuse(call.Verb, fmt.Sprintf("%s does not exist, or was retracted", call.Ref))
		}
		if camp.Status != "save" {
			return refuse(call.Verb, fmt.Sprintf("%s is already %s; publish acts on a draft or a payload, not "+
				"an asset already scheduled or sent", call.Ref, camp.Status))
		}
		id = call.Ref
	} else {
		if call.AssetKind != kindEmailBroadcast {
			return refuse(call.Verb, fmt.Sprintf("this wrapper knows no asset kind %q; it acts on %s only.",
				call.AssetKind, kindEmailBroadcast))
		}
		asset, why := parseEmailBroadcast(call.Payload)
		if why != "" {
			return refuse(call.Verb, why)
		}
		if strings.TrimSpace(w.conn.AccountID) == "" {
			return fail(call.Verb, "no audience id was given (connection.account)")
		}
		name := call.Name
		if name == "" {
			name = "mendel-campaign"
		}
		title := name
		if call.IdempotencyKey != "" {
			existing, apiErr := w.findByTitleSuffix(ctx, idempotencyMarker+call.IdempotencyKey)
			if apiErr != "" {
				return fail(call.Verb, apiErr)
			}
			id = existing
			title = titleWithKey(name, call.IdempotencyKey)
		}
		if id == "" {
			newID, apiErr := w.createCampaign(ctx, title, asset)
			if apiErr != "" {
				return fail(call.Verb, apiErr)
			}
			id = newID
		}
	}

	switch call.When.Mode {
	case wp.WhenNow:
		if apiErr := w.sendCampaign(ctx, id); apiErr != "" {
			return fail(call.Verb, apiErr)
		}
	case wp.WhenAt:
		if apiErr := w.scheduleCampaign(ctx, id, *call.When.At); apiErr != "" {
			return fail(call.Verb, apiErr)
		}
	}
	return wp.VerbResult{Verb: call.Verb, Ref: id, URL: archiveURL(id)}
}

func archiveURL(id string) string { return "https://mailchimp.com/campaigns/" + id }

// --- status ---

func (w *wrapper) status(ctx context.Context, call wp.VerbCall) wp.VerbResult {
	if call.Ref == "" {
		return refuse(call.Verb, "status needs a ref.")
	}
	camp, apiErr := w.getCampaign(ctx, call.Ref)
	if apiErr != "" {
		return fail(call.Verb, apiErr)
	}
	if camp == nil {
		return wp.VerbResult{Verb: call.Verb, Status: &wp.AssetStatus{Configured: "gone", Effective: wp.EffectiveGone}}
	}
	return wp.VerbResult{Verb: call.Verb, Status: statusFor(camp)}
}

func statusFor(c *mcCampaign) *wp.AssetStatus {
	switch {
	case c.MendelRetracted:
		return &wp.AssetStatus{Configured: "retracted (" + c.MendelRetractedAs + ")", Effective: wp.EffectiveGone}
	case c.Status == "sent" || c.Status == "sending":
		return &wp.AssetStatus{Configured: "sent", Effective: wp.EffectiveLive}
	case c.Status == "schedule":
		return &wp.AssetStatus{Configured: "scheduled for " + c.ScheduleTime, Effective: wp.EffectiveNotLive}
	default: // "save"
		return &wp.AssetStatus{Configured: "draft", Effective: wp.EffectiveNotLive}
	}
}

// --- retract ---

func (w *wrapper) retract(ctx context.Context, call wp.VerbCall) wp.VerbResult {
	if call.Ref == "" {
		return refuse(call.Verb, "retract needs a ref.")
	}
	camp, apiErr := w.getCampaign(ctx, call.Ref)
	if apiErr != "" {
		return fail(call.Verb, apiErr)
	}
	if camp == nil {
		// Already gone: a draft's retract deletes it outright, and a second
		// retract must answer the same outcome rather than fail.
		return wp.VerbResult{Verb: call.Verb, Retracted: wp.RetractedDeleted}
	}
	if camp.MendelRetracted {
		return wp.VerbResult{Verb: call.Verb, Retracted: camp.MendelRetractedAs}
	}
	switch camp.Status {
	case "save":
		if apiErr := w.deleteCampaign(ctx, call.Ref); apiErr != "" {
			return fail(call.Verb, apiErr)
		}
		return wp.VerbResult{Verb: call.Verb, Retracted: wp.RetractedDeleted}
	case "schedule":
		if apiErr := w.unscheduleCampaign(ctx, call.Ref); apiErr != "" {
			return fail(call.Verb, apiErr)
		}
		return wp.VerbResult{Verb: call.Verb, Retracted: wp.RetractedPaused}
	case "sent", "sending":
		if apiErr := w.cancelSend(ctx, call.Ref); apiErr != "" {
			return fail(call.Verb, apiErr)
		}
		return wp.VerbResult{Verb: call.Verb, Retracted: wp.RetractedPaused}
	default:
		return refuse(call.Verb, fmt.Sprintf("a campaign in status %q cannot be retracted.", camp.Status))
	}
}

// --- read_back ---

func (w *wrapper) readBack(ctx context.Context, call wp.VerbCall) wp.VerbResult {
	if call.Ref == "" {
		return refuse(call.Verb, "read_back needs a ref.")
	}
	camp, apiErr := w.getCampaign(ctx, call.Ref)
	if apiErr != "" {
		return fail(call.Verb, apiErr)
	}
	if camp == nil || camp.MendelRetracted {
		return refuse(call.Verb, fmt.Sprintf("%s is gone; a retracted asset is not read back.", call.Ref))
	}
	var content mcContent
	if err := w.cl.do(ctx, http.MethodGet, "/campaigns/"+url.PathEscape(call.Ref)+"/content", nil, &content); err != nil {
		return fail(call.Verb, describeErr("GET campaign content", err))
	}
	asset := emailBroadcastAsset{
		Subject:   camp.Settings.SubjectLine,
		Preheader: camp.Settings.Preheader,
		Body:      emailBroadcastBody{HTML: content.HTML, Text: content.PlainText},
	}
	raw, err := json.Marshal(asset)
	if err != nil {
		return fail(call.Verb, err.Error())
	}
	return wp.VerbResult{Verb: call.Verb, Asset: raw}
}

// --- read_metrics ---

func (w *wrapper) readMetrics(ctx context.Context, call wp.VerbCall) wp.VerbResult {
	if call.Ref == "" {
		return refuse(call.Verb, "read_metrics needs a ref.")
	}
	camp, apiErr := w.getCampaign(ctx, call.Ref)
	if apiErr != "" {
		return fail(call.Verb, apiErr)
	}
	if camp == nil {
		return refuse(call.Verb, fmt.Sprintf("%s is gone.", call.Ref))
	}
	if camp.Status != "sent" && camp.Status != "sending" {
		why := "the campaign has not been sent yet"
		return wp.VerbResult{Verb: call.Verb, AssetMetrics: map[string]wp.FieldReading{
			"opens":   {Unavailable: why},
			"clicks":  {Unavailable: why},
			"bounces": {Unavailable: why},
		}}
	}
	var report mcReport
	if err := w.cl.do(ctx, http.MethodGet, "/reports/"+url.PathEscape(call.Ref), nil, &report); err != nil {
		return fail(call.Verb, describeErr("GET campaign report", err))
	}
	opens := float64(report.Opens.OpensTotal)
	clicks := float64(report.Clicks.ClicksTotal)
	bounces := float64(report.Bounces.HardBounces + report.Bounces.SoftBounces)
	return wp.VerbResult{Verb: call.Verb, AssetMetrics: map[string]wp.FieldReading{
		"opens":   {Value: &opens},
		"clicks":  {Value: &clicks},
		"bounces": {Value: &bounces},
	}}
}

// --- list_owned ---

func (w *wrapper) listOwned(ctx context.Context, call wp.VerbCall) wp.VerbResult {
	if call.Prefix == "" {
		return refuse(call.Verb, "list_owned needs a prefix.")
	}
	if strings.TrimSpace(w.conn.AccountID) == "" {
		return fail(call.Verb, "no audience id was given (connection.account)")
	}
	list, apiErr := w.listCampaigns(ctx)
	if apiErr != "" {
		return fail(call.Verb, apiErr)
	}
	var owned []string
	for _, c := range list {
		if strings.HasPrefix(c.Settings.Title, call.Prefix) {
			owned = append(owned, c.ID)
		}
	}
	return wp.VerbResult{Verb: call.Verb, Owned: owned}
}

// --- Mailchimp calls shared by the verbs above ---

func (w *wrapper) getCampaign(ctx context.Context, id string) (*mcCampaign, string) {
	var c mcCampaign
	if err := w.cl.do(ctx, http.MethodGet, "/campaigns/"+url.PathEscape(id), nil, &c); err != nil {
		if statusOf(err) == http.StatusNotFound {
			return nil, ""
		}
		return nil, describeErr("GET /campaigns/"+id, err)
	}
	return &c, ""
}

func (w *wrapper) listCampaigns(ctx context.Context) ([]mcCampaign, string) {
	var list mcCampaignList
	path := "/campaigns?list_id=" + url.QueryEscape(w.conn.AccountID) + "&count=1000"
	if err := w.cl.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, describeErr("GET /campaigns", err)
	}
	return list.Campaigns, ""
}

func (w *wrapper) findByTitleSuffix(ctx context.Context, suffix string) (string, string) {
	list, apiErr := w.listCampaigns(ctx)
	if apiErr != "" {
		return "", apiErr
	}
	for _, c := range list {
		if strings.HasSuffix(c.Settings.Title, suffix) {
			return c.ID, ""
		}
	}
	return "", ""
}

func (w *wrapper) createCampaign(ctx context.Context, title string, asset *emailBroadcastAsset) (string, string) {
	body := map[string]any{
		"type":       "regular",
		"recipients": map[string]any{"list_id": w.conn.AccountID},
		"settings": map[string]any{
			"subject_line": asset.Subject,
			"preheader":    asset.Preheader,
			"title":        title,
			"from_name":    configString(w.conn, "from_name", defaultFromName),
			"reply_to":     configString(w.conn, "reply_to", defaultReplyTo),
		},
	}
	var created mcCampaign
	if err := w.cl.do(ctx, http.MethodPost, "/campaigns", body, &created); err != nil {
		return "", describeErr("POST /campaigns", err)
	}
	content := map[string]any{"html": asset.Body.HTML, "plain_text": asset.Body.Text}
	if err := w.cl.do(ctx, http.MethodPut, "/campaigns/"+url.PathEscape(created.ID)+"/content", content, nil); err != nil {
		return "", describeErr("PUT campaign content", err)
	}
	return created.ID, ""
}

func (w *wrapper) sendCampaign(ctx context.Context, id string) string {
	if err := w.cl.do(ctx, http.MethodPost, "/campaigns/"+url.PathEscape(id)+"/actions/send", nil, nil); err != nil {
		return describeErr("POST actions/send", err)
	}
	return ""
}

func (w *wrapper) scheduleCampaign(ctx context.Context, id string, at time.Time) string {
	body := map[string]any{"schedule_time": formatScheduleTime(at)}
	if err := w.cl.do(ctx, http.MethodPost, "/campaigns/"+url.PathEscape(id)+"/actions/schedule", body, nil); err != nil {
		return describeErr("POST actions/schedule", err)
	}
	return ""
}

func (w *wrapper) unscheduleCampaign(ctx context.Context, id string) string {
	if err := w.cl.do(ctx, http.MethodPost, "/campaigns/"+url.PathEscape(id)+"/actions/unschedule", nil, nil); err != nil {
		return describeErr("POST actions/unschedule", err)
	}
	return ""
}

func (w *wrapper) cancelSend(ctx context.Context, id string) string {
	if err := w.cl.do(ctx, http.MethodPost, "/campaigns/"+url.PathEscape(id)+"/actions/cancel-send", nil, nil); err != nil {
		return describeErr("POST actions/cancel-send", err)
	}
	return ""
}

func (w *wrapper) deleteCampaign(ctx context.Context, id string) string {
	if err := w.cl.do(ctx, http.MethodDelete, "/campaigns/"+url.PathEscape(id), nil, nil); err != nil {
		return describeErr("DELETE /campaigns", err)
	}
	return ""
}

// configString reads a connection.config setting, or fallback when it is
// unset or not text.
func configString(conn wp.Connection, name, fallback string) string {
	if v, ok := conn.Config[name]; ok {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return fallback
}
