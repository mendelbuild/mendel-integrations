// Package fakeapi is an in-memory stand-in for the parts of Gmail API that the
// gmail-api wrapper calls: users.getProfile, users.drafts (create, get, list,
// update, delete, send) and users.messages (send, get, list, delete).
//
// It answers as the documentation fetched on 2026-10-21 says the real service
// does (see gmail-api/README.md), keeps its state between requests, and
// accepts any credential. Every route is found by the "/gmail/v1/" in the
// request path, so it answers whether it is mounted at the root or behind a
// path prefix that a connection's endpoint keeps.
package fakeapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"sort"
	"strings"
	"sync"
)

const apiRoot = "/gmail/v1/"

// Handler returns a new fake, empty, whose state lives as long as the handler.
func Handler() http.Handler {
	return &fake{drafts: map[string]*draft{}, messages: map[string]*message{}}
}

type message struct {
	ID       string
	ThreadID string
	Labels   []string
	Raw      []byte
	seq      int
}

type draft struct {
	ID      string
	Message string // id of the message the draft holds
	seq     int
}

type fake struct {
	mu       sync.Mutex
	next     int
	drafts   map[string]*draft
	messages map[string]*message
}

func (f *fake) newID() string {
	f.next++
	return fmt.Sprintf("%016x", 0x18f3a5c00000+f.next*31)
}

// --- wire shapes ---

type wireMessage struct {
	ID           string       `json:"id"`
	ThreadID     string       `json:"threadId,omitempty"`
	LabelIDs     []string     `json:"labelIds,omitempty"`
	Raw          string       `json:"raw,omitempty"`
	SizeEstimate int          `json:"sizeEstimate,omitempty"`
	Payload      *wirePayload `json:"payload,omitempty"`
}

type wirePayload struct {
	Headers []wireHeader `json:"headers"`
}

type wireHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type wireDraft struct {
	ID      string       `json:"id"`
	Message *wireMessage `json:"message,omitempty"`
}

