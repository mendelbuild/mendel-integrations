// Package fakeapi is an in-memory stand-in for the parts of Ayrshare's API that
// the wrapper calls, written against the documentation fetched into
// spec/ayrshare/ (see ayrshare/README.md). It keeps state between requests, so
// what is posted through it can be read, listed and deleted. It accepts any
// credential.
//
// What it answers, and where the documentation says so:
//
//   - POST /post: the post endpoint. requiresApproval holds the post as
//     "awaiting approval"; scheduleDate in the future makes it "scheduled"; a
//     repeated idempotencyKey is an error, whatever became of the first post;
//     media for Reddit must be JPG, PNG, GIF or WEBP (no video).
//   - GET /post/{id}: a post, deleted ones included.
//   - PATCH /post: approved:true releases a post held for approval, and
//     scheduleDate moves it.
//   - DELETE /post: deletes a post; it stays in the history as "deleted".
//   - GET /history: posts newest first, 20 by default and at most 500
//     ("count"), notes included, deleted posts included.
//
// The documentation fetched does not give the shape of the error body or the
// paths' prefix, so errors are {"action","status":"error","code","message"}
// with an HTTP 4xx, and any prefix before /post and /history is accepted.
package fakeapi

import (
	"crypto/rand"
	"encoding/json"
	"math/big"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Statuses a post has here, in the documentation's words.
const (
	statusAwaiting  = "awaiting approval"
	statusScheduled = "scheduled"
	statusSuccess   = "success"
	statusDeleted   = "deleted"
)

type postID struct {
	Status   string `json:"status"`
	Platform string `json:"platform"`
	ID       string `json:"id"`
	PostURL  string `json:"postUrl"`
}

type post struct {
	ID             string
	RefID          string
	Status         string
	Text           string
	Platforms      []string
	MediaURLs      []string
	Notes          string
	ScheduleDate   string
	Created        time.Time
	IdempotencyKey string
	PostIDs        []postID
	seq            int
}

type server struct {
	mu    sync.Mutex
	posts map[string]*post
	keys  map[string]bool
	seq   int
}

// Handler returns a new, empty fake of Ayrshare's API.
func Handler() http.Handler {
	return &server{posts: map[string]*post{}, keys: map[string]bool{}}
}

var (
	postRoute    = regexp.MustCompile(`(?:^|/)post(?:/([^/]+))?$`)
	historyRoute = regexp.MustCompile(`(?:^|/)history$`)

	knownPlatforms = map[string]bool{"bluesky": true, "facebook": true, "gmb": true, "instagram": true,
		"linkedin": true, "pinterest": true, "reddit": true, "snapchat": true, "telegram": true,
		"threads": true, "tiktok": true, "twitter": true, "youtube": true}
	// Facebook, Instagram, LinkedIn, Threads, TikTok and X/Twitter take media
	// with no text ("Image or Video Only").
	blankTextOK = map[string]bool{"facebook": true, "instagram": true, "linkedin": true,
		"threads": true, "tiktok": true, "twitter": true}
	redditImages = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true}
	videoExts    = map[string]bool{".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".webm": true, ".m4v": true}
)

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := strings.TrimRight(r.URL.Path, "/")
	switch {
	case historyRoute.MatchString(p):
		if r.Method != http.MethodGet {
			fail(w, http.StatusMethodNotAllowed, 405, "Method not allowed.")
			return
		}
		s.history(w, r)
	case postRoute.MatchString(p):
		id := postRoute.FindStringSubmatch(p)[1]
		switch r.Method {
		case http.MethodPost:
			s.create(w, r)
		case http.MethodGet:
			s.get(w, id)
		case http.MethodPatch:
			s.patch(w, r, id)
		case http.MethodDelete:
			s.del(w, r, id)
		default:
			fail(w, http.StatusMethodNotAllowed, 405, "Method not allowed.")
		}
	default:
		fail(w, http.StatusNotFound, 404, "No such endpoint.")
	}
}

func fail(w http.ResponseWriter, httpCode, code int, msg string) {
	reply(w, httpCode, map[string]any{"action": "post", "status": "error", "code": code, "message": msg})
}

func reply(w http.ResponseWriter, httpCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpCode)
	_ = json.NewEncoder(w).Encode(v)
}

func readBody(r *http.Request) (map[string]any, bool) {
	var m map[string]any
	if r.Body == nil {
		return map[string]any{}, true
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&m); err != nil {
		return nil, false
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, true
}

const alnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randID(n int) string {
	b := make([]byte, n)
	for i := range b {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alnum))))
		b[i] = alnum[k.Int64()]
	}
	return string(b)
}

