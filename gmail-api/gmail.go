package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

// defaultEndpoint is Gmail API's own service endpoint (the REST reference:
// "Service endpoint").
const defaultEndpoint = "https://gmail.googleapis.com"

// tokenCredential is the OAuth 2.0 access token the project supplies, sent as
// a bearer token (the drafts guide's cURL sample).
const tokenCredential = "GMAIL_API_ACCESS_TOKEN"

// client is one run's connection to Gmail API.
type client struct {
	base  string // the endpoint, without a trailing slash; its path is kept
	user  string // the userId of every path: the account, or "me"
	from  string // the account's address, when the account is named by one
	token string
	http  *http.Client
}

func newClient(c wp.Connection) *client {
	base := strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	if base == "" {
		base = defaultEndpoint
	}
	cl := &client{base: base, user: "me", token: c.Credentials[tokenCredential],
		http: &http.Client{Timeout: 30 * time.Second}}
	if acct := strings.TrimSpace(c.AccountID); acct != "" {
		cl.user = acct
		if strings.Contains(acct, "@") {
			cl.from = acct
		}
	}
	return cl
}

// apiError is an answer other than success from Gmail API.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("Gmail API answered %d: %s", e.Status, e.Message)
}

func (e *apiError) notFound() bool { return e.Status == http.StatusNotFound }

// do makes one request to /gmail/v1/users/{userId}/<path> and decodes the
// JSON answer into out, when there is one.
func (c *client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	u := c.base + "/gmail/v1/users/" + url.PathEscape(c.user) + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// The transport's error names the URL, never a header.
		return fmt.Errorf("could not reach Gmail API: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 40<<20))
	if err != nil {
		return fmt.Errorf("reading Gmail API's answer: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		var msg string
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		} else {
			msg = strings.TrimSpace(string(data))
			if len(msg) > 200 {
				msg = msg[:200] + "..."
			}
		}
		return &apiError{Status: resp.StatusCode, Message: msg}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("Gmail API's answer is not readable as JSON: %v", err)
		}
	}
	return nil
}

// The methods the wrapper calls.

// messageResource is the part of a Message the wrapper reads.
type messageResource struct {
	ID       string   `json:"id"`
	ThreadID string   `json:"threadId"`
	LabelIDs []string `json:"labelIds"`
	Raw      string   `json:"raw"`
}

type draftResource struct {
	ID      string          `json:"id"`
	Message messageResource `json:"message"`
}

// getProfile is the cheapest call (1 quota unit) that shows the credential
// can reach the account.
func (c *client) getProfile(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/profile", nil, nil, nil)
}

// createDraft is users.drafts.create: a draft with the DRAFT label, which no
// one is sent.
func (c *client) createDraft(ctx context.Context, raw []byte) (draftResource, error) {
	var d draftResource
	err := c.do(ctx, http.MethodPost, "/drafts", nil,
		map[string]any{"message": map[string]string{"raw": base64URL(raw)}}, &d)
	return d, err
}

// sendMessage is users.messages.send.
func (c *client) sendMessage(ctx context.Context, raw []byte) (messageResource, error) {
	var m messageResource
	err := c.do(ctx, http.MethodPost, "/messages/send", nil, map[string]string{"raw": base64URL(raw)}, &m)
	return m, err
}

// sendDraft is users.drafts.send, as the draft is: it deletes the draft and
// answers the new message, with a new id and the SENT label.
func (c *client) sendDraft(ctx context.Context, id string) (messageResource, error) {
	var m messageResource
	err := c.do(ctx, http.MethodPost, "/drafts/send", nil, map[string]string{"id": id}, &m)
	return m, err
}

// getDraftRaw is users.drafts.get with format=raw.
func (c *client) getDraftRaw(ctx context.Context, id string) (draftResource, error) {
	var d draftResource
	err := c.do(ctx, http.MethodGet, "/drafts/"+url.PathEscape(id), url.Values{"format": {"raw"}}, nil, &d)
	return d, err
}

// getMessageRaw is users.messages.get with format=raw.
func (c *client) getMessageRaw(ctx context.Context, id string) (messageResource, error) {
	var m messageResource
	err := c.do(ctx, http.MethodGet, "/messages/"+url.PathEscape(id), url.Values{"format": {"raw"}}, nil, &m)
	return m, err
}
