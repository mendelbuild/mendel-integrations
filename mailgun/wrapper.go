package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

const (
	// credentialKey is the one credential: a Mailgun API key.
	credentialKey = "MAILGUN_API_KEY"
	// defaultBase is Mailgun's US API host. The EU host, api.eu.mailgun.net,
	// is reached by giving it as the connection's endpoint.
	defaultBase = "https://api.mailgun.net"
	// kindDirectMessage is the one External Asset Kind published.
	kindDirectMessage = "direct_message"
	// maxSubject is the longest subject accepted.
	maxSubject = 200
	// maxSchedule is the farthest ahead Mailgun schedules, on its longest plan:
	// "up to 3 or 7 days in advance" depending on the plan.
	maxSchedule = 7 * 24 * time.Hour
	// probeRecipient is where the probe's test-mode message is addressed. In
	// test mode it is processed and not delivered, and example.com has no
	// mail exchanger.
	probeRecipient = "mendel-probe@example.com"
)

// handle answers a request: one result per call, in order, stopping at the
// first that does not succeed.
func handle(ctx context.Context, req wp.WrapperRequest, hc *http.Client, now func() time.Time) wp.WrapperResponse {
	var out wp.WrapperResponse
	if len(req.Calls) == 0 {
		return out
	}
	if req.Contract != wp.ContractVersion {
		out.Results = append(out.Results, wp.VerbResult{Verb: req.Calls[0].Verb,
			Failed: fmt.Sprintf("this wrapper speaks contract %q and the request is for %q; nothing was done", wp.ContractVersion, req.Contract)})
		return out
	}
	c := newClient(req.Connection, hc, now)
	for _, call := range req.Calls {
		res := c.run(ctx, call)
		res.Verb = call.Verb
		out.Results = append(out.Results, res)
		if !res.Succeeded() {
			break
		}
	}
	return out
}

type client struct {
	hc     *http.Client
	now    func() time.Time
	base   string
	domain string
	key    string
	config map[string]any
}

func newClient(conn wp.Connection, hc *http.Client, now func() time.Time) *client {
	base := strings.TrimRight(strings.TrimSpace(conn.Endpoint), "/")
	if base == "" {
		base = defaultBase
	}
	return &client{hc: hc, now: now, base: base, domain: strings.TrimSpace(conn.AccountID),
		key: conn.Credentials[credentialKey], config: conn.Config}
}

func (c *client) run(ctx context.Context, call wp.VerbCall) wp.VerbResult {
	switch call.Verb {
	case wp.VerbProbe:
		return c.probe(ctx)
	case wp.VerbPublish:
		return c.publish(ctx, call)
	}
	if reason, ok := declined[call.Verb]; ok {
		return wp.VerbResult{Refused: fmt.Sprintf("this wrapper does not do %s: %s", call.Verb, reason)}
	}
	return wp.VerbResult{Refused: fmt.Sprintf("%q is not a verb of contract %s", call.Verb, wp.ContractVersion)}
}

// --- Mailgun ---

// apiError is Mailgun answering with something other than 200.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string {
	if e.msg == "" {
		return fmt.Sprintf("Mailgun answered %d", e.status)
	}
	return fmt.Sprintf("Mailgun answered %d: %s", e.status, e.msg)
}

// ready says why a call to Mailgun cannot be made, or "".
func (c *client) ready() string {
	switch {
	case c.key == "":
		return "no " + credentialKey + " was given"
	case c.domain == "":
		return "no sending domain was given as the account"
	}
	return ""
}

// send posts one message to {endpoint}/v3/{domain}/messages as multipart
// form data, and answers the id Mailgun gave it.
func (c *client) send(ctx context.Context, fields [][2]string) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return "", err
		}
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	u := c.base + "/v3/" + url.PathEscape(c.domain) + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth("api", c.key)
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach Mailgun: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var answer struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &answer)
	if resp.StatusCode != http.StatusOK {
		return "", &apiError{status: resp.StatusCode, msg: answer.Message}
	}
	if answer.ID == "" {
		return "", fmt.Errorf("Mailgun answered 200 without the id of the message")
	}
	return answer.ID, nil
}

// failure is what went wrong with a call to Mailgun, as a result. Mailgun's
// 400 is a message it will not take, which is a refusal and not worth a
// retry; everything else is a failure.
func failure(err error, refuseBadRequest bool) wp.VerbResult {
	if ae, ok := err.(*apiError); ok {
		switch {
		case ae.status == http.StatusBadRequest && refuseBadRequest:
			return wp.VerbResult{Refused: "Mailgun would not take the message: " + ae.Error()}
		case ae.status == http.StatusUnauthorized:
			return wp.VerbResult{Failed: "Mailgun rejected the API key (401)"}
		case ae.status == http.StatusTooManyRequests:
			return wp.VerbResult{Failed: "Mailgun is rate limiting this account (429); try again later"}
		}
	}
	return wp.VerbResult{Failed: err.Error()}
}

// --- probe ---

// probe proves the key can send for the domain with the cheapest call the
// documentation gives: a message in test mode, which Mailgun "processes
// normally but does not deliver". The pages this wrapper was written against
// describe no call that only reads the account.
func (c *client) probe(ctx context.Context) wp.VerbResult {
	if why := c.ready(); why != "" {
		return wp.VerbResult{Failed: why}
	}
	_, err := c.send(ctx, [][2]string{
		{"from", c.sender()}, {"to", probeRecipient}, {"subject", "Mendel probe"},
		{"text", "Sent in test mode by Mendel to check the credential; it is not delivered."},
		{"o:testmode", "yes"}, {"o:tracking", "no"},
	})
	if err != nil {
		return wp.VerbResult{Failed: "the probe's test-mode message was not accepted: " + failure(err, false).Failed}
	}
	return wp.VerbResult{Manifest: manifest()}
}

