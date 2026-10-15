package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

// nameTag is the tag that keeps Mendel's name for an email, since Resend
// keeps no name of its own; list_owned finds emails by it.
const nameTag = "mendel_name"

// tagValue is what a tag value may hold: ASCII letters, digits, underscores
// and dashes (Resend's rule for tags, from memory; see README.md).
var tagValue = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// maxListPages bounds list_owned's walk: 50 pages of 100 emails.
const maxListPages = 50

type wrapper struct {
	conn wp.Connection
	c    *client
}

func refuse(format string, a ...any) wp.VerbResult {
	return wp.VerbResult{Refused: fmt.Sprintf(format, a...)}
}

func fail(format string, a ...any) wp.VerbResult {
	return wp.VerbResult{Failed: fmt.Sprintf(format, a...)}
}

// failed turns an error from Resend into a failed result.
func failed(what string, err error) wp.VerbResult {
	return fail("%s: %v", what, err)
}

func (w *wrapper) handle(call wp.VerbCall) wp.VerbResult {
	if why, ok := declined[call.Verb]; ok {
		return refuse("%s is declined: %s", call.Verb, why)
	}
	switch call.Verb {
	case wp.VerbProbe:
		return w.probe()
	case wp.VerbPublish:
		return w.publish(call)
	case wp.VerbStatus:
		return w.status(call)
	case wp.VerbReadBack:
		return w.readBack(call)
	case wp.VerbReadMetrics:
		return w.readMetrics(call)
	case wp.VerbListOwned:
		return w.listOwned(call)
	}
	return refuse("%q is not a verb of contract %s", call.Verb, wp.ContractVersion)
}

// probe proves the credential with the cheapest read there is: one email
// from List Sent Emails.
func (w *wrapper) probe() wp.VerbResult {
	if _, err := w.c.list(1, ""); err != nil {
		return failed("the API key could not read the account's sent emails", err)
	}
	return wp.VerbResult{Manifest: manifest()}
}

// from is the sender: the connection's account, as a full address or as a
// verified domain (then hello@domain).
func (w *wrapper) from() string {
	a := strings.TrimSpace(w.conn.AccountID)
	if a == "" || strings.Contains(a, "@") {
		return a
	}
	return "hello@" + a
}