// settle makes a scheduled post whose time has come a published one.
func (s *server) settle(p *post) {
	if p.Status != statusScheduled {
		return
	}
	at, err := time.Parse(time.RFC3339, p.ScheduleDate)
	if err == nil && !at.After(time.Now()) {
		s.publish(p)
	}
}

func (s *server) publish(p *post) {
	p.Status = statusSuccess
	p.PostIDs = nil
	for _, pl := range p.Platforms {
		id := randID(8)
		u := "https://www.example.com/" + pl + "/" + id
		if pl == "reddit" {
			u = "https://www.reddit.com/r/fakeapi/comments/" + strings.ToLower(id) + "/"
		}
		p.PostIDs = append(p.PostIDs, postID{Status: "success", Platform: pl, ID: id, PostURL: u})
	}
}

func (p *post) view() map[string]any {
	v := map[string]any{"id": p.ID, "refId": p.RefID, "status": p.Status, "post": p.Text,
		"platforms": p.Platforms, "notes": p.Notes, "created": p.Created.UTC().Format(time.RFC3339)}
	if len(p.MediaURLs) > 0 {
		v["mediaUrls"] = p.MediaURLs
	}
	if p.ScheduleDate != "" {
		v["scheduleDate"] = p.ScheduleDate
	}
	if len(p.PostIDs) > 0 {
		v["postIds"] = p.PostIDs
	}
	return v
}

