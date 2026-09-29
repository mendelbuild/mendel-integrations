package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// specSource is the reference the wrapper was written against.
const specSource = "https://docs.tavily.com/documentation/api-reference/endpoint/search"

// credentialName is the name the connection carries the API key under.
const credentialName = "TAVILY_API_KEY"

// defaultEndpoint is Tavily's API.
const defaultEndpoint = "https://api.tavily.com"

// maxResults is the most one search returns (the API's own maximum).
const maxResults = 20

// readme is the record of the cited spec; its hash is the wrapper's spec hash.
//
//go:embed README.md
var readme []byte

func specHash() string {
	sum := sha256.Sum256(readme)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type api struct {
	base   string
	key    string
	config map[string]any
	client *http.Client
}

func newAPI(c wp.Connection, client *http.Client) *api {
	base := strings.TrimRight(c.Endpoint, "/")
	if base == "" {
		base = defaultEndpoint
	}
	return &api{base: base, key: c.Credentials[credentialName], config: c.Config, client: client}
}

// apiError is a non-2xx answer: its status and Tavily's own sentence.
type apiError struct {
	status int
	says   string
}

func (e *apiError) Error() string {
	if e.status == 0 { // nothing was sent
		return e.says
	}
	return fmt.Sprintf("Tavily answered %d: %s", e.status, e.says)
}

func (a *api) call(ctx context.Context, method, path string, body any, out any) error {
	if a.key == "" {
		return &apiError{says: "the connection holds no API key"}
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("reaching %s: %v", a.base, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Detail json.RawMessage `json:"detail"`
		}
		_ = json.Unmarshal(raw, &e)
		var d struct {
			Error string `json:"error"`
		}
		says := http.StatusText(resp.StatusCode)
		if json.Unmarshal(e.Detail, &d) == nil && d.Error != "" {
			says = d.Error
		} else if len(e.Detail) > 0 {
			says = string(e.Detail)
		}
		return &apiError{status: resp.StatusCode, says: says}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("Tavily's answer to %s is not readable: %v", path, err)
		}
	}
	return nil
}

func verbs() map[wp.Verb]wp.VerbSupport {
	declined := func(why string) wp.VerbSupport { return wp.VerbSupport{Level: wp.VerbDeclined, Reason: why} }
	nothing := declined("Tavily searches; it holds nothing of the project's to act on.")
	return map[wp.Verb]wp.VerbSupport{
		wp.VerbProbe:         {Level: wp.VerbSupported},
		wp.VerbAuthorize:     declined("Tavily is connected with an API key a person types."),
		wp.VerbDraft:         nothing, wp.VerbPublish: nothing, wp.VerbStatus: nothing, wp.VerbAppendUpdate: nothing,
		wp.VerbRetract: nothing, wp.VerbReadBack: nothing, wp.VerbReadMetrics: nothing, wp.VerbSetCap: nothing,
		wp.VerbListOwned:  nothing,
		wp.VerbReadSeries: declined("Tavily reports no counts over time."),
		wp.VerbReadTotal:  declined("Tavily reports no totals."),
		wp.VerbSearch: {Level: wp.VerbPartial, Caveat: "A window is applied by whole UTC days, against Tavily's " +
			"estimate of when a page was published or last updated, and a result with no date it can detect is dropped " +
			"from a windowed search. Every search spends credits: 1 at basic depth, 2 at advanced (the connection's " +
			"config: search_depth)."},
	}
}

