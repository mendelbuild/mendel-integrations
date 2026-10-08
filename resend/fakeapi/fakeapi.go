// Package fakeapi is an in-memory stand-in for the parts of Resend's API the
// wrapper calls, answering as Resend's documentation (spec/resend/) says the
// real one does, and keeping state between requests: what is sent through it
// can be retrieved, listed, rescheduled and cancelled.
//
// Documented: Retrieve Sent Email (GET /emails/:id), List Sent Emails
// (GET /emails), Update Email (PATCH /emails/:id, scheduled_at) and Cancel
// Email (POST /emails/:id/cancel). Send Email (POST /emails) is not among the
// pages that were fetched; its request is written from the fields Retrieve
// returns, and its Idempotency-Key header from memory (see resend/README.md).
//
// It accepts any credential. Paths are matched from the "emails" segment on,
// so the fake answers whatever prefix it is mounted under.
package fakeapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Handler returns a fresh fake with no emails.
func Handler() http.Handler { return &fake{byID: map[string]*email{}, keys: map[string]string{}} }

type tag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type email struct {
	id, from, subject string
	messageID         string
	to, cc, bcc, rto  []string
	html, text        *string
	created           time.Time
	scheduledAt       *time.Time
	canceled          bool
	tags              []tag
	seq               int
}

type fake struct {
	mu    sync.Mutex
	byID  map[string]*email
	order []*email // oldest first
	keys  map[string]string
}

const timeFormat = "2006-01-02 15:04:05.000000+00"

func (e *email) lastEvent(now time.Time) string {
	switch {
	case e.canceled:
		return "canceled"
	case e.scheduledAt != nil && now.Before(*e.scheduledAt):
		return "scheduled"
	}
	return "delivered"
}

func list(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (e *email) full(now time.Time) map[string]any {
	m := e.summary(now)
	m["object"] = "email"
	m["html"] = e.html
	m["text"] = e.text
	m["bcc"], m["cc"], m["reply_to"] = list(e.bcc), list(e.cc), list(e.rto)
	tags := e.tags
	if tags == nil {
		tags = []tag{}
	}
	m["tags"] = tags
	return m
}

func (e *email) summary(now time.Time) map[string]any {
	m := map[string]any{"id": e.id, "message_id": e.messageID, "to": e.to, "from": e.from,
		"created_at": e.created.UTC().Format(timeFormat), "subject": e.subject,
		"bcc": nil, "cc": nil, "reply_to": nil, "last_event": e.lastEvent(now), "scheduled_at": nil}
	if e.scheduledAt != nil {
		m["scheduled_at"] = e.scheduledAt.UTC().Format(time.RFC3339Nano)
	}
	return m
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, name, msg string) {
	writeJSON(w, status, map[string]any{"statusCode": status, "name": name, "message": msg})
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	at := -1
	for i, s := range segs {
		if s == "emails" {
			at = i
			break
		}
	}
	if at < 0 {
		apiError(w, 404, "not_found", "The requested endpoint does not exist.")
		return
	}
	rest := segs[at+1:]
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case len(rest) == 0 && r.Method == http.MethodPost:
		f.send(w, r)
	case len(rest) == 0 && r.Method == http.MethodGet:
		f.list(w, r)
	case len(rest) == 1 && r.Method == http.MethodGet:
		if e := f.find(w, rest[0]); e != nil {
			writeJSON(w, 200, e.full(time.Now()))
		}
	case len(rest) == 1 && r.Method == http.MethodPatch:
		f.update(w, r, rest[0])
	case len(rest) == 2 && rest[1] == "cancel" && r.Method == http.MethodPost:
		f.cancel(w, rest[0])
	default:
		apiError(w, 405, "method_not_allowed", "Method is not allowed for the requested path.")
	}
}

