package main

import "time"

// The Mailchimp resources this wrapper reads and writes: campaigns, their
// content and their reports (Campaigns, and Campaigns > Content,
// https://mailchimp.com/developer/marketing/api/campaigns/, fetched
// 2026-09-30). Fields beyond what the wrapper uses are left out.
//
// mendelRetracted and mendelRetractedAs are not Mailchimp's own fields: the
// API gives no durable way to mark a sent or scheduled campaign "taken
// back" short of an action that changes its status (cancel-send,
// unschedule), and both of those leave the campaign in a status a fresh
// draft can also be in. The wrapper needs to tell the two apart so that
// retract stays safe to call twice and status and read_back agree it is
// gone; it writes these two fields itself (see fakeapi, which is the only
// server it talks to that understands them) and never assumes a real
// Mailchimp deployment would carry them under these exact names.
type mcCampaign struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	CreateTime string `json:"create_time,omitempty"`
	ArchiveURL string `json:"archive_url,omitempty"`
	// Status is one of Mailchimp's own: "save" (a draft that has not been
	// scheduled or sent), "schedule" (scheduled for delivery), "sending" or
	// "sent".
	Status     string `json:"status"`
	Recipients struct {
		ListID string `json:"list_id"`
	} `json:"recipients"`
	Settings struct {
		SubjectLine string `json:"subject_line"`
		Preheader   string `json:"preheader"`
		// Title is Mailchimp's internal campaign name, never shown to a
		// recipient: where this wrapper keeps the name Mendel gave the asset
		// (for list_owned) and, appended after idempotencyMarker, the
		// idempotency key a publish was made with (for a repeat publish to
		// find the same asset rather than sending a second wave).
		Title    string `json:"title"`
		FromName string `json:"from_name"`
		ReplyTo  string `json:"reply_to"`
	} `json:"settings"`
	ScheduleTime      string `json:"schedule_time,omitempty"`
	SendTime          string `json:"send_time,omitempty"`
	MendelRetracted   bool   `json:"_mendel_retracted,omitempty"`
	MendelRetractedAs string `json:"_mendel_retracted_as,omitempty"`
}

// mcContent is a campaign's content (Campaigns > Content: "Manage the
// HTML, plain-text, and template content for your Mailchimp campaigns.").
type mcContent struct {
	HTML      string `json:"html"`
	PlainText string `json:"plain_text"`
}

// mcCampaignList is List campaigns' answer.
type mcCampaignList struct {
	Campaigns  []mcCampaign `json:"campaigns"`
	TotalItems int          `json:"total_items"`
}

// mcList is Get list info's answer, as far as probe reads it: enough to
// show the credential can read the audience this connection names.
type mcList struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Stats struct {
		MemberCount int `json:"member_count"`
	} `json:"stats"`
}

// mcReport is Get campaign report's answer, as far as read_metrics reads
// it: opens, clicks and bounces (Reports: "Mailchimp's campaign and
// automation reports analyze clicks, opens, subscribers' social activity,
// e-commerce data, and more").
type mcReport struct {
	EmailsSent int `json:"emails_sent"`
	Opens      struct {
		OpensTotal  int `json:"opens_total"`
		UniqueOpens int `json:"unique_opens"`
	} `json:"opens"`
	Clicks struct {
		ClicksTotal  int `json:"clicks_total"`
		UniqueClicks int `json:"unique_subscriber_clicks"`
	} `json:"clicks"`
	Bounces struct {
		HardBounces int `json:"hard_bounces"`
		SoftBounces int `json:"soft_bounces"`
	} `json:"bounces"`
}

// idempotencyMarker separates a campaign's name from the idempotency key a
// publish that created it carried, inside Mailchimp's internal title field.
const idempotencyMarker = " #mendel-idempotency:"

func titleWithKey(name, key string) string {
	if key == "" {
		return name
	}
	return name + idempotencyMarker + key
}

// scheduleTimeLayout is the timestamp Mailchimp's schedule action reads:
// UTC, to the minute, no offset (the form Schedule campaign's examples use).
const scheduleTimeLayout = "2006-01-02T15:04:05"

func formatScheduleTime(t time.Time) string {
	return t.UTC().Format(scheduleTimeLayout)
}
