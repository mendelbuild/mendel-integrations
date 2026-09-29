package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// call sends one Tavily API request, bearer-authenticated, and returns its
// status and raw body. It is the only place this wrapper reaches the
// network, so wrapper_test.go's fake server can stand in for all of Tavily
// by pointing Wrapper.BaseURL at itself.
func (w *Wrapper) call(method, path string, body any, key string) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, w.baseURL()+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := w.client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
}

// apiErrorMessage reads spec/tavily's ApiError shape ({"detail": {"error":
// "..."}}), falling back to the raw body (trimmed) when a response does not
// match it, e.g. a proxy's own error page.
func apiErrorMessage(raw []byte) string {
	var parsed struct {
		Detail struct {
			Error string `json:"error"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil && parsed.Detail.Error != "" {
		return parsed.Detail.Error
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 500 {
		s = s[:500] + "..."
	}
	if s == "" {
		return "(empty body)"
	}
	return s
}
