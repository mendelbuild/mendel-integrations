package fakeapi

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func post(t *testing.T, h http.Handler, path string, auth bool, fields map[string]string) (int, map[string]string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, path, &b)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if auth {
		req.SetBasicAuth("api", "anything")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestSendKeepsWhatItAccepts(t *testing.T) {
	f := New()
	h := f.Handler()
	code, out := post(t, h, "/v3/mg.example.com/messages", true,
		map[string]string{"from": "a@mg.example.com", "to": "b@example.org", "subject": "s", "text": "t"})
	if code != 200 || out["message"] != "Queued. Thank you." || !strings.HasSuffix(out["id"], "@mg.example.com>") {
		t.Fatalf("%d %v", code, out)
	}
	if len(f.Sent()) != 1 {
		t.Fatal("not kept")
	}
	post(t, h, "/v3/mg.example.com/messages", true,
		map[string]string{"from": "a@mg.example.com", "to": "b@example.org", "subject": "s", "text": "t", "o:testmode": "yes"})
	if len(f.Sent()) != 1 {
		t.Fatal("a test-mode message was kept as delivered")
	}
}

func TestSendRefusals(t *testing.T) {
	h := Handler()
	ok := map[string]string{"from": "a@mg.example.com", "to": "b@example.org", "subject": "s", "text": "t"}
	without := func(k string) map[string]string {
		m := map[string]string{}
		for a, b := range ok {
			if a != k {
				m[a] = b
			}
		}
		return m
	}
	for _, k := range []string{"from", "to", "subject", "text"} {
		if code, _ := post(t, h, "/v3/d.example/messages", true, without(k)); code != 400 {
			t.Errorf("without %s: %d", k, code)
		}
	}
	if code, _ := post(t, h, "/v3/d.example/messages", false, ok); code != 401 {
		t.Errorf("no credential: %d", code)
	}
	if code, _ := post(t, h, "/v3/d.example/other", true, ok); code != 404 {
		t.Errorf("other path: %d", code)
	}
}
