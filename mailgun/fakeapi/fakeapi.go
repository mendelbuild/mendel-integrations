// Package fakeapi is an in-memory stand-in for the part of Mailgun's API the
// wrapper calls: "Send an email", POST /v3/{domain_name}/messages, as
// documentation.mailgun.com describes it (see mailgun/README.md). It keeps
// what it accepts between requests, takes any credential, and is meant to be
// served on the loopback.
package fakeapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"time"
)

// maxSchedule is how far ahead o:deliverytime may be: "up to 3 or 7 days in
// advance" depending on the plan; the fake behaves as a 7 day plan.
const maxSchedule = 7 * 24 * time.Hour

// Message is one message the fake accepted for delivery.
type Message struct {
	Domain     string
	ID         string
	From       string
	To         []string
	Subject    string
	Text       string
	HTML       string
	Headers    map[string]string // h: options, by header name
	Variables  map[string]string // v: options, by variable name
	Options    map[string]string // o: options, by option name
	DeliverAt  time.Time         // zero when not scheduled
	AcceptedAt time.Time
}

// Fake is the fake Mailgun.
type Fake struct {
	mu   sync.Mutex
	sent []Message
	now  func() time.Time
}

// New is a fake with nothing sent.
func New() *Fake { return &Fake{now: time.Now} }

// Handler is a fresh fake, as an http.Handler.
func Handler() http.Handler { return New().Handler() }

// Sent is every message accepted so far, oldest first. A message sent with
// o:testmode=yes is processed but, as Mailgun says, not delivered, so it is
// not here.
func (f *Fake) Sent() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.sent...)
}

// Handler serves the fake's API.
func (f *Fake) Handler() http.Handler {
	return http.HandlerFunc(f.serve)
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"message": msg})
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	// /v3/{domain_name}/messages
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "v3" || parts[2] != "messages" || parts[1] == "" {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}
	// Any credential is accepted, but basic auth is how Mailgun is called.
	if _, _, ok := r.BasicAuth(); !ok {
		fail(w, http.StatusUnauthorized, "Forbidden")
		return
	}
	domain, err := url.PathUnescape(parts[1])
	if err != nil {
		fail(w, http.StatusBadRequest, "domain is not valid")
		return
	}
	f.send(w, r, domain)
}

func (f *Fake) send(w http.ResponseWriter, r *http.Request, domain string) {
	if err := r.ParseMultipartForm(32 << 20); err != nil && err != http.ErrNotMultipart {
		fail(w, http.StatusBadRequest, "the form could not be read")
		return
	}
	if r.MultipartForm == nil {
		if err := r.ParseForm(); err != nil {
			fail(w, http.StatusBadRequest, "the form could not be read")
			return
		}
	}
	get := func(k string) string { return r.FormValue(k) }

	m := Message{Domain: domain, Headers: map[string]string{}, Variables: map[string]string{}, Options: map[string]string{}}
	m.From, m.Subject, m.Text, m.HTML = get("from"), get("subject"), get("text"), get("html")
	switch {
	case strings.TrimSpace(m.From) == "":
		fail(w, http.StatusBadRequest, "'from' parameter is missing")
		return
	case strings.TrimSpace(get("to")) == "":
		fail(w, http.StatusBadRequest, "'to' parameter is missing")
		return
	case strings.TrimSpace(m.Subject) == "" && get("template") == "":
		fail(w, http.StatusBadRequest, "'subject' parameter is missing")
		return
	case m.Text == "" && m.HTML == "" && get("amp-html") == "" && get("template") == "":
		fail(w, http.StatusBadRequest, "need at least one of 'text', 'html', 'amp-html' or 'template' parameters specified")
		return
	}
	if _, err := mail.ParseAddress(m.From); err != nil {
		fail(w, http.StatusBadRequest, "'from' parameter is not a valid address. please check documentation")
		return
	}
	// Duplicate addresses are automatically ignored.
	seen := map[string]bool{}
	for _, field := range r.Form["to"] {
		for _, one := range strings.Split(field, ",") {
			a, err := mail.ParseAddress(strings.TrimSpace(one))
			if err != nil {
				fail(w, http.StatusBadRequest, "'to' parameter is not a valid address. please check documentation")
				return
			}
			if !seen[a.Address] {
				seen[a.Address] = true
				m.To = append(m.To, a.Address)
			}
		}
	}
	total := 0
	for k, vs := range r.Form {
		if len(vs) == 0 {
			continue
		}
		switch {
		case strings.HasPrefix(k, "h:"):
			m.Headers[k[2:]] = vs[0]
		case strings.HasPrefix(k, "v:"):
			m.Variables[k[2:]] = vs[0]
		case strings.HasPrefix(k, "o:"):
			m.Options[k[2:]] = vs[0]
		}
		if len(k) > 1 && k[1] == ':' && strings.ContainsAny(k[:1], "ohvt") {
			total += len(k) + len(vs[0])
		}
	}
	if total > 16*1024 {
		fail(w, http.StatusBadRequest, "send options (o:, h:, v:, t:) are limited to 16KB total")
		return
	}
	now := f.now()
	m.AcceptedAt = now
	if dt := m.Options["deliverytime"]; dt != "" {
		at, err := mail.ParseDate(dt)
		if err != nil {
			fail(w, http.StatusBadRequest, "o:deliverytime is not a valid RFC-2822 date")
			return
		}
		if at.After(now.Add(maxSchedule)) {
			fail(w, http.StatusBadRequest, "o:deliverytime is too far in the future for this plan")
			return
		}
		m.DeliverAt = at
	}

	id := "<" + randomID() + "@" + domain + ">"
	m.ID = id
	if !strings.EqualFold(m.Options["testmode"], "yes") {
		f.mu.Lock()
		f.sent = append(f.sent, m)
		f.mu.Unlock()
	}
	reply(w, http.StatusOK, map[string]string{"id": id, "message": "Queued. Thank you."})
}

func randomID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102150405") + "." + hex.EncodeToString(b)
}