type draftBody struct {
	ID      string `json:"id"`
	Message *struct {
		Raw string `json:"raw"`
	} `json:"message"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, code int, status, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]any{
		"code": code, "message": msg, "status": status,
		"errors": []map[string]any{{"message": msg, "domain": "global", "reason": reasonFor(status)}},
	}})
}

func reasonFor(status string) string {
	switch status {
	case "NOT_FOUND":
		return "notFound"
	case "INVALID_ARGUMENT":
		return "invalidArgument"
	}
	return "backendError"
}

func notFound(w http.ResponseWriter) {
	apiError(w, http.StatusNotFound, "NOT_FOUND", "Requested entity was not found.")
}

// decodeRaw reads a base64url string, with or without padding.
func decodeRaw(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	s = strings.NewReplacer("+", "-", "/", "_").Replace(s)
	return base64.RawURLEncoding.DecodeString(s)
}

func encodeRaw(b []byte) string { return base64.URLEncoding.EncodeToString(b) }

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	i := strings.Index(r.URL.Path, apiRoot)
	if i < 0 {
		notFound(w)
		return
	}
	segs := strings.Split(strings.Trim(r.URL.Path[i+len(apiRoot):], "/"), "/")
	if len(segs) < 3 || segs[0] != "users" || segs[1] == "" {
		notFound(w)
		return
	}
	user, segs := segs[1], segs[2:]
	f.mu.Lock()
	defer f.mu.Unlock()

	method := r.Method
	switch {
	case segs[0] == "profile" && len(segs) == 1:
		f.onlyMethod(w, method, http.MethodGet, func() { f.profile(w, user) })
	case segs[0] == "drafts" && len(segs) == 1:
		switch method {
		case http.MethodPost:
			f.createDraft(w, r)
		case http.MethodGet:
			f.listDrafts(w)
		default:
			methodNotAllowed(w)
		}
	case segs[0] == "drafts" && len(segs) == 2 && segs[1] == "send":
		f.onlyMethod(w, method, http.MethodPost, func() { f.sendDraft(w, r) })
	case segs[0] == "drafts" && len(segs) == 2:
		switch method {
		case http.MethodGet:
			f.getDraft(w, r, segs[1])
		case http.MethodPut:
			f.updateDraft(w, r, segs[1])
		case http.MethodDelete:
			f.deleteDraft(w, segs[1])
		default:
			methodNotAllowed(w)
		}
	case segs[0] == "messages" && len(segs) == 1:
		f.onlyMethod(w, method, http.MethodGet, func() { f.listMessages(w) })
	case segs[0] == "messages" && len(segs) == 2 && segs[1] == "send":
		f.onlyMethod(w, method, http.MethodPost, func() { f.sendMessage(w, r) })
	case segs[0] == "messages" && len(segs) == 2:
		switch method {
		case http.MethodGet:
			f.getMessage(w, r, segs[1])
		case http.MethodDelete:
			f.deleteMessage(w, segs[1])
		default:
			methodNotAllowed(w)
		}
	default:
		notFound(w)
	}
}

func methodNotAllowed(w http.ResponseWriter) {
	apiError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "The method is not allowed for this resource.")
}

func (f *fake) onlyMethod(w http.ResponseWriter, got, want string, do func()) {
	if got != want {
		methodNotAllowed(w)
		return
	}
	do()
}

// --- users.getProfile ---

func (f *fake) profile(w http.ResponseWriter, user string) {
	addr := user
	if !strings.Contains(user, "@") {
		addr = "owner@example.com"
	}
	writeJSON(w, http.StatusOK, map[string]any{"emailAddress": addr, "messagesTotal": len(f.messages),
		"threadsTotal": len(f.messages), "historyId": fmt.Sprint(f.next)})
}

// --- helpers ---

func readBody(w http.ResponseWriter, r *http.Request, into any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 36<<20))
	if err != nil {
		apiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Could not read the request body.")
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	if err := json.Unmarshal(body, into); err != nil {
		apiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid JSON payload received.")
		return false
	}
	return true
}

// recipients counts the addresses in To, Cc and Bcc of an RFC 2822 message.
func recipients(raw []byte) (int, error) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, h := range []string{"To", "Cc", "Bcc"} {
		for _, v := range m.Header[h] {
			list, err := mail.ParseAddressList(v)
			if err != nil {
				return 0, fmt.Errorf("invalid %s header", h)
			}
			n += len(list)
		}
	}
	return n, nil
}

func (f *fake) addMessage(raw []byte, labels ...string) *message {
	id := f.newID()
	m := &message{ID: id, ThreadID: f.newID(), Labels: labels, Raw: raw, seq: f.next}
	f.messages[id] = m
	return m
}

func (m *message) wire(format string) *wireMessage {
	out := &wireMessage{ID: m.ID, ThreadID: m.ThreadID, LabelIDs: m.Labels}
	switch format {
	case "minimal":
	case "raw":
		out.Raw = encodeRaw(m.Raw)
		out.SizeEstimate = len(m.Raw)
	default: // full, metadata, and the default (full)
		out.SizeEstimate = len(m.Raw)
		p := &wirePayload{Headers: []wireHeader{}}
		if mm, err := mail.ReadMessage(bytes.NewReader(m.Raw)); err == nil {
			names := make([]string, 0, len(mm.Header))
			for n := range mm.Header {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				for _, v := range mm.Header[n] {
					p.Headers = append(p.Headers, wireHeader{Name: n, Value: v})
				}
			}
		}
		out.Payload = p
	}
	return out
}

func validFormat(w http.ResponseWriter, format string) bool {
	switch format {
	case "", "minimal", "full", "raw", "metadata":
		return true
	}
	apiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", fmt.Sprintf("Invalid value at 'format': %q", format))
	return false
}

// --- users.drafts ---

func (f *fake) rawOf(w http.ResponseWriter, b *draftBody) ([]byte, bool) {
	if b.Message == nil || b.Message.Raw == "" {
		return nil, true
	}
	raw, err := decodeRaw(b.Message.Raw)
	if err != nil {
		apiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid value at 'message.raw': not base64url.")
		return nil, false
	}
	return raw, true
}

func (f *fake) createDraft(w http.ResponseWriter, r *http.Request) {
	var b draftBody
	if !readBody(w, r, &b) {
		return
	}
	raw, ok := f.rawOf(w, &b)
	if !ok {
		return
	}
	m := f.addMessage(raw, "DRAFT")
	d := &draft{ID: f.newID(), Message: m.ID, seq: f.next}
	f.drafts[d.ID] = d
	writeJSON(w, http.StatusOK, wireDraft{ID: d.ID, Message: m.wire("minimal")})
}

func (f *fake) getDraft(w http.ResponseWriter, r *http.Request, id string) {
	format := r.URL.Query().Get("format")
	if !validFormat(w, format) {
		return
	}
	d, ok := f.drafts[id]
	if !ok {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, wireDraft{ID: d.ID, Message: f.messages[d.Message].wire(format)})
}

func (f *fake) listDrafts(w http.ResponseWriter) {
	ds := make([]*draft, 0, len(f.drafts))
	for _, d := range f.drafts {
		ds = append(ds, d)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].seq > ds[j].seq })
	out := make([]wireDraft, 0, len(ds))
	for _, d := range ds {
		out = append(out, wireDraft{ID: d.ID, Message: f.messages[d.Message].wire("minimal")})
	}
	resp := map[string]any{"resultSizeEstimate": len(out)}
	if len(out) > 0 {
		resp["drafts"] = out
	}
	writeJSON(w, http.StatusOK, resp)
}

func (f *fake) updateDraft(w http.ResponseWriter, r *http.Request, id string) {
	var b draftBody
	if !readBody(w, r, &b) {
		return
	}
	d, ok := f.drafts[id]
	if !ok {
		notFound(w)
		return
	}
	raw, ok := f.rawOf(w, &b)
	if !ok {
		return
	}
	// The message is destroyed and replaced; the draft's id is stable.
	delete(f.messages, d.Message)
	m := f.addMessage(raw, "DRAFT")
	d.Message = m.ID
	writeJSON(w, http.StatusOK, wireDraft{ID: d.ID, Message: m.wire("minimal")})
}

func (f *fake) deleteDraft(w http.ResponseWriter, id string) {
	d, ok := f.drafts[id]
	if !ok {
		notFound(w)
		return
	}
	delete(f.messages, d.Message)
	delete(f.drafts, id)
	w.WriteHeader(http.StatusNoContent)
}

// sendDraft sends the draft whose id the body names, as it is or replaced by
// the body's message; the draft is deleted and a new message with the SENT
// label is answered.
func (f *fake) sendDraft(w http.ResponseWriter, r *http.Request) {
	var b draftBody
	if !readBody(w, r, &b) {
		return
	}
	if b.ID == "" {
		apiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Missing draft id.")
		return
	}
	d, ok := f.drafts[b.ID]
	if !ok {
		notFound(w)
		return
	}
	raw := f.messages[d.Message].Raw
	if b.Message != nil && b.Message.Raw != "" {
		var ok bool
		if raw, ok = f.rawOf(w, &b); !ok {
			return
		}
	}
	if !f.deliverable(w, raw) {
		return
	}
	delete(f.messages, d.Message)
	delete(f.drafts, d.ID)
	m := f.addMessage(raw, "SENT")
	writeJSON(w, http.StatusOK, m.wire("minimal"))
}

// deliverable answers 400 for a message that could not be sent: unreadable,
// or with no recipient in To, Cc or Bcc.
func (f *fake) deliverable(w http.ResponseWriter, raw []byte) bool {
	n, err := recipients(raw)
	switch {
	case err != nil:
		apiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid message: "+err.Error())
		return false
	case n == 0:
		apiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Recipient address required")
		return false
	}
	return true
}

// --- users.messages ---

func (f *fake) sendMessage(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Raw string `json:"raw"`
	}
	if !readBody(w, r, &b) {
		return
	}
	if b.Raw == "" {
		apiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid message: raw is empty.")
		return
	}
	raw, err := decodeRaw(b.Raw)
	if err != nil {
		apiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid value at 'raw': not base64url.")
		return
	}
	if !f.deliverable(w, raw) {
		return
	}
	m := f.addMessage(raw, "SENT")
	writeJSON(w, http.StatusOK, m.wire("minimal"))
}

func (f *fake) getMessage(w http.ResponseWriter, r *http.Request, id string) {
	format := r.URL.Query().Get("format")
	if !validFormat(w, format) {
		return
	}
	m, ok := f.messages[id]
	if !ok {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, m.wire(format))
}

func (f *fake) listMessages(w http.ResponseWriter) {
	ms := make([]*message, 0, len(f.messages))
	for _, m := range f.messages {
		ms = append(ms, m)
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].seq > ms[j].seq })
	out := make([]wireMessage, 0, len(ms))
	for _, m := range ms {
		out = append(out, wireMessage{ID: m.ID, ThreadID: m.ThreadID})
	}
	resp := map[string]any{"resultSizeEstimate": len(out)}
	if len(out) > 0 {
		resp["messages"] = out
	}
	writeJSON(w, http.StatusOK, resp)
}

func (f *fake) deleteMessage(w http.ResponseWriter, id string) {
	if _, ok := f.messages[id]; !ok {
		notFound(w)
		return
	}
	delete(f.messages, id)
	for did, d := range f.drafts {
		if d.Message == id {
			delete(f.drafts, did)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
