package conformance

import (
	"context"
	"testing"
	"time"
	"strings"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// searcher is a small search tool on the draft, with switches that each
// break one rule of search.
type searcher struct {
	overLimit, noURL, ignoresWindow, answersEmpty, ignoresFilter bool
}

func (f searcher) manifest() *wp.CapabilityManifest {
	m := &wp.CapabilityManifest{Contract: wp.ContractVersion,
		Wrapper: wp.WrapperProvenance{Version: "1.0", SpecSource: "https://search.example/api", SpecHash: "sha256:x"},
		Verbs:   map[wp.Verb]wp.VerbSupport{}, Metrics: map[string]wp.MetricSupport{}, FilterDimensions: []string{"domain"},
		Venue: "read_only", Idempotency: "none", Entitlements: map[string]any{}, StoragePolicy: "References only.",
		SearchHorizon: "a year"}
	for _, v := range wp.ContractVerbs() {
		m.Verbs[v] = wp.VerbSupport{Level: wp.VerbDeclined, Reason: "not here"}
	}
	m.Verbs[wp.VerbProbe] = wp.VerbSupport{Level: wp.VerbSupported}
	m.Verbs[wp.VerbSearch] = wp.VerbSupport{Level: wp.VerbSupported}
	return m
}

func (f searcher) run(_ context.Context, req wp.WrapperRequest) (wp.WrapperResponse, string, error) {
	var out wp.WrapperResponse
	for _, c := range req.Calls {
		r := wp.VerbResult{Verb: c.Verb}
		m := f.manifest()
		switch {
		case req.Contract != wp.ContractVersion:
			r.Refused = "draft only"
		case m.Verbs[c.Verb].Level == wp.VerbDeclined || m.Verbs[c.Verb].Level == "":
			r.Refused = "not here"
		case c.Verb == wp.VerbProbe:
			r.Manifest = m
		case strings.TrimSpace(c.Query) == "" && !f.answersEmpty:
			r.Refused = "no query"
		case c.Filter != nil && c.Filter.Dimension != "domain" && !f.ignoresFilter:
			r.Refused = "no such dimension"
		default:
			n := c.Limit
			if f.overLimit {
				n++
			}
			for i := 0; i < n; i++ {
				at := now.Add(-time.Duration(i+2) * 24 * time.Hour)
				if f.ignoresWindow && c.Window != nil {
					at = now.Add(-400 * 24 * time.Hour)
				}
				it := wp.Item{URL: "https://news.example/" + string(rune('a'+i)), Title: "Item", PublishedAt: &at}
				if f.noURL {
					it.URL = ""
				}
				r.Items = append(r.Items, it)
			}
		}
		out.Results = append(out.Results, r)
		if !r.Succeeded() {
			break
		}
	}
	return out, "", nil
}

func runSearcher(f searcher) Report {
	d := wp.Description{Tool: wp.Tool{Slug: "search", Name: "Search"}, Version: "1.0", Contract: wp.ContractVersion,
		Image: "s:dev", Command: []string{"/s"}, SpecSource: "https://search.example/api",
		Connection: wp.ConnectionSpec{Credentials: []wp.Field{{Name: "SEARCH_KEY", Label: "Key"}}},
		Claims:     map[string]string{"search": "supported"}}
	return Run(context.Background(), Target{Description: d, Run: f.run,
		Connection: wp.Connection{Credentials: map[string]string{"SEARCH_KEY": "search-key-SECRET"}}}, now)
}

func TestASearcherThatKeepsTheDraftPasses(t *testing.T) {
	r := runSearcher(searcher{})
	for _, c := range r.Checks {
		if c.Outcome != Pass {
			t.Errorf("%s (%s): %s: %s", c.Name, c.Verb, c.Outcome, c.Detail)
		}
	}
}

func TestEverySearchMutantIsCaught(t *testing.T) {
	for name, tc := range map[string]struct {
		f     searcher
		check string
	}{
		"answers past its limit": {searcher{overLimit: true}, "no more than it was asked"},
		"a result with no url":   {searcher{noURL: true}, "no more than it was asked"},
		"ignores the window":     {searcher{ignoresWindow: true}, "only what the window holds"},
		"answers an empty query": {searcher{answersEmpty: true}, "no query is refused"},
		"ignores a filter":       {searcher{ignoresFilter: true}, "dimension not declared"},
	} {
		caught := false
		for _, c := range runSearcher(tc.f).Checks {
			caught = caught || (c.Outcome == Fail && strings.Contains(c.Name, tc.check))
		}
		if !caught {
			t.Errorf("%s: not caught by %q", name, tc.check)
		}
	}
}
