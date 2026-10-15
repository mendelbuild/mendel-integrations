package fakeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func do(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any, []any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	var l []any
	if json.Unmarshal(rec.Body.Bytes(), &m) != nil {
		_ = json.Unmarshal(rec.Body.Bytes(), &l)
	}
	return rec.Code, m, l
}

func TestPostGetHistoryDelete(t *testing.T) {
	h := Handler()
	code, p, _ := do(t, h, "POST", "/post", `{"post":"hi","platforms":["reddit"],"notes":"n1","idempotencyKey":"k"}`)
	if code != 200 || p["status"] != "success" || p["id"] == "" {
		t.Fatalf("%d %v", code, p)
	}
	id := p["id"].(string)
	if code, e, _ := do(t, h, "POST", "/post", `{"post":"hi","platforms":["reddit"],"idempotencyKey":"k"}`); code != 400 || e["status"] != "error" {
		t.Errorf("a repeated key: %d %v", code, e)
	}
	if code, g, _ := do(t, h, "GET", "/api/post/"+id, ""); code != 200 || g["post"] != "hi" || g["notes"] != "n1" {
		t.Errorf("get: %d %v", code, g)
	}
	if code, _, l := do(t, h, "GET", "/history?count=5", ""); code != 200 || len(l) != 1 {
		t.Errorf("history: %d %v", code, l)
	}
	if code, _, _ := do(t, h, "DELETE", "/post", `{"id":"`+id+`"}`); code != 200 {
		t.Errorf("delete: %d", code)
	}
	if code, _, _ := do(t, h, "DELETE", "/post", `{"id":"`+id+`"}`); code != 400 {
		t.Errorf("second delete: %d", code)
	}
	if _, g, _ := do(t, h, "GET", "/post/"+id, ""); g["status"] != "deleted" {
		t.Errorf("deleted post: %v", g)
	}
	if _, _, l := do(t, h, "GET", "/history", ""); len(l) != 1 {
		t.Errorf("a deleted post stays in the history: %v", l)
	}
	// A repeated key is refused whatever became of the first post.
	if code, _, _ := do(t, h, "POST", "/post", `{"post":"hi","platforms":["reddit"],"idempotencyKey":"k"}`); code != 400 {
		t.Errorf("key of a deleted post: %d", code)
	}
}

func TestApprovalAndSchedule(t *testing.T) {
	h := Handler()
	_, p, _ := do(t, h, "POST", "/post", `{"post":"hi","platforms":["reddit"],"requiresApproval":true}`)
	if p["status"] != "awaiting approval" || p["postIds"] != nil {
		t.Fatalf("%v", p)
	}
	id := p["id"].(string)
	do(t, h, "PATCH", "/post", `{"id":"`+id+`","approved":true}`)
	if _, g, _ := do(t, h, "GET", "/post/"+id, ""); g["status"] != "success" || g["postIds"] == nil {
		t.Errorf("approved: %v", g)
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	_, p, _ = do(t, h, "POST", "/post", `{"post":"later","platforms":["reddit"],"scheduleDate":"`+future+`"}`)
	if p["status"] != "scheduled" {
		t.Errorf("%v", p)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	_, p, _ = do(t, h, "POST", "/post", `{"post":"now","platforms":["reddit"],"scheduleDate":"`+past+`"}`)
	if p["status"] != "success" {
		t.Errorf("a past schedule is sent at once: %v", p)
	}
}

func TestValidation(t *testing.T) {
	h := Handler()
	for name, body := range map[string]string{
		"no post":         `{"platforms":["reddit"]}`,
		"no platforms":    `{"post":"x"}`,
		"unknown":         `{"post":"x","platforms":["nope"]}`,
		"reddit video":    `{"post":"x","platforms":["reddit"],"mediaUrls":["https://a.example.com/v.mp4"]}`,
		"reddit bmp":      `{"post":"x","platforms":["reddit"],"mediaUrls":["https://a.example.com/v.bmp"]}`,
		"space in url":    `{"post":"x","platforms":["reddit"],"mediaUrls":["https://a.example.com/a b.png"]}`,
		"blank on reddit": `{"post":"","platforms":["reddit"],"mediaUrls":["https://a.example.com/a.png"]}`,
		"bad date":        `{"post":"x","platforms":["reddit"],"scheduleDate":"tomorrow"}`,
	} {
		if code, e, _ := do(t, h, "POST", "/post", body); code != 400 || e["message"] == nil {
			t.Errorf("%s: %d %v", name, code, e)
		}
	}
	if code, _, _ := do(t, h, "POST", "/post", `{"post":"","platforms":["instagram"],"mediaUrls":["https://a.example.com/a.jpg"]}`); code != 200 {
		t.Errorf("image-only on instagram: %d", code)
	}
	if code, _, _ := do(t, h, "GET", "/post/missing", ""); code != 404 {
		t.Errorf("missing: %d", code)
	}
}