// directMessage is the kind's family: one recipient, a subject, a body.
type directMessage struct {
	Recipient string `json:"recipient"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
}

// parseDirectMessage checks a payload against the shape, and says why not.
func parseDirectMessage(raw json.RawMessage) (*directMessage, string) {
	var fields map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, "the payload is not a JSON object"
	}
	for _, k := range []string{"recipient", "subject", "body"} {
		v, present := fields[k]
		s, isText := v.(string)
		switch {
		case !present:
			return nil, fmt.Sprintf("the payload has no %s, which a direct_message requires", k)
		case !isText || strings.TrimSpace(s) == "":
			return nil, fmt.Sprintf("%s must be non-empty text", k)
		}
	}
	for k := range fields {
		if k != "recipient" && k != "subject" && k != "body" {
			return nil, fmt.Sprintf("the payload has a field %q; a direct_message has recipient, subject and body", k)
		}
	}
	m := &directMessage{fields["recipient"].(string), fields["subject"].(string), fields["body"].(string)}
	if a, err := mail.ParseAddress(m.Recipient); err != nil || a.Address != m.Recipient || strings.ContainsAny(m.Recipient, ", ") {
		return nil, fmt.Sprintf("recipient %q is not one bare email address; a direct_message goes to one person", m.Recipient)
	}
	if n := utf8.RuneCountInString(m.Subject); n > maxSubject {
		return nil, fmt.Sprintf("the subject is %d characters; the shape's maxLength is %d", n, maxSubject)
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		return nil, "the subject must be one line"
	}
	if n := utf8.RuneCountInString(m.Body); n > maxBody {
		return nil, fmt.Sprintf("the body is %d characters; the shape's maxLength is %d", n, maxBody)
	}
	return m, ""
}

func (w *wrapper) publish(call wp.VerbCall) wp.VerbResult {
	if call.Ref != "" {
		return refuse("there are no drafts to publish by ref (draft is declined); publish takes the payload")
	}
	if call.AssetKind != kindDirectMessage {
		return refuse("this wrapper publishes direct_message, not %q", call.AssetKind)
	}
	msg, why := parseDirectMessage(call.Payload)
	if why != "" {
		return refuse("%s", why)
	}
	body := map[string]any{"from": w.from(), "to": []string{msg.Recipient}, "subject": msg.Subject, "text": msg.Body}
	if body["from"] == "" {
		return refuse("the connection names no account, so there is no sender; give a verified domain or a from address")
	}
	mode := wp.WhenNow
	if call.When != nil && call.When.Mode != "" {
		mode = call.When.Mode
	}
	switch mode {
	case wp.WhenNow:
	case wp.WhenAt:
		if call.When.At == nil || !call.When.At.After(time.Now()) {
			return refuse("when 'at' needs an instant in the future")
		}
		body["scheduled_at"] = call.When.At.UTC().Format(time.RFC3339)
	default:
		return refuse("when %q is not offered: a one-to-one message is sent now or at an instant, never announced", mode)
	}
	if call.Name != "" {
		if !tagValue.MatchString(call.Name) {
			return refuse("the name %q cannot be kept as a tag (letters, digits, underscore and dash, up to 256)", call.Name)
		}
		body["tags"] = []map[string]string{{"name": nameTag, "value": call.Name}}
	}
	header := map[string]string{}
	if call.IdempotencyKey != "" {
		if len(call.IdempotencyKey) > 256 {
			return refuse("the idempotency key is longer than 256 characters")
		}
		header["Idempotency-Key"] = call.IdempotencyKey
	}
	var sent struct {
		ID string `json:"id"`
	}
	if err := w.c.do(http.MethodPost, "/emails", nil, body, header, &sent); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == 422 {
			return refuse("Resend refused the message: %s", ae.Message)
		}
		return failed("sending the message", err)
	}
	if sent.ID == "" {
		return fail("Resend accepted the message and gave no email id")
	}
	return wp.VerbResult{Ref: sent.ID}
}

// retrieve reads an email, answering a result when it cannot: notFound says
// what a 404 means for the verb.
func (w *wrapper) retrieve(ref string) (*emailDoc, *wp.VerbResult) {
	if ref == "" {
		r := refuse("no ref was given")
		return nil, &r
	}
	e, err := w.c.retrieve(ref)
	var ae *apiError
	switch {
	case errors.As(err, &ae) && ae.Status == 404:
		return nil, nil
	case err != nil:
		r := failed("reading the email", err)
		return nil, &r
	}
	return e, nil
}

func (w *wrapper) status(call wp.VerbCall) wp.VerbResult {
	e, bad := w.retrieve(call.Ref)
	switch {
	case bad != nil:
		return *bad
	case e == nil:
		return wp.VerbResult{Status: &wp.AssetStatus{Configured: "Resend holds no email " + call.Ref, Effective: wp.EffectiveGone}}
	}
	to := strings.Join(e.To, ", ")
	switch e.LastEvent {
	case "scheduled":
		when := ""
		if e.ScheduledAt != nil {
			when = " for " + *e.ScheduledAt
		}
		return wp.VerbResult{Status: &wp.AssetStatus{Configured: "scheduled" + when + " to " + to, Effective: wp.EffectiveNotLive}}
	case "canceled":
		return wp.VerbResult{Status: &wp.AssetStatus{Configured: "canceled before it was sent to " + to, Effective: wp.EffectiveGone}}
	}
	return wp.VerbResult{Status: &wp.AssetStatus{Configured: "sent to " + to + "; last event " + e.LastEvent, Effective: wp.EffectiveLive}}
}

func (w *wrapper) readBack(call wp.VerbCall) wp.VerbResult {
	e, bad := w.retrieve(call.Ref)
	switch {
	case bad != nil:
		return *bad
	case e == nil:
		return refuse("Resend holds no email %s", call.Ref)
	case e.LastEvent == "canceled":
		return refuse("email %s was canceled and is gone", call.Ref)
	case len(e.To) != 1 || e.Text == nil:
		return refuse("email %s is not a direct_message this wrapper sent: it has %d recipients and %s plain-text body",
			call.Ref, len(e.To), map[bool]string{true: "a", false: "no"}[e.Text != nil])
	}
	asset, _ := json.Marshal(directMessage{Recipient: e.To[0], Subject: e.Subject, Body: *e.Text})
	return wp.VerbResult{Asset: asset}
}

// delivered says an email reached the recipient's server: its last event is
// delivered, or one that can only follow delivery.
func delivered(last string) bool {
	return last == "delivered" || last == "opened" || last == "clicked"
}

func (w *wrapper) readMetrics(call wp.VerbCall) wp.VerbResult {
	e, bad := w.retrieve(call.Ref)
	switch {
	case bad != nil:
		return *bad
	case e == nil:
		return refuse("Resend holds no email %s", call.Ref)
	case e.LastEvent == "canceled":
		return refuse("email %s was canceled and is gone", call.Ref)
	}
	m := manifest().Metrics
	out := map[string]wp.FieldReading{}
	for name, ms := range m {
		if ms.Level == wp.MetricUnavailable {
			out[name] = wp.FieldReading{Unavailable: ms.Reason}
			continue
		}
		v := 0.0
		if delivered(e.LastEvent) {
			v = 1
		}
		out[name] = wp.FieldReading{Value: &v, Quality: ms.Quality}
	}
	return wp.VerbResult{AssetMetrics: out}
}

// listOwned walks List Sent Emails newest first and keeps the emails whose
// name tag starts with the prefix. The list carries no tags, so each email is
// read; a history longer than the walk is a failure, never a short answer.
func (w *wrapper) listOwned(call wp.VerbCall) wp.VerbResult {
	if call.Prefix == "" {
		return refuse("list_owned needs a prefix; an empty one would list every email of the account")
	}
	owned := []string{}
	after := ""
	for page := 0; ; page++ {
		if page == maxListPages {
			return fail("the account has more than %d emails; list_owned will not answer from part of them", maxListPages*100)
		}
		l, err := w.c.list(100, after)
		if err != nil {
			return failed("listing sent emails", err)
		}
		for _, item := range l.Data {
			e, err := w.c.retrieve(item.ID)
			var ae *apiError
			if errors.As(err, &ae) && ae.Status == 404 {
				continue
			}
			if err != nil {
				return failed("reading an email's tags", err)
			}
			if e.LastEvent == "canceled" {
				continue
			}
			for _, t := range e.Tags {
				if t.Name == nameTag && strings.HasPrefix(t.Value, call.Prefix) {
					owned = append(owned, e.ID)
					break
				}
			}
		}
		if !l.HasMore || len(l.Data) == 0 {
			return wp.VerbResult{Owned: owned}
		}
		after = l.Data[len(l.Data)-1].ID
	}
}
