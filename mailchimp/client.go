package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

// client talks to Mailchimp's Marketing API v3
// (https://mailchimp.com/developer/marketing/api/, fetched 2026-09-30):
// https://<dc>.api.mailchimp.com/3.0/, the data center named by the API
// key's own "-dc" suffix (Fundamentals: "if your API key is
// 0123456789abcdef0123456789abcde-us6, then the data center subdomain is
// us6"), authenticated with the key over HTTP Basic auth, "any
// string:TOKEN" (Fundamentals: "Authenticate with an API key or OAuth 2
// token").
//
// When the run's connection names an endpoint, every request goes there
// instead, keeping its path, whether that is a self-hosted proxy or a
// stand-in for Mailchimp on the loopback: the data center is never derived
// in that case, since the endpoint already says where to send the request.
type client struct {
	base   string // no trailing slash
	apiKey string
	http   *http.Client
}

func newClient(conn wp.Connection) (*client, error) {
	apiKey := conn.Credentials["MAILCHIMP_API_KEY"]
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("no MAILCHIMP_API_KEY credential was given")
	}
	base := strings.TrimRight(conn.Endpoint, "/")
	if base == "" {
		dc, err := datacenter(apiKey)
		if err != nil {
			return nil, err
		}
		base = "https://" + dc + ".api.mailchimp.com/3.0"
	}
	return &client{base: base, apiKey: apiKey, http: &http.Client{Timeout: 15 * time.Second}}, nil
}

// datacenter reads the data center subdomain from the tail of a Mailchimp
// API key, as Fundamentals describes it.
func datacenter(apiKey string) (string, error) {
	i := strings.LastIndex(apiKey, "-")
	if i < 0 || i == len(apiKey)-1 {
		return "", fmt.Errorf("the API key is not in Mailchimp's key-dc form (for example 0123...-us6), " +
			"so its data center cannot be found; set the endpoint to use a self-hosted or test address instead")
	}
	return apiKey[i+1:], nil
}

// apiError is Mailchimp's standard error body
// (https://mailchimp.com/developer/marketing/docs/errors/): a type, a
// title, the status again, and a detail sentence.
type apiError struct {
	Type   string `json:"type,omitempty"`
	Title  string `json:"title,omitempty"`
	Status int    `json:"status,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func (e *apiError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s", e.Title, e.Detail)
	}
	if e.Title != "" {
		return e.Title
	}
	return fmt.Sprintf("Mailchimp answered status %d", e.Status)
}

// do sends one request and decodes a JSON response into out (nil to
// discard the body). A response in the 4xx/5xx range comes back as an
// *apiError, never silently as success.
func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.SetBasicAuth("mendel", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		ae := &apiError{Status: resp.StatusCode}
		if len(data) > 0 {
			_ = json.Unmarshal(data, ae)
		}
		ae.Status = resp.StatusCode
		return ae
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("reading Mailchimp's answer: %w", err)
		}
	}
	return nil
}

// statusOf reports the HTTP status an error carries, or 0 for one that is
// not an *apiError (a network failure, a body that would not parse).
func statusOf(err error) int {
	if ae, ok := err.(*apiError); ok {
		return ae.Status
	}
	return 0
}
