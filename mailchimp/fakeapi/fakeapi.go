// Package fakeapi is an in-memory stand-in for the parts of Mailchimp's
// Marketing API v3 (https://mailchimp.com/developer/marketing/api/,
// fetched 2026-09-30) the mailchimp wrapper calls: enough of Campaigns,
// Campaigns > Content, Lists/Audiences and Reports to draft, publish,
// read back, count and take back an email_broadcast.
//
// It keeps state in memory for as long as the process serving Handler()
// runs, so what one request publishes, a later request on the same server
// can read back, list, count or retract -- which is what lets the
// wrapper's own tests, and the conformance suite run with -fake-venue
// against it, exercise a whole lifecycle. It accepts any credential: like
// the real API, authentication is a bearer token or HTTP Basic pair, but
// this fake never checks either, since Mendel always runs it with
// made-up ones.
package fakeapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// scheduleTimeLayout matches the wrapper's own (types.go): UTC, to the
// minute, the form Mailchimp's schedule action examples use.
const scheduleTimeLayout = "2006-01-02T15:04:05"

// list is a Mailchimp audience, as far as probe reads one.
type list struct {
	ID          string
	Name        string
	MemberCount int
}

// campaign is a Mailchimp "regular" campaign: enough of it to draft,
// publish, read back, meter and retract an email_broadcast.
//
// retracted and retractedAs are not fields the real Mailchimp API has;
// see mailchimp/types.go for why the wrapper needs the fake to carry them.
type campaign struct {
	ID          string
	ListID      string
	Title       string
	SubjectLine string
	Preheader   string
	FromName    string
	ReplyTo     string
	HTML        string
	PlainText   string
	// Status is one of Mailchimp's own: save, schedule, sending, sent.
	Status       string
	ScheduleTime time.Time
	SendTime     time.Time
	CreateTime   time.Time
	Retracted    bool
	RetractedAs  string
	// Opens, Clicks and Bounces are zero for every campaign this fake ever
	// sends: it delivers nothing, so nobody ever opens or clicks one, which
	// is the truthful answer read_metrics gives for a freshly sent
	// campaign, not a placeholder.
}

// server holds every list and campaign the process has seen.
type server struct {
	mu        sync.Mutex
	lists     map[string]*list
	campaigns map[string]*campaign
	nextID    int
}

// Handler answers as far as the wrapper's own calls need: List
// campaigns, Add campaign, Get campaign info, Delete campaign, Cancel
// campaign, Send campaign, Schedule campaign, Unschedule campaign, Get
// campaign content, Set campaign content, Get campaign report, Get list
// info, and Ping (https://mailchimp.com/developer/marketing/api/campaigns/,
// fetched 2026-09-30).
func Handler() http.Handler {
	s := &server{lists: map[string]*list{}, campaigns: map[string]*campaign{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", s.handlePing)
	mux.HandleFunc("GET /lists/{id}", s.handleGetList)
	mux.HandleFunc("POST /campaigns", s.handleCreateCampaign)
	mux.HandleFunc("GET /campaigns", s.handleListCampaigns)
	mux.HandleFunc("GET /campaigns/{id}", s.handleGetCampaign)
	mux.HandleFunc("DELETE /campaigns/{id}", s.handleDeleteCampaign)
	mux.HandleFunc("PUT /campaigns/{id}/content", s.handleSetContent)
	mux.HandleFunc("GET /campaigns/{id}/content", s.handleGetContent)
	mux.HandleFunc("POST /campaigns/{id}/actions/send", s.handleSend)
	mux.HandleFunc("POST /campaigns/{id}/actions/schedule", s.handleSchedule)
	mux.HandleFunc("POST /campaigns/{id}/actions/unschedule", s.handleUnschedule)
	mux.HandleFunc("POST /campaigns/{id}/actions/cancel-send", s.handleCancelSend)
	mux.HandleFunc("GET /reports/{id}", s.handleReport)
	return mux
}

// --- wire shapes, mirroring mailchimp/types.go ---

type wireCampaign struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	CreateTime string `json:"create_time,omitempty"`
	ArchiveURL string `json:"archive_url,omitempty"`
	Status     string `json:"status"`
	Recipients struct {
		ListID string `json:"list_id"`
	} `json:"recipients"`
	Settings struct {
		SubjectLine string `json:"subject_line"`
		Preheader   string `json:"preheader"`
		Title       string `json:"title"`
		FromName    string `json:"from_name"`
		ReplyTo     string `json:"reply_to"`
	} `json:"settings"`
	ScheduleTime      string `json:"schedule_time,omitempty"`
	SendTime          string `json:"send_time,omitempty"`
	MendelRetracted   bool   `json:"_mendel_retracted,omitempty"`
	MendelRetractedAs string `json:"_mendel_retracted_as,omitempty"`
}

func toWire(c *campaign) wireCampaign {
	w := wireCampaign{ID: c.ID, Type: "regular", Status: c.Status,
		ArchiveURL: "https://mailchimp.com/campaigns/" + c.ID,
		CreateTime: c.CreateTime.UTC().Format(time.RFC3339)}
	w.Recipients.ListID = c.ListID
	w.Settings.SubjectLine = c.SubjectLine
	w.Settings.Preheader = c.Preheader
	w.Settings.Title = c.Title
	w.Settings.FromName = c.FromName
	w.Settings.ReplyTo = c.ReplyTo
	if !c.ScheduleTime.IsZero() {
		w.ScheduleTime = c.ScheduleTime.Format(scheduleTimeLayout)
	}
	if !c.SendTime.IsZero() {
		w.SendTime = c.SendTime.Format(scheduleTimeLayout)
	}
	w.MendelRetracted = c.Retracted
	w.MendelRetractedAs = c.RetractedAs
	return w
}

// --- errors, in Mailchimp's own shape (Errors docs) ---

func writeError(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "https://mailchimp.com/developer/marketing/docs/errors/",
		"title":  title,
		"status": status,
		"detail": detail,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// --- handlers ---

func (s *server) handlePing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"health_status": "Everything's Chimpy!"})
}