func stringList(v any) ([]string, bool) {
	switch x := v.(type) {
	case nil:
		return nil, true
	case string:
		return []string{x}, true
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

func (s *server) create(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(r)
	if !ok {
		fail(w, http.StatusBadRequest, 101, "The request body is not valid JSON.")
		return
	}
	text, isText := body["post"].(string)
	if !isText {
		fail(w, http.StatusBadRequest, 102, "The post parameter is required and must be text.")
		return
	}
	platforms, ok := stringList(body["platforms"])
	if !ok || len(platforms) == 0 {
		fail(w, http.StatusBadRequest, 103, "The platforms parameter is required: a list of the social networks to post to.")
		return
	}
	for _, pl := range platforms {
		if !knownPlatforms[pl] {
			fail(w, http.StatusBadRequest, 104, "Unknown platform: "+pl+".")
			return
		}
	}
	media, ok := stringList(body["mediaUrls"])
	if !ok {
		fail(w, http.StatusBadRequest, 105, "mediaUrls must be a list of URLs.")
		return
	}
	for _, m := range media {
		u, err := url.Parse(m)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(m, " ") {
			fail(w, http.StatusBadRequest, 106, "Media URL is not valid and directly accessible: "+m)
			return
		}
		ext := strings.ToLower(path.Ext(u.Path))
		for _, pl := range platforms {
			if pl != "reddit" {
				continue
			}
			if videoExts[ext] {
				fail(w, http.StatusBadRequest, 107, "Video API posting is not supported by Reddit.")
				return
			}
			if ext != "" && !redditImages[ext] {
				fail(w, http.StatusBadRequest, 108, "Reddit images must be JPG, PNG, GIF or WEBP.")
				return
			}
		}
	}
	if strings.TrimSpace(text) == "" {
		allowed := len(media) > 0
		for _, pl := range platforms {
			allowed = allowed && blankTextOK[pl]
		}
		if !allowed {
			fail(w, http.StatusBadRequest, 109, "The post text is empty, and one of the networks needs text.")
			return
		}
	}
	var sched time.Time
	schedText, _ := body["scheduleDate"].(string)
	if schedText != "" {
		t, err := time.Parse(time.RFC3339, schedText)
		if err != nil {
			fail(w, http.StatusBadRequest, 110, "scheduleDate must be an ISO-8601 UTC time, such as 2026-07-08T12:30:00Z.")
			return
		}
		sched = t
	}
	key, _ := body["idempotencyKey"].(string)
	if key != "" {
		if s.keys[key] {
			fail(w, http.StatusBadRequest, 137, "Duplicate idempotencyKey found. The request was already made with this key.")
			return
		}
	}
	notes, _ := body["notes"].(string)
	needsApproval, _ := body["requiresApproval"].(bool)

	s.seq++
	p := &post{ID: randID(20), RefID: randID(25), Text: text, Platforms: platforms, MediaURLs: media,
		Notes: notes, Created: time.Now(), IdempotencyKey: key, seq: s.seq}
	if !sched.IsZero() {
		p.ScheduleDate = sched.UTC().Format(time.RFC3339)
	}
	switch {
	case needsApproval:
		p.Status = statusAwaiting
	case !sched.IsZero() && sched.After(time.Now()):
		p.Status = statusScheduled
	default:
		s.publish(p)
	}
	s.posts[p.ID] = p
	if key != "" {
		s.keys[key] = true
	}
	out := map[string]any{"status": p.Status, "id": p.ID, "refId": p.RefID, "post": p.Text}
	if p.ScheduleDate != "" {
		out["scheduleDate"] = p.ScheduleDate
	}
	if len(p.PostIDs) > 0 {
		out["postIds"] = p.PostIDs
		out["errors"] = []any{}
	}
	reply(w, http.StatusOK, out)
}

func (s *server) find(id string) *post {
	p := s.posts[id]
	if p != nil {
		s.settle(p)
	}
	return p
}

func (s *server) get(w http.ResponseWriter, id string) {
	if id == "" {
		fail(w, http.StatusBadRequest, 111, "A post id is required.")
		return
	}
	p := s.find(id)
	if p == nil {
		fail(w, http.StatusNotFound, 112, "Post not found.")
		return
	}
	reply(w, http.StatusOK, p.view())
}

func (s *server) patch(w http.ResponseWriter, r *http.Request, id string) {
	body, ok := readBody(r)
	if !ok {
		fail(w, http.StatusBadRequest, 101, "The request body is not valid JSON.")
		return
	}
	if id == "" {
		id, _ = body["id"].(string)
	}
	p := s.find(id)
	if id == "" {
		fail(w, http.StatusBadRequest, 111, "A post id is required.")
		return
	}
	if p == nil {
		fail(w, http.StatusNotFound, 112, "Post not found.")
		return
	}
	if p.Status == statusDeleted {
		fail(w, http.StatusBadRequest, 113, "The post was deleted and cannot be updated.")
		return
	}
	if p.Status == statusSuccess {
		fail(w, http.StatusBadRequest, 114, "The post was already published and cannot be updated.")
		return
	}
	if sd, ok := body["scheduleDate"].(string); ok && sd != "" {
		t, err := time.Parse(time.RFC3339, sd)
		if err != nil {
			fail(w, http.StatusBadRequest, 110, "scheduleDate must be an ISO-8601 UTC time, such as 2026-07-08T12:30:00Z.")
			return
		}
		p.ScheduleDate = t.UTC().Format(time.RFC3339)
	}
	if n, ok := body["notes"].(string); ok {
		p.Notes = n
	}
	if approved, _ := body["approved"].(bool); approved && p.Status == statusAwaiting {
		// "If the scheduleDate is included and the date is in the past, the post
		// will be published immediately upon approval."
		at, err := time.Parse(time.RFC3339, p.ScheduleDate)
		if p.ScheduleDate != "" && err == nil && at.After(time.Now()) {
			p.Status = statusScheduled
		} else {
			s.publish(p)
		}
	}
	reply(w, http.StatusOK, map[string]any{"status": p.Status, "id": p.ID})
}

func (s *server) del(w http.ResponseWriter, r *http.Request, id string) {
	if id == "" {
		if body, ok := readBody(r); ok {
			id, _ = body["id"].(string)
		}
	}
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		fail(w, http.StatusBadRequest, 111, "A post id is required.")
		return
	}
	p := s.find(id)
	if p == nil {
		fail(w, http.StatusNotFound, 112, "Post not found.")
		return
	}
	if p.Status == statusDeleted {
		fail(w, http.StatusBadRequest, 115, "The post was already deleted.")
		return
	}
	// The post stays in the history, as "deleted".
	p.Status = statusDeleted
	reply(w, http.StatusOK, map[string]any{"status": "success", "id": p.ID})
}

func (s *server) history(w http.ResponseWriter, r *http.Request) {
	count := 20
	if c := r.URL.Query().Get("count"); c != "" {
		n, err := strconv.Atoi(c)
		if err != nil || n < 1 {
			fail(w, http.StatusBadRequest, 116, "count must be a positive integer.")
			return
		}
		count = n
	}
	if count > 500 {
		count = 500
	}
	want := r.URL.Query().Get("status")
	all := make([]*post, 0, len(s.posts))
	for _, p := range s.posts {
		s.settle(p)
		if want == "" || p.Status == want {
			all = append(all, p)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].seq > all[j].seq })
	if len(all) > count {
		all = all[:count]
	}
	out := make([]map[string]any, 0, len(all))
	for _, p := range all {
		out = append(out, p.view())
	}
	reply(w, http.StatusOK, out)
}
