package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

// defaultBase is Resend's own API host, used only when the connection gives
// no endpoint.
const defaultBase = "https://api.resend.com"

// credentialName is the connection credential that holds the API key.
const credentialName = "RESEND_API_KEY"

type client struct {
	base string
	key  string
	hc   *http.Client
}

// newClient points every request at the connection's endpoint when one is
// given, keeping its path.
func newClient(conn wp.Connection, hc *http.Client) *client {
	base := defaultBase
	if e := strings.TrimSpace(conn.Endpoint); e != "" {
		base = strings.TrimRight(e, "/")
	}
	return &client{base: base, key: conn.Credentials[credentialName], hc: hc}
}

// apiError is a non-2xx answer from Resend, in its documented error shape.
type apiError struct {
	Status  int
	Name    string
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("Resend answered %d (%s): %s", e.Status, e.Name, e.Message)
}

// do makes one request and decodes a 2xx answer into out (when not nil). A
// non-2xx answer is an *apiError; anything else is a transport failure.
func (c *client) do(method, path string, query url.Values, body any, header map[string]string, out any) error {
	u := c.base + path
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
		return fmt.Errorf("building the request to Resend: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		// The URL carries no secret, but the text is scrubbed of the key anyway.
		return fmt.Errorf("reaching Resend: %s", wp.Scrub(err.Error(), []string{c.key}))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("reading Resend's answer: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := &apiError{Status: resp.StatusCode, Name: "error", Message: strings.TrimSpace(string(data))}
		var doc struct {
			Name    string `json:"name"`
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &doc) == nil && (doc.Name != "" || doc.Message != "") {
			e.Name, e.Message = doc.Name, doc.Message
		}
		if len(e.Message) > 300 {
			e.Message = e.Message[:300]
		}
		e.Message = wp.Scrub(e.Message, []string{c.key})
		return e
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("Resend's answer is not readable: %v", err)
		}
	}
	return nil
}

// emailDoc is Retrieve Sent Email's answer, and the part of a List Sent
// Emails item the wrapper reads.
type emailDoc struct {
	ID          string   `json:"id"`
	To          []string `json:"to"`
	From        string   `json:"from"`
	Subject     string   `json:"subject"`
	HTML        *string  `json:"html"`
	Text        *string  `json:"text"`
	LastEvent   string   `json:"last_event"`
	ScheduledAt *string  `json:"scheduled_at"`
	Tags        []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"tags"`
}

func (c *client) retrieve(id string) (*emailDoc, error) {
	var e emailDoc
	if err := c.do(http.MethodGet, "/emails/"+url.PathEscape(id), nil, nil, nil, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

type emailList struct {
	HasMore bool       `json:"has_more"`
	Data    []emailDoc `json:"data"`
}

func (c *client) list(limit int, after string) (*emailList, error) {
	q := url.Values{"limit": {fmt.Sprint(limit)}}
	if after != "" {
		q.Set("after", after)
	}
	var l emailList
	if err := c.do(http.MethodGet, "/emails", q, nil, nil, &l); err != nil {
		return nil, err
	}
	return &l, nil
}