func (a *api) probe(ctx context.Context) wp.VerbResult {
	// The usage endpoint proves the key and costs no credits.
	var u struct {
		Key struct {
			Usage       int  `json:"usage"`
			Limit       *int `json:"limit"`
			SearchUsage int  `json:"search_usage"`
		} `json:"key"`
		Account struct {
			CurrentPlan string `json:"current_plan"`
			PlanUsage   int    `json:"plan_usage"`
			PlanLimit   int    `json:"plan_limit"`
		} `json:"account"`
	}
	if err := a.call(ctx, http.MethodGet, "/usage", nil, &u); err != nil {
		return wp.VerbResult{Verb: wp.VerbProbe, Failed: err.Error()}
	}
	ent := map[string]any{"plan": u.Account.CurrentPlan, "plan_usage": u.Account.PlanUsage,
		"plan_limit": u.Account.PlanLimit, "key_usage": u.Key.Usage,
		"cost": "1 credit per basic search, 2 per advanced", "rate_limit": "100 requests a minute for a development key, 1,000 for production"}
	if u.Key.Limit != nil {
		ent["key_limit"] = *u.Key.Limit
	}
	return wp.VerbResult{Verb: wp.VerbProbe, Manifest: &wp.CapabilityManifest{
		Contract:         wp.ContractVersion,
		Wrapper:          wp.WrapperProvenance{Version: wrapperVersion, SpecSource: specSource, SpecHash: specHash()},
		Verbs:            verbs(),
		Metrics:          map[string]wp.MetricSupport{},
		FilterDimensions: []string{"domain"},
		Venue:            "read_only",
		Idempotency:      "none",
		Entitlements:     ent,
		StoragePolicy: "Tavily's terms (tavily.com/terms, updated 2026-05-04): the customer must review Output before " +
			"relying on it and may not use it alone for decisions with a legal or similarly significant effect on a person, " +
			"nor to build competing models. Results point at third parties' pages, whose content is theirs: keep a result's " +
			"title, URL, date and snippet as a reference to it, and do not republish the page.",
		SearchHorizon: "Not documented by Tavily: the web as its index holds it, with no stated limit on how far back; " +
			"a window filters by Tavily's estimated publish or update date.",
	}}
}

type result struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	Content       string `json:"content"`
	PublishedDate string `json:"published_date"`
}

func (a *api) search(ctx context.Context, c wp.VerbCall) wp.VerbResult {
	v := wp.VerbSearch
	q := strings.TrimSpace(c.Query)
	switch {
	case q == "":
		return wp.VerbResult{Verb: v, Refused: "a search needs a query"}
	case c.Limit < 0 || c.Limit > maxResults:
		return wp.VerbResult{Verb: v, Refused: fmt.Sprintf("Tavily returns 1 to %d results, not %d", maxResults, c.Limit)}
	case c.Filter != nil && c.Filter.Dimension != "domain":
		return wp.VerbResult{Verb: v, Refused: fmt.Sprintf("%s is not a dimension this wrapper filters on; domain is", c.Filter.Dimension)}
	case c.Window != nil && !c.Window.End.After(c.Window.Start):
		return wp.VerbResult{Verb: v, Refused: "the window's end is not after its start"}
	}
	body := map[string]any{"query": q, "search_depth": "basic", "topic": "general", "include_published_date": true}
	if c.Limit > 0 {
		body["max_results"] = c.Limit
	}
	for _, k := range []string{"search_depth", "topic"} {
		if s, ok := a.config[k].(string); ok && s != "" {
			body[k] = s
		}
	}
	if c.Window != nil {
		body["start_date"] = c.Window.Start.UTC().Format("2006-01-02")
		body["end_date"] = c.Window.End.Add(-time.Nanosecond).UTC().Format("2006-01-02")
		body["filter_by_published_date"] = true
	}
	if c.Filter != nil {
		body["include_domains"] = []string{c.Filter.Value}
		body["include_domains_mode"] = "restrict"
	}
	var r struct {
		Results []result `json:"results"`
	}
	err := a.call(ctx, http.MethodPost, "/search", body, &r)
	if e, ok := err.(*apiError); ok && (e.status == http.StatusBadRequest || e.status == http.StatusUnprocessableEntity) {
		return wp.VerbResult{Verb: v, Refused: "Tavily refused the search: " + e.says}
	}
	if err != nil {
		return wp.VerbResult{Verb: v, Failed: err.Error()}
	}
	items := []wp.Item{}
	for _, x := range r.Results {
		it := wp.Item{URL: x.URL, Title: x.Title, Snippet: x.Content}
		if u, err := url.Parse(x.URL); err == nil {
			it.Source = strings.TrimPrefix(u.Hostname(), "www.")
		}
		if t, err := time.Parse(time.RFC1123, x.PublishedDate); err == nil {
			t = t.UTC()
			it.PublishedAt = &t
		}
		items = append(items, it)
	}
	return wp.VerbResult{Verb: v, Items: items}
}
