package conformance

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// searchQuery is what the suite searches for: a phrase common enough that
// any web search answers it, recent enough that a month's window holds some.
const searchQuery = "web analytics"

// search checks the search verb: results are articles within the limit
// asked, a windowed search answers only what the window holds, and what the
// contract cannot ask is refused before anything is spent. Two searches in
// all, since each may cost the account money.
func (s *suite) search(ctx context.Context, m *wp.CapabilityManifest) {
	const limit = 3
	c := Check{Name: "a search answers articles, no more than it was asked for", Verb: wp.VerbSearch, Calls: 1}
	res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: wp.VerbSearch, Query: searchQuery, Limit: limit})
	c.Elapsed = elapsed
	switch {
	case err != nil:
		c.Outcome, c.Detail = Fail, err.Error()
	case !res.Succeeded():
		c.Outcome, c.Detail = Fail, res.Why()
	case len(res.Items) > limit:
		c.Outcome, c.Detail = Fail, fmt.Sprintf("%d results for a limit of %d", len(res.Items), limit)
	case len(res.Items) == 0:
		c.Outcome, c.Detail = Warn, fmt.Sprintf("no results for %q, so nothing about them could be judged", searchQuery)
	default:
		c.Outcome, c.Detail = judgeItems(res.Items)
	}
	s.add(c)

	day := s.now.Truncate(24 * time.Hour)
	w := &wp.Window{Start: day.Add(-30 * 24 * time.Hour), End: day}
	c = Check{Name: "a windowed search answers only what the window holds", Verb: wp.VerbSearch, Calls: 1}
	res, elapsed, err = s.one(ctx, wp.VerbCall{Verb: wp.VerbSearch, Query: searchQuery, Limit: limit, Window: w})
	c.Elapsed = elapsed
	switch {
	case err != nil:
		c.Outcome, c.Detail = Fail, err.Error()
	case !res.Succeeded():
		c.Outcome, c.Detail = Fail, res.Why()
	case len(res.Items) == 0:
		c.Outcome, c.Detail = Warn, "no results in the last 30 days, so the window could not be judged"
	default:
		var outside []string
		for _, it := range res.Items {
			switch {
			case it.PublishedAt == nil:
				outside = append(outside, it.URL+" has no date, so nothing says it is in the window")
			case it.PublishedAt.Before(w.Start) || !it.PublishedAt.Before(w.End):
				outside = append(outside, fmt.Sprintf("%s is dated %s", it.URL, it.PublishedAt.Format("2006-01-02")))
			}
		}
		if len(outside) > 0 {
			c.Outcome, c.Detail = Fail, fmt.Sprintf("outside %s: %s", span(w), strings.Join(outside, "; "))
		} else {
			c.Outcome, c.Detail = Pass, fmt.Sprintf("%d results within %s", len(res.Items), span(w))
		}
	}
	s.add(c)

	for _, tc := range []struct {
		name string
		call wp.VerbCall
	}{
		{"a search with no query is refused", wp.VerbCall{Verb: wp.VerbSearch, Query: " "}},
		{"a filter on a dimension not declared is refused",
			wp.VerbCall{Verb: wp.VerbSearch, Query: searchQuery, Filter: &wp.Filter{Dimension: "conformance:not_a_dimension", Value: "x"}}},
	} {
		c := Check{Name: tc.name, Verb: wp.VerbSearch, Calls: 1}
		res, elapsed, err := s.one(ctx, tc.call)
		c.Elapsed = elapsed
		switch {
		case err != nil:
			c.Outcome, c.Detail = Fail, err.Error()
		case res.Refused == "":
			c.Outcome, c.Detail = Fail, "it was not refused"
		default:
			c.Outcome, c.Detail = Pass, res.Refused
		}
		s.add(c)
	}
}

// judgeItems checks each result is an article someone could open.
func judgeItems(items []wp.Item) (Outcome, string) {
	var sources []string
	for i, it := range items {
		u, err := url.Parse(it.URL)
		switch {
		case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
			return Fail, fmt.Sprintf("result %d's url %q is not one a person could open", i, it.URL)
		case strings.TrimSpace(it.Title) == "":
			return Fail, fmt.Sprintf("result %d (%s) has no title", i, it.URL)
		}
		sources = append(sources, u.Hostname())
	}
	return Pass, fmt.Sprintf("%d results: %s", len(items), strings.Join(sources, ", "))
}