func (s *server) handleGetList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusNotFound, "Resource Not Found", "no audience id was given")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.lists[id]
	if !ok {
		// A dedicated test/fake audience always "exists": there is no
		// signup flow this fake can drive to create one first.
		l = &list{ID: id, Name: "Fake audience " + id, MemberCount: 42}
		s.lists[id] = l
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":   l.ID,
		"name": l.Name,
		"stats": map[string]any{
			"member_count": l.MemberCount,
		},
	})
}

type createCampaignBody struct {
	Type       string `json:"type"`
	Recipients struct {
		ListID string `json:"list_id"`
	} `json:"recipients"`
	Settings struct {
		SubjectLine string `json:"subject_line"`
		Preheader   string `json:"preheader"`
		Title       string `json:"title"`
		FromName    string `json:"from_name"`
		ReplyTo     string `json:"reply_to"`
	} `json:"settings"`
}

func (s *server) handleCreateCampaign(w http.ResponseWriter, r *http.Request) {
	var body createCampaignBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid Resource", "the request body could not be read: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Recipients.ListID) == "" {
		writeError(w, http.StatusBadRequest, "Invalid Resource", "recipients.list_id is required")
		return
	}
	if strings.TrimSpace(body.Settings.SubjectLine) == "" {
		writeError(w, http.StatusBadRequest, "Invalid Resource", "settings.subject_line is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	c := &campaign{
		ID: fmt.Sprintf("mc%08d", s.nextID), ListID: body.Recipients.ListID,
		SubjectLine: body.Settings.SubjectLine, Preheader: body.Settings.Preheader,
		Title: body.Settings.Title, FromName: body.Settings.FromName, ReplyTo: body.Settings.ReplyTo,
		Status: "save", CreateTime: time.Now(),
	}
	s.campaigns[c.ID] = c
	writeJSON(w, http.StatusOK, toWire(c))
}

