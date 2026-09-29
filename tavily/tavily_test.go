package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

const key = "tvly-FAKE-SECRET-KEY"

// fakeTavily plays the two endpoints the wrapper calls, as the OpenAPI
// document cited in README.md describes them.
type fakeTavily struct {
	last map[string]any
}

func (f *fakeTavily) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	if r.Header.Get("Authorization") != "Bearer "+key {
		reply(401, map[string]any{"detail": map[string]any{"error": "Unauthorized: missing or invalid API key."}})
		return
	}
	switch r.URL.Path {
	case "/usage":
		reply(200, map[string]any{"key": map[string]any{"usage": 3, "limit": nil},
			"account": map[string]any{"current_plan": "Researcher", "plan_usage": 3, "plan_limit": 1000}})
	case "/search":
		json.NewDecoder(r.Body).Decode(&f.last)
		if f.last["query"] == "refuse me" {
			reply(400, map[string]any{"detail": map[string]any{"error": "Query is invalid."}})
			return
		}
		reply(200, map[string]any{"query": f.last["query"], "results": []map[string]any{
			{"title": "Plausible Analytics", "url": "https://www.plausible.io/", "content": "Simple analytics.",
				"score": 0.9, "published_date": "Tue, 11 Mar 2025 17:00:00 GMT"},
			{"title": "Undated", "url": "https://example.org/x", "content": "No date.", "score": 0.5},
		}})
	default:
		reply(404, map[string]any{"detail": map[string]any{"error": "Not found"}})
	}
}

func do(t *testing.T, f *fakeTavily, config map[string]any, calls ...wp.VerbCall) (wp.WrapperResponse, string) {
	t.Helper()
	srv := httptest.NewServer(f)
	defer srv.Close()
	body, _ := json.Marshal(wp.WrapperRequest{Contract: wp.DraftContractVersion, Calls: calls,
		Connection: wp.Connection{Endpoint: srv.URL, Credentials: map[string]string{credentialName: key}, Config: config}})
	var out bytes.Buffer
	if err := run(context.Background(), bytes.NewReader(body), &out, srv.Client()); err != nil {
		t.Fatal(err)
	}
	resp, err := wp.ParseWrapperResponse(out.Bytes(), len(calls))
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return resp, out.String()
}

func TestProbeReadsTheKeysUsageAndSpendsNothing(t *testing.T) {
	f := &fakeTavily{}
	resp, out := do(t, f, nil, wp.VerbCall{Verb: wp.VerbProbe})
	m := resp.Results[0].Manifest
	if why := m.CheckAs(wp.DraftContractVersion); why != "" {
		t.Fatalf("the manifest is refused: %s", why)
	}
	if m.Entitlements["plan"] != "Researcher" || f.last != nil {
		t.Errorf("entitlements %v; searched %v", m.Entitlements, f.last)
	}
	if strings.Contains(out, key) {
		t.Error("the key was printed")
	}
}

func TestASearchIsAskedAsTheContractSaysAndAnsweredAsArticles(t *testing.T) {
	f := &fakeTavily{}
	w := &wp.Window{Start: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)}
	resp, _ := do(t, f, map[string]any{"topic": "news"}, wp.VerbCall{Verb: wp.VerbSearch, Query: " web analytics ",
		Limit: 5, Window: w, Filter: &wp.Filter{Dimension: "domain", Value: "plausible.io"}})
	if f.last["query"] != "web analytics" || f.last["max_results"] != float64(5) || f.last["topic"] != "news" ||
		f.last["search_depth"] != "basic" || f.last["start_date"] != "2025-03-01" || f.last["end_date"] != "2025-03-31" ||
		f.last["filter_by_published_date"] != true || f.last["include_domains_mode"] != "restrict" {
		t.Errorf("what was asked: %v", f.last)
	}
	items := resp.Results[0].Items
	if len(items) != 2 || items[0].Source != "plausible.io" || items[0].PublishedAt == nil ||
		!items[0].PublishedAt.Equal(time.Date(2025, 3, 11, 17, 0, 0, 0, time.UTC)) || items[1].PublishedAt != nil ||
		items[0].Snippet != "Simple analytics." {
		t.Errorf("items: %+v", items)
	}
}

func TestSearchRefusesWhatItCannotAsk(t *testing.T) {
	f := &fakeTavily{}
	for name, c := range map[string]wp.VerbCall{
		"no query":          {Query: "  "},
		"too many results":  {Query: "x", Limit: 21},
		"another dimension": {Query: "x", Filter: &wp.Filter{Dimension: "language", Value: "en"}},
		"a backward window": {Query: "x", Window: &wp.Window{Start: time.Now(), End: time.Now().Add(-time.Hour)}},
		"Tavily says no":    {Query: "refuse me"},
	} {
		c.Verb = wp.VerbSearch
		resp, _ := do(t, f, nil, c)
		if resp.Results[0].Refused == "" {
			t.Errorf("%s: %+v", name, resp.Results[0])
		}
	}
}

func TestWrapperJSONAgreesWithWhatTheWrapperAnswers(t *testing.T) {
	raw, err := os.ReadFile("wrapper.json")
	if err != nil {
		t.Fatal(err)
	}
	var d wp.Description
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		t.Fatal(err)
	}
	if why := d.Check(); why != "" {
		t.Fatal(why)
	}
	dockerfile, _ := os.ReadFile("Dockerfile")
	var entrypoint []string
	for _, line := range strings.Split(string(dockerfile), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ENTRYPOINT "); ok {
			json.Unmarshal([]byte(rest), &entrypoint)
		}
	}
	if strings.Join(entrypoint, " ") != strings.Join(d.Command, " ") || len(entrypoint) == 0 {
		t.Errorf("command %q, ENTRYPOINT %q", d.Command, entrypoint)
	}
	if d.Version != wrapperVersion || d.Contract != wp.DraftContractVersion || d.SpecSource != specSource ||
		d.Connection.Credentials[0].Name != credentialName {
		t.Errorf("wrapper.json disagrees with the code: %+v", d)
	}
	for verb, claimed := range d.Claims {
		got, ok := verbs()[wp.Verb(verb)]
		if !ok || (claimed != "declined" && got.Level == wp.VerbDeclined) {
			t.Errorf("wrapper.json claims %s %s; the probe answers %+v", verb, claimed, got)
		}
	}
}
