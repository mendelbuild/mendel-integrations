package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultBase is Ayrshare's own host. It is not in the pages under spec/; it is
// the host Ayrshare's API is served from (see README.md). A connection's
// endpoint replaces it, path and all.
const defaultBase = "https://api.ayrshare.com/api"

// historyCount is the most records the history endpoint returns in one call
// (changelog, March 2022: "retrieve up to 500 records").
const historyCount = 500

type client struct {
	base string
	key  string
	hc   *http.Client
}

func newClient(endpoint, key string, hc *http.Client) *client {
	base := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if base == "" {
		base = defaultBase
	}
	if hc == nil {
		hc = &http.Client{Timeout: 45 * time.Second}
	}
	return &client{base: base, key: key, hc: hc}
}

// apiError is an answer from Ayrshare that was not a success.
type apiError struct {
	HTTP    int
	Code    int
	Message string
}

func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Ayrshare answered HTTP %d", e.HTTP)
	}
	return fmt.Sprintf("Ayrshare answered HTTP %d: %s", e.HTTP, e.Message)
}

func (e *apiError) notFound() bool {
	return e.HTTP == http.StatusNotFound || strings.Contains(strings.ToLower(e.Message), "not found")
}

func (e *apiError) duplicateKey() bool {
	m := strings.ToLower(e.Message)
	return e.HTTP == http.StatusBadRequest && (strings.Contains(m, "idempotency") || strings.Contains(m, "duplicate"))
}

// do sends one request and decodes the JSON answer into out (when not nil).
func (c *client) do(method, p string, query url.Values, body any, out any) error {
	u := c.base + p
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		ae := &apiError{HTTP: resp.StatusCode}
		var e struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &e) == nil {
			ae.Code, ae.Message = e.Code, e.Message
		}
		return ae
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("Ayrshare's answer is not JSON of the shape expected: %v", err)
	}
	return nil
}

// apiPost is a post as Ayrshare answers it.
type apiPost struct {
	ID           string   `json:"id"`
	RefID        string   `json:"refId"`
	Status       string   `json:"status"`
	Post         string   `json:"post"`
	Platforms    []string `json:"platforms"`
	MediaURLs    []string `json:"mediaUrls"`
	Notes        string   `json:"notes"`
	ScheduleDate string   `json:"scheduleDate"`
	PostIDs      []struct {
		Status   string `json:"status"`
		Platform string `json:"platform"`
		ID       string `json:"id"`
		PostURL  string `json:"postUrl"`
	} `json:"postIds"`
}

func (p apiPost) url() string {
	for _, id := range p.PostIDs {
		if id.PostURL != "" {
			return id.PostURL
		}
	}
	return ""
}

var errNoPost = errors.New("Ayrshare holds no post with that id")

// history reads the most recent posts, deleted ones included. The answer is a
// list, or an object holding one.
func (c *client) history(count int) ([]apiPost, error) {
	var raw json.RawMessage
	if err := c.do(http.MethodGet, "/history", url.Values{"count": {fmt.Sprint(count)}}, nil, &raw); err != nil {
		return nil, err
	}
	trim := bytes.TrimSpace(raw)
	if len(trim) > 0 && trim[0] == '[' {
		var list []apiPost
		return list, json.Unmarshal(trim, &list)
	}
	var wrapped struct {
		History []apiPost `json:"history"`
		Posts   []apiPost `json:"posts"`
	}
	if err := json.Unmarshal(trim, &wrapped); err != nil {
		return nil, fmt.Errorf("Ayrshare's history is not a list of posts: %v", err)
	}
	if wrapped.History != nil {
		return wrapped.History, nil
	}
	return wrapped.Posts, nil
}

// getPost reads one post. A post deleted at Ayrshare stays in its history, so
// where the post endpoint does not know an id the history is asked too.
func (c *client) getPost(id string) (*apiPost, error) {
	var p apiPost
	err := c.do(http.MethodGet, "/post/"+url.PathEscape(id), nil, nil, &p)
	if err == nil {
		if p.ID == "" {
			p.ID = id
		}
		return &p, nil
	}
	var ae *apiError
	if !errors.As(err, &ae) || !ae.notFound() {
		return nil, err
	}
	list, herr := c.history(historyCount)
	if herr != nil {
		return nil, herr
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, errNoPost
}