func (f *fake) find(w http.ResponseWriter, id string) *email {
	e := f.byID[id]
	if e == nil {
		apiError(w, 404, "not_found", "Email not found")
	}
	return e
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// recipients reads "to" as Resend takes it: one address or a list.
func recipients(v any) []string {
	switch t := v.(type) {
	case string:
		if t != "" {
			return []string{t}
		}
	case []any:
		var out []string
		for _, x := range t {
			s, ok := x.(string)
			if !ok || s == "" {
				return nil
			}
			out = append(out, s)
		}
		return out
	}
	return nil
}

func str(v any) *string {
	if s, ok := v.(string); ok && s != "" {
		return &s
	}
	return nil
}

func (f *fake) send(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiError(w, 422, "invalid_parameter", "The request body is not valid JSON.")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key != "" {
		if id, ok := f.keys[key]; ok {
			writeJSON(w, 200, map[string]any{"id": id})
			return
		}
	}
	from, subject := str(body["from"]), str(body["subject"])
	to := recipients(body["to"])
	switch {
	case from == nil:
		apiError(w, 422, "missing_required_field", "Missing `from` field.")
		return
	case len(to) == 0:
		apiError(w, 422, "missing_required_field", "Missing `to` field.")
		return
	case subject == nil:
		apiError(w, 422, "missing_required_field", "Missing `subject` field.")
		return
	case str(body["html"]) == nil && str(body["text"]) == nil:
		apiError(w, 422, "missing_required_field", "Missing `html` or `text` field.")
		return
	}
	e := &email{id: newID(), from: *from, subject: *subject, to: to, html: str(body["html"]), text: str(body["text"]),
		created: time.Now().UTC(), seq: len(f.order)}
	e.messageID = "<" + e.id + "@email.example.com>"
	if s := str(body["scheduled_at"]); s != nil {
		t, err := time.Parse(time.RFC3339, *s)
		if err != nil {
			apiError(w, 422, "invalid_scheduled_at", "`scheduled_at` must be an ISO 8601 date.")
			return
		}
		e.scheduledAt = &t
	}
	if raw, ok := body["tags"].([]any); ok {
		for _, t := range raw {
			m, _ := t.(map[string]any)
			n, v := str(m["name"]), str(m["value"])
			if n == nil || v == nil {
				apiError(w, 422, "invalid_parameter", "Each tag needs a name and a value.")
				return
			}
			e.tags = append(e.tags, tag{*n, *v})
		}
	}
	f.byID[e.id] = e
	f.order = append(f.order, e)
	if key != "" {
		f.keys[key] = e.id
	}
	writeJSON(w, 200, map[string]any{"id": e.id})
}

func (f *fake) list(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			apiError(w, 422, "validation_error", "The `limit` must be between 1 and 100.")
			return
		}
		limit = n
	}
	newest := make([]*email, len(f.order))
	for i, e := range f.order {
		newest[len(f.order)-1-i] = e
	}
	sort.SliceStable(newest, func(i, j int) bool { return newest[i].seq > newest[j].seq })
	if after := r.URL.Query().Get("after"); after != "" {
		i := -1
		for k, e := range newest {
			if e.id == after {
				i = k
			}
		}
		if i < 0 {
			apiError(w, 404, "not_found", "The `after` email was not found.")
			return
		}
		newest = newest[i+1:]
	}
	more := len(newest) > limit
	if more {
		newest = newest[:limit]
	}
	now := time.Now()
	data := make([]map[string]any, 0, len(newest))
	for _, e := range newest {
		data = append(data, e.summary(now))
	}
	writeJSON(w, 200, map[string]any{"object": "list", "has_more": more, "data": data})
}

func (f *fake) update(w http.ResponseWriter, r *http.Request, id string) {
	e := f.find(w, id)
	if e == nil {
		return
	}
	var body struct {
		ScheduledAt string `json:"scheduled_at"`
	}
	t, err := time.Time{}, json.NewDecoder(r.Body).Decode(&body)
	if err == nil {
		t, err = time.Parse(time.RFC3339, body.ScheduledAt)
	}
	if err != nil {
		apiError(w, 422, "invalid_scheduled_at", "`scheduled_at` must be an ISO 8601 date.")
		return
	}
	if e.lastEvent(time.Now()) != "scheduled" {
		apiError(w, 422, "validation_error", fmt.Sprintf("Only a scheduled email can be updated; this one is %s.", e.lastEvent(time.Now())))
		return
	}
	e.scheduledAt = &t
	writeJSON(w, 200, map[string]any{"object": "email", "id": e.id})
}

func (f *fake) cancel(w http.ResponseWriter, id string) {
	e := f.find(w, id)
	if e == nil {
		return
	}
	if ev := e.lastEvent(time.Now()); ev != "scheduled" {
		apiError(w, 422, "validation_error", fmt.Sprintf("Only a scheduled email can be canceled; this one is %s.", ev))
		return
	}
	e.canceled = true
	writeJSON(w, 200, map[string]any{"object": "email", "id": e.id})
}