// --- publish ---

// directMessage is the payload of a direct_message asset.
type directMessage struct {
	Recipient *string `json:"recipient"`
	Subject   *string `json:"subject"`
	Body      *string `json:"body"`
}

// parsePayload checks a payload against the kind's shape and says why not, or "".
func parsePayload(raw json.RawMessage) (directMessage, string) {
	var p directMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if len(raw) == 0 {
		return p, "the payload is empty; a direct_message needs recipient, subject and body"
	}
	if err := dec.Decode(&p); err != nil {
		return p, "the payload is not a direct_message (recipient, subject and body, all text): " + err.Error()
	}
	switch {
	case p.Recipient == nil || strings.TrimSpace(*p.Recipient) == "":
		return p, "the payload has no recipient"
	case p.Subject == nil || strings.TrimSpace(*p.Subject) == "":
		return p, "the payload has no subject"
	case p.Body == nil || strings.TrimSpace(*p.Body) == "":
		return p, "the payload has no body"
	case utf8.RuneCountInString(*p.Subject) > maxSubject:
		return p, fmt.Sprintf("the subject is %d characters; the longest this wrapper sends is %d",
			utf8.RuneCountInString(*p.Subject), maxSubject)
	case strings.ContainsAny(*p.Subject, "\r\n"):
		return p, "the subject has a line break, which a header cannot hold"
	}
	// One recipient: a direct message is one-to-one, so a list, a friendly
	// name or a group is not one.
	to := strings.TrimSpace(*p.Recipient)
	if a, err := mail.ParseAddress(to); err != nil || a.Address != to || strings.ContainsAny(to, ",;") {
		return p, fmt.Sprintf("the recipient %q is not a single email address", to)
	}
	return p, ""
}

func (c *client) publish(ctx context.Context, call wp.VerbCall) wp.VerbResult {
	if call.Ref != "" && len(call.Payload) == 0 {
		return wp.VerbResult{Refused: "Mailgun has no draft to publish by its ref (draft is declined); publish a payload"}
	}
	if call.AssetKind != kindDirectMessage {
		return wp.VerbResult{Refused: fmt.Sprintf("this wrapper publishes %s and not %q", kindDirectMessage, call.AssetKind)}
	}
	p, why := parsePayload(call.Payload)
	if why != "" {
		return wp.VerbResult{Refused: why}
	}
	from, reply, why := c.senderFromConfig()
	if why != "" {
		return wp.VerbResult{Refused: why}
	}
	fields := [][2]string{
		{"from", from}, {"to", strings.TrimSpace(*p.Recipient)}, {"subject", *p.Subject}, {"text", *p.Body},
		// One person's check-in is not a campaign: no pixel, no rewritten links.
		{"o:tracking", "no"},
	}
	if reply != "" {
		fields = append(fields, [2]string{"h:Reply-To", reply})
	}
	// What Mendel called the asset, and its idempotency key, are kept as
	// custom data so they appear in Mailgun's events; the header that would
	// show them to the recipient is suppressed.
	if call.Name != "" {
		fields = append(fields, [2]string{"v:mendel-name", call.Name})
	}
	if call.IdempotencyKey != "" {
		fields = append(fields, [2]string{"v:mendel-idempotency-key", call.IdempotencyKey})
	}
	if call.Name != "" || call.IdempotencyKey != "" {
		fields = append(fields, [2]string{"o:suppress-headers", "X-Mailgun-Variables"})
	}
	when := call.When
	if when == nil {
		when = &wp.When{Mode: wp.WhenNow}
	}
	switch when.Mode {
	case wp.WhenNow:
	case wp.WhenAt:
		now := c.now()
		switch {
		case when.At == nil:
			return wp.VerbResult{Refused: "publish at an instant needs the instant"}
		case !when.At.After(now):
			return wp.VerbResult{Refused: "the instant is not in the future; publish now instead"}
		case when.At.After(now.Add(maxSchedule)):
			return wp.VerbResult{Refused: "Mailgun schedules a message at most 3 or 7 days ahead, by plan; publish when it is nearer"}
		}
		fields = append(fields, [2]string{"o:deliverytime", when.At.UTC().Format(time.RFC1123Z)})
	default:
		return wp.VerbResult{Refused: fmt.Sprintf("a direct message is sent now or at an instant, not %q: it has no announcement", when.Mode)}
	}
	if why := c.ready(); why != "" {
		return wp.VerbResult{Failed: why}
	}
	id, err := c.send(ctx, fields)
	if err != nil {
		return failure(err, true)
	}
	return wp.VerbResult{Ref: id}
}

// senderFromConfig is the From and Reply-To of a message: the project's
// settings, or postmaster at the sending domain.
func (c *client) senderFromConfig() (from, reply, why string) {
	from = c.sender()
	if v, ok := c.config["from"].(string); ok && strings.TrimSpace(v) != "" {
		from = strings.TrimSpace(v)
	}
	if _, err := mail.ParseAddress(from); err != nil {
		return "", "", fmt.Sprintf("the setting from, %q, is not an email address", from)
	}
	if v, ok := c.config["reply_to"].(string); ok && strings.TrimSpace(v) != "" {
		reply = strings.TrimSpace(v)
		if _, err := mail.ParseAddress(reply); err != nil {
			return "", "", fmt.Sprintf("the setting reply_to, %q, is not an email address", reply)
		}
	}
	return from, reply, ""
}

func (c *client) sender() string {
	if v, ok := c.config["from"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return "postmaster@" + c.domain
}
