package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// searchRequest is the subset of spec/tavily/search.md's request body this
// wrapper sends. search_depth is pinned to "basic", the cheapest option (1
// API credit; "advanced" is 2, per spec/tavily/api-credits.md), since the
// contract gives Mendel no way to ask for more relevance at more cost.
//
// StartDate, EndDate and FilterByPublishedDate are set only when the call
// carries a Window (wrapperprotocol.VerbCall.Window): the contract's window
// is half-open on instants, but Tavily's own start_date/end_date are dates,
// so a window is the closest a search can come to it. FilterByPublishedDate
// must be true for that to mean anything -- left false (Tavily's default),
// start_date/end_date only narrow the results Tavily happens to date, and
// leave every undated result in, which is not "only what the window holds".
type searchRequest struct {
	Query                 string `json:"query"`
	SearchDepth           string `json:"search_depth"`
	MaxResults            *int   `json:"max_results,omitempty"`
	IncludePublishedDate  bool   `json:"include_published_date"`
	StartDate             string `json:"start_date,omitempty"`
	EndDate               string `json:"end_date,omitempty"`
	FilterByPublishedDate bool   `json:"filter_by_published_date,omitempty"`
}

// searchResponse is the subset of spec/tavily/search.md's 200 response this
// wrapper reads.
type searchResponse struct {
	Results []struct {
		Title         string `json:"title"`
		URL           string `json:"url"`
		Content       string `json:"content"`
		PublishedDate string `json:"published_date"`
	} `json:"results"`
}

// dateLayout is the YYYY-MM-DD spec/tavily/search.md's start_date and
// end_date require.
const dateLayout = "2006-01-02"

// search answers one search call: query and limit are the contract's usual
// arguments for it (wrapperprotocol.VerbCall's Query and Limit); window
// narrows it to Tavily's start_date/end_date, and a filter is refused since
// this wrapper declares no filter dimension for search to honour.
func (w *Wrapper) search(conn Connection, call VerbCall) VerbResult {
	key, ok := conn.Credentials[credentialName]
	if !ok || isBlank(key) {
		return VerbResult{Failed: fmt.Sprintf("the connection has no %s credential", credentialName)}
	}
	if isBlank(call.Query) {
		return VerbResult{Refused: "search needs a query; none was given"}
	}
	if call.Filter != nil {
		return VerbResult{Refused: fmt.Sprintf(
			"tavily's search declares no filter dimensions (probe's manifest.filter_dimensions is empty); %q is not one",
			call.Filter.Dimension)}
	}
	if call.Limit < 0 {
		return VerbResult{Refused: "limit must not be negative"}
	}
	if call.Limit > maxResults {
		return VerbResult{Refused: fmt.Sprintf(
			"Tavily returns at most %d results per search (spec/tavily/search.md); %d was asked for",
			maxResults, call.Limit)}
	}

	body := searchRequest{
		Query:                call.Query,
		SearchDepth:          "basic",
		IncludePublishedDate: true,
	}
	if call.Limit > 0 {
		limit := call.Limit
		body.MaxResults = &limit
	}
	if call.Window != nil {
		// Tavily's start_date/end_date are dates (spec/tavily/search.md),
		// not instants, so the window's bound is truncated to one. Setting
		// filter_by_published_date is what actually removes results outside
		// [start, end) -- and results with no detectable date, since nothing
		// says those are in the window either.
		body.StartDate = call.Window.Start.Format(dateLayout)
		body.EndDate = call.Window.End.Format(dateLayout)
		body.FilterByPublishedDate = true
	}

	status, raw, err := w.call(http.MethodPost, "/search", body, key)
	if err != nil {
		return VerbResult{Failed: fmt.Sprintf("calling Tavily's search endpoint: %v", err)}
	}
	if status != http.StatusOK {
		return VerbResult{Failed: fmt.Sprintf("Tavily's search answered %d %s: %s",
			status, http.StatusText(status), apiErrorMessage(raw))}
	}

	var parsed searchResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return VerbResult{Failed: fmt.Sprintf("Tavily's search response is not readable JSON: %v", err)}
	}

	items := make([]wp.Item, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		item := wp.Item{
			URL:     r.URL,
			Title:   r.Title,
			Snippet: r.Content,
			Source:  hostOf(r.URL),
		}
		if r.PublishedDate != "" {
			if t, err := time.Parse(time.RFC1123, r.PublishedDate); err == nil {
				item.PublishedAt = &t
			}
		}
		items = append(items, item)
	}

	return VerbResult{Items: items}
}

// hostOf is a result's URL host, Item's Source field (wrapperprotocol.Item):
// Tavily's own response names no publisher field, so this is the closest a
// wrapper can name "where a result is from" without another call.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}