func (s *server) handleListCampaigns(w http.ResponseWriter, r *http.Request) {
	listID := r.URL.Query().Get("list_id")
	count := 1000
	if v := r.URL.Query().Get("count"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			count = n
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []wireCampaign
	for _, c := range sortedCampaigns(s.campaigns) {
		if listID != "" && c.ListID != listID {
			continue
		}
		out = append(out, toWire(c))
		if len(out) >= count {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaigns": out, "total_items": len(out)})
}

func (s *server) handleGetCampaign(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.campaigns[id]
	if !ok {
		writeError(w, http.StatusNotFound, "Resource Not Found", "no campaign with that id")
		return
	}
	writeJSON(w, http.StatusOK, toWire(c))
}

func (s *server) handleDeleteCampaign(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.campaigns[id]; !ok {
		writeError(w, http.StatusNotFound, "Resource Not Found", "no campaign with that id")
		return
	}
	delete(s.campaigns, id)
	w.WriteHeader(http.StatusNoContent)
}

type contentBody struct {
	HTML      string `json:"html"`
	PlainText string `json:"plain_text"`
}

func (s *server) handleSetContent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body contentBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid Resource", "the request body could not be read: "+err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.campaigns[id]
	if !ok {
		writeError(w, http.StatusNotFound, "Resource Not Found", "no campaign with that id")
		return
	}
	c.HTML, c.PlainText = body.HTML, body.PlainText
	writeJSON(w, http.StatusOK, body)
}

func (s *server) handleGetContent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.campaigns[id]
	if !ok {
		writeError(w, http.StatusNotFound, "Resource Not Found", "no campaign with that id")
		return
	}
	writeJSON(w, http.StatusOK, contentBody{HTML: c.HTML, PlainText: c.PlainText})
}

func (s *server) handleSend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.campaigns[id]
	if !ok {
		writeError(w, http.StatusNotFound, "Resource Not Found", "no campaign with that id")
		return
	}
	if c.Retracted {
		writeError(w, http.StatusBadRequest, "Campaign Not Saved", "this campaign was retracted")
		return
	}
	// Idempotent: sending an already-sent campaign again changes nothing
	// (Send campaign: "All other campaigns will send immediately").
	if c.Status != "sent" {
		c.Status = "sent"
		c.SendTime = time.Now()
	}
	w.WriteHeader(http.StatusNoContent)
}

type scheduleBody struct {
	ScheduleTime string `json:"schedule_time"`
}

func (s *server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body scheduleBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid Resource", "the request body could not be read: "+err.Error())
		return
	}
	at, err := time.Parse(scheduleTimeLayout, body.ScheduleTime)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid Resource", "schedule_time is not a readable timestamp: "+err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.campaigns[id]
	if !ok {
		writeError(w, http.StatusNotFound, "Resource Not Found", "no campaign with that id")
		return
	}
	if c.Status == "sent" && !c.Retracted {
		writeError(w, http.StatusBadRequest, "Campaign Not Saved", "this campaign has already been sent")
		return
	}
	c.Status = "schedule"
	c.ScheduleTime = at
	c.Retracted, c.RetractedAs = false, ""
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleUnschedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.campaigns[id]
	if !ok {
		writeError(w, http.StatusNotFound, "Resource Not Found", "no campaign with that id")
		return
	}
	if c.Retracted {
		// Safe twice: already unscheduled.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if c.Status != "schedule" {
		writeError(w, http.StatusBadRequest, "Campaign Not Saved",
			"Unschedule a scheduled campaign that hasn't started sending: this one is not scheduled")
		return
	}
	c.Status = "save"
	c.Retracted, c.RetractedAs = true, "paused"
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleCancelSend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.campaigns[id]
	if !ok {
		writeError(w, http.StatusNotFound, "Resource Not Found", "no campaign with that id")
		return
	}
	if c.Retracted {
		// Safe twice: already cancelled.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if c.Status != "sent" && c.Status != "sending" {
		writeError(w, http.StatusBadRequest, "Campaign Not Saved",
			"Cancel a Regular or Plain-Text Campaign after you send: this one has not been sent")
		return
	}
	c.Retracted, c.RetractedAs = true, "paused"
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.campaigns[id]
	if !ok {
		writeError(w, http.StatusNotFound, "Resource Not Found", "no campaign with that id")
		return
	}
	if c.Status != "sent" && c.Status != "sending" {
		writeError(w, http.StatusNotFound, "Resource Not Found", "get report details for a specific sent campaign: this one has not been sent")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          c.ID,
		"emails_sent": 1,
		"opens":       map[string]any{"opens_total": 0, "unique_opens": 0},
		"clicks":      map[string]any{"clicks_total": 0, "unique_subscriber_clicks": 0},
		"bounces":     map[string]any{"hard_bounces": 0, "soft_bounces": 0},
	})
}

// sortedCampaigns orders by id, so List campaigns answers the same order on
// every call: helpful for a test to assert against, and true to the real
// endpoint's own default sort (by id) when nothing else is asked for.
func sortedCampaigns(m map[string]*campaign) []*campaign {
	out := make([]*campaign, 0, len(m))
	for _, c := range m {
		out = append(out, c)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].ID > out[j].ID; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
