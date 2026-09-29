package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// fakeTavily stands in for api.tavily.com: /usage and /search, checking the
// bearer key it was given and answering canned bodies. A test never calls
// the real API (there is no key here); this is the only server any test in
// this package talks to.
type fakeTavily struct {
	key string // the key that is accepted; anything else is 401

	usageStatus int
	usageBody   string

	searchStatus int
	searchBody   string
	lastSearch   map[string]any // the last decoded /search request body, for assertions
}

func (f *fakeTavily) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/usage", func(w http.ResponseWriter, r *http.Request) {
		if !f.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"detail":{"error":"Unauthorized: missing or invalid API key."}}`))
			return
		}
		status := f.usageStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		w.Write([]byte(f.usageBody))
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		if !f.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"detail":{"error":"Unauthorized: missing or invalid API key."}}`))
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.lastSearch = body
		status := f.searchStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		w.Write([]byte(f.searchBody))
	})
	return httptest.NewServer(mux)
}

func (f *fakeTavily) authorized(r *http.Request) bool {
	want := f.key
	if want == "" {
		want = "tvly-good-key"
	}
	return r.Header.Get("Authorization") == "Bearer "+want
}

const usageOKBody = `{
  "key": {"usage": 150, "limit": 1000, "search_usage": 100, "extract_usage": 25, "crawl_usage": 15, "map_usage": 7, "research_usage": 3},
  "account": {"current_plan": "Bootstrap", "plan_usage": 500, "plan_limit": 15000, "paygo_usage": 0, "paygo_limit": 0, "search_usage": 350, "extract_usage": 75, "crawl_usage": 50, "map_usage": 15, "research_usage": 10}
}`

const searchOKBody = `{
  "query": "who is Leo Messi?",
  "answer": null,
  "images": [],
  "results": [
    {
      "title": "Lionel Messi Facts | Britannica",
      "url": "https://www.britannica.com/facts/Lionel-Messi",
      "content": "Lionel Messi, an Argentine footballer...",
      "score": 0.81025416,
      "raw_content": null,
      "published_date": "Tue, 11 Mar 2025 17:00:00 GMT",
      "id": "a3f9c2-04"
    },
    {
      "title": "No date result",
      "url": "https://example.com/undated",
      "content": "content without a date",
      "score": 0.5
    }
  ],
  "response_time": 1.2,
  "request_id": "123e4567-e89b-12d3-a456-426614174111"
}`

func newWrapper(t *testing.T, f *fakeTavily) (*Wrapper, func()) {
	t.Helper()
	srv := f.server()
	w := &Wrapper{HTTP: srv.Client(), BaseURL: srv.URL}
	return w, srv.Close
}

func conn(key string) Connection {
	return Connection{Credentials: map[string]string{credentialName: key}}
}

// --- probe ---

func TestProbeSucceedsAndBuildsAManifest(t *testing.T) {
	f := &fakeTavily{key: "tvly-good-key", usageBody: usageOKBody}
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()

	resp := w.Handle(WrapperRequest{
		Contract:   wp.DraftContractVersion,
		Connection: conn("tvly-good-key"),
		Calls:      []VerbCall{{Verb: wp.VerbProbe}},
	})

	if len(resp.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(resp.Results))
	}
	res := resp.Results[0]
	if !res.Succeeded() {
		t.Fatalf("probe did not succeed: %s", res.Why())
	}
	m := res.Manifest
	if m == nil {
		t.Fatal("probe answered no manifest")
	}
	if why := m.CheckAs(wp.DraftContractVersion); why != "" {
		t.Fatalf("manifest fails CapabilityManifest.CheckAs: %s", why)
	}
	if m.Verbs[wp.VerbProbe].Level != wp.VerbSupported {
		t.Error("probe must declare itself supported, since it just answered")
	}
	if m.Verbs[wp.VerbSearch].Level != wp.VerbSupported {
		t.Error("search should be supported")
	}
	if m.Wrapper.Version != Version || m.Wrapper.SpecSource == "" || m.Wrapper.SpecHash == "" {
		t.Errorf("wrapper provenance incomplete: %+v", m.Wrapper)
	}
	if m.Contract != wp.DraftContractVersion {
		t.Errorf("manifest.contract = %q, want %q", m.Contract, wp.DraftContractVersion)
	}
	// Every verb of the contract must be accounted for (probe, search
	// supported; everything else declined with a reason), and nothing
	// claimed that is not actually declined should slip through.
	for _, v := range wp.VerbsOf(wp.DraftContractVersion) {
		s, ok := m.Verbs[v]
		if !ok {
			t.Errorf("manifest does not answer for verb %s", v)
			continue
		}
		switch v {
		case wp.VerbProbe, wp.VerbSearch:
		default:
			if s.Level != wp.VerbDeclined {
				t.Errorf("verb %s should be declined, got %s", v, s.Level)
			}
			if s.Reason == "" {
				t.Errorf("verb %s is declined with no reason", v)
			}
		}
	}
	entitlements, ok := m.Entitlements["key"].(map[string]any)
	if !ok || entitlements["usage"] != float64(150) {
		t.Errorf("entitlements did not carry /usage's key data: %+v", m.Entitlements)
	}
}

func TestProbeFailsOnBadKey(t *testing.T) {
	f := &fakeTavily{key: "tvly-good-key"} // any other key is 401
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()

	resp := w.Handle(WrapperRequest{
		Contract:   wp.DraftContractVersion,
		Connection: conn("tvly-wrong-key"),
		Calls:      []VerbCall{{Verb: wp.VerbProbe}},
	})

	res := resp.Results[0]
	if res.Succeeded() {
		t.Fatal("probe should not succeed with a rejected key")
	}
	if res.Failed == "" {
		t.Errorf("a rejected key is failed, not refused: %+v", res)
	}
}

func TestProbeFailsWithNoCredential(t *testing.T) {
	f := &fakeTavily{}
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()

	resp := w.Handle(WrapperRequest{
		Contract:   wp.DraftContractVersion,
		Connection: Connection{},
		Calls:      []VerbCall{{Verb: wp.VerbProbe}},
	})
	res := resp.Results[0]
	if res.Succeeded() || res.Failed == "" {
		t.Fatalf("probe without a credential should fail, got %+v", res)
	}
}

// --- search ---

func TestSearchMapsResultsToItems(t *testing.T) {
	f := &fakeTavily{key: "tvly-good-key", usageBody: usageOKBody, searchBody: searchOKBody}
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()

	resp := w.Handle(WrapperRequest{
		Contract:   wp.DraftContractVersion,
		Connection: conn("tvly-good-key"),
		Calls: []VerbCall{
			{Verb: wp.VerbSearch, Query: "who is Leo Messi?", Limit: 5},
		},
	})

	res := resp.Results[0]
	if !res.Succeeded() {
		t.Fatalf("search did not succeed: %s", res.Why())
	}
	if len(res.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(res.Items))
	}
	first := res.Items[0]
	if first.URL != "https://www.britannica.com/facts/Lionel-Messi" {
		t.Errorf("url = %q", first.URL)
	}
	if first.Title != "Lionel Messi Facts | Britannica" {
		t.Errorf("title = %q", first.Title)
	}
	if first.Snippet == "" {
		t.Error("snippet (content) should not be empty")
	}
	if first.Source != "www.britannica.com" {
		t.Errorf("source = %q, want the result's host", first.Source)
	}
	if first.PublishedAt == nil || !first.PublishedAt.Equal(time.Date(2025, 3, 11, 17, 0, 0, 0, time.UTC)) {
		t.Errorf("published_at = %v", first.PublishedAt)
	}
	second := res.Items[1]
	if second.PublishedAt != nil {
		t.Errorf("a result with no published_date should have a nil PublishedAt, got %v", second.PublishedAt)
	}

	// The request sent must ask for the cheapest depth, and to raise no
	// unnecessary cost: search_depth basic (1 credit, not advanced's 2).
	if f.lastSearch["search_depth"] != "basic" {
		t.Errorf("search_depth = %v, want basic (spec/tavily/api-credits.md: cheapest option)", f.lastSearch["search_depth"])
	}
	if f.lastSearch["max_results"] != float64(5) {
		t.Errorf("max_results = %v, want 5 (the call's limit)", f.lastSearch["max_results"])
	}
}

func TestSearchOmitsMaxResultsWhenLimitIsZero(t *testing.T) {
	f := &fakeTavily{key: "tvly-good-key", searchBody: searchOKBody}
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()

	resp := w.Handle(WrapperRequest{
		Contract:   wp.DraftContractVersion,
		Connection: conn("tvly-good-key"),
		Calls:      []VerbCall{{Verb: wp.VerbSearch, Query: "anything"}},
	})
	if !resp.Results[0].Succeeded() {
		t.Fatalf("search did not succeed: %s", resp.Results[0].Why())
	}
	if _, present := f.lastSearch["max_results"]; present {
		t.Errorf("max_results should be omitted when Limit is 0, so Tavily's own default applies")
	}
}

func TestSearchRefusesANegativeLimit(t *testing.T) {
	f := &fakeTavily{key: "tvly-good-key"}
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()

	resp := w.Handle(WrapperRequest{
		Contract:   wp.DraftContractVersion,
		Connection: conn("tvly-good-key"),
		Calls:      []VerbCall{{Verb: wp.VerbSearch, Query: "x", Limit: -1}},
	})
	res := resp.Results[0]
	if res.Refused == "" {
		t.Fatalf("a negative limit should be refused, not failed or ignored: %+v", res)
	}
}

func TestSearchRefusesALimitAboveTwenty(t *testing.T) {
	f := &fakeTavily{key: "tvly-good-key"}
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()

	resp := w.Handle(WrapperRequest{
		Contract:   wp.DraftContractVersion,
		Connection: conn("tvly-good-key"),
		Calls:      []VerbCall{{Verb: wp.VerbSearch, Query: "x", Limit: 21}},
	})
	res := resp.Results[0]
	if res.Refused == "" {
		t.Fatalf("a limit over 20 should be refused (Tavily's own ceiling), not silently capped: %+v", res)
	}
}

func TestSearchRefusesAnEmptyQuery(t *testing.T) {
	f := &fakeTavily{key: "tvly-good-key"}
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()

	resp := w.Handle(WrapperRequest{
		Contract:   wp.DraftContractVersion,
		Connection: conn("tvly-good-key"),
		Calls:      []VerbCall{{Verb: wp.VerbSearch, Query: "   "}},
	})
	res := resp.Results[0]
	if res.Refused == "" {
		t.Fatalf("an empty query should be refused, not sent to Tavily: %+v", res)
	}
}

func TestSearchRefusesAFilter(t *testing.T) {
	f := &fakeTavily{key: "tvly-good-key"}
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()

	resp := w.Handle(WrapperRequest{
		Contract:   wp.DraftContractVersion,
		Connection: conn("tvly-good-key"),
		Calls: []VerbCall{{Verb: wp.VerbSearch, Query: "x",
			Filter: &wp.Filter{Dimension: "topic", Value: "news"}}},
	})
	res := resp.Results[0]
	if res.Refused == "" {
		t.Fatalf("a filter should be refused: this wrapper declares no filter_dimensions: %+v", res)
	}
}

func TestSearchWithAWindowFiltersByPublishedDate(t *testing.T) {
	f := &fakeTavily{key: "tvly-good-key", searchBody: searchOKBody}
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()

	start := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	resp := w.Handle(WrapperRequest{
		Contract:   wp.DraftContractVersion,
		Connection: conn("tvly-good-key"),
		Calls: []VerbCall{{Verb: wp.VerbSearch, Query: "web analytics",
			Window: &wp.Window{Start: start, End: end}}},
	})
	if !resp.Results[0].Succeeded() {
		t.Fatalf("search did not succeed: %s", resp.Results[0].Why())
	}
	if f.lastSearch["start_date"] != "2026-08-30" {
		t.Errorf("start_date = %v, want 2026-08-30", f.lastSearch["start_date"])
	}
	if f.lastSearch["end_date"] != "2026-09-28" {
		t.Errorf("end_date = %v, want 2026-09-28", f.lastSearch["end_date"])
	}
	if f.lastSearch["filter_by_published_date"] != true {
		t.Errorf("filter_by_published_date = %v, want true (else undated results stay in, "+
			"which is not \"only what the window holds\")", f.lastSearch["filter_by_published_date"])
	}
}

func TestSearchWithNoWindowSendsNoDateFields(t *testing.T) {
	f := &fakeTavily{key: "tvly-good-key", searchBody: searchOKBody}
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()

	resp := w.Handle(WrapperRequest{
		Contract:   wp.DraftContractVersion,
		Connection: conn("tvly-good-key"),
		Calls:      []VerbCall{{Verb: wp.VerbSearch, Query: "anything"}},
	})
	if !resp.Results[0].Succeeded() {
		t.Fatalf("search did not succeed: %s", resp.Results[0].Why())
	}
	for _, field := range []string{"start_date", "end_date", "filter_by_published_date"} {
		if _, present := f.lastSearch[field]; present {
			t.Errorf("%s should be omitted with no window, got %v", field, f.lastSearch[field])
		}
	}
}

func TestSearchFailsOnServerError(t *testing.T) {
	f := &fakeTavily{key: "tvly-good-key", searchStatus: http.StatusInternalServerError,
		searchBody: `{"detail":{"error":"Internal Server Error"}}`}
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()

	resp := w.Handle(WrapperRequest{
		Contract:   wp.DraftContractVersion,
		Connection: conn("tvly-good-key"),
		Calls:      []VerbCall{{Verb: wp.VerbSearch, Query: "x"}},
	})
	res := resp.Results[0]
	if res.Failed == "" {
		t.Fatalf("a 500 from Tavily should be failed: %+v", res)
	}
}

// --- every other verb is declined, and the run stops there ---

func TestEveryOtherVerbIsDeclinedAndStops(t *testing.T) {
	f := &fakeTavily{key: "tvly-good-key"}
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()

	for _, v := range wp.VerbsOf(wp.DraftContractVersion) {
		if v == wp.VerbProbe || v == wp.VerbSearch {
			continue
		}
		t.Run(string(v), func(t *testing.T) {
			resp := w.Handle(WrapperRequest{
				Contract:   wp.DraftContractVersion,
				Connection: conn("tvly-good-key"),
				Calls: []VerbCall{
					{Verb: v},
					{Verb: wp.VerbProbe}, // must never be reached
				},
			})
			if len(resp.Results) != 1 {
				t.Fatalf("got %d results, want 1 (the run must stop at the refusal)", len(resp.Results))
			}
			res := resp.Results[0]
			if res.Refused == "" {
				t.Fatalf("verb %s should be refused, got %+v", v, res)
			}
			if res.Verb != v {
				t.Errorf("result names verb %s, want %s", res.Verb, v)
			}
		})
	}
}

// --- the wire contract itself ---

func TestWrongContractFailsTheFirstCallAndStops(t *testing.T) {
	f := &fakeTavily{key: "tvly-good-key"}
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()

	resp := w.Handle(WrapperRequest{
		Contract:   "1",
		Connection: conn("tvly-good-key"),
		Calls:      []VerbCall{{Verb: wp.VerbProbe}, {Verb: wp.VerbSearch}},
	})
	if len(resp.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(resp.Results))
	}
	if resp.Results[0].Failed == "" {
		t.Fatalf("a request for a contract this wrapper does not speak should fail, got %+v", resp.Results[0])
	}
}

func TestResponseParsesUnderParseWrapperResponse(t *testing.T) {
	f := &fakeTavily{key: "tvly-good-key", usageBody: usageOKBody, searchBody: searchOKBody}
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()

	calls := []VerbCall{
		{Verb: wp.VerbProbe},
		{Verb: wp.VerbSearch, Query: "who is Leo Messi?"},
	}
	resp := w.Handle(WrapperRequest{Contract: wp.DraftContractVersion, Connection: conn("tvly-good-key"), Calls: calls})

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshalling response: %v", err)
	}
	if _, err := wp.ParseWrapperResponse(raw, len(calls)); err != nil {
		t.Fatalf("ParseWrapperResponse rejected the wrapper's own output: %v", err)
	}
}

// --- wrapper.json agrees with what the wrapper answers ---

func TestWrapperJSONAgreesWithTheWrapper(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(".", "wrapper.json"))
	if err != nil {
		t.Fatalf("reading wrapper.json: %v", err)
	}
	var d wp.Description
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("wrapper.json is not readable JSON: %v", err)
	}
	if why := d.Check(); why != "" {
		t.Fatalf("wrapper.json: %s", why)
	}
	if d.Tool.Slug != "tavily" {
		t.Errorf("tool.slug = %q, want %q (the directory's name)", d.Tool.Slug, "tavily")
	}
	if d.Version != Version {
		t.Errorf("wrapper.json version = %q, does not match the wrapper's own Version %q", d.Version, Version)
	}
	if d.Contract != wp.DraftContractVersion {
		t.Errorf("wrapper.json contract = %q, want %q", d.Contract, wp.DraftContractVersion)
	}
	wantCmd := []string{"/tavily"}
	if len(d.Command) != len(wantCmd) {
		t.Fatalf("command = %v, want %v", d.Command, wantCmd)
	}
	for i := range wantCmd {
		if d.Command[i] != wantCmd[i] {
			t.Fatalf("command = %v, want %v", d.Command, wantCmd)
		}
	}
	if len(d.Connection.Credentials) != 1 || d.Connection.Credentials[0].Name != credentialName {
		t.Fatalf("connection.credentials = %+v, want just %s", d.Connection.Credentials, credentialName)
	}

	// The probe's manifest must not decline anything wrapper.json claims,
	// and must not claim more than a real probe answers (GUIDE.md:
	// "claims are shortlisting evidence... the wrapper's own tests check
	// its probe does not decline anything this file claims").
	f := &fakeTavily{key: "tvly-good-key", usageBody: usageOKBody}
	w, closeSrv := newWrapper(t, f)
	defer closeSrv()
	resp := w.Handle(WrapperRequest{
		Contract:   wp.DraftContractVersion,
		Connection: conn("tvly-good-key"),
		Calls:      []VerbCall{{Verb: wp.VerbProbe}},
	})
	manifest := resp.Results[0].Manifest
	if manifest == nil {
		t.Fatal("probe answered no manifest")
	}
	for verb, level := range d.Claims {
		got, ok := manifest.Verbs[wp.Verb(verb)]
		if !ok {
			t.Errorf("wrapper.json claims %s, the manifest does not answer for it", verb)
			continue
		}
		if string(got.Level) != level {
			t.Errorf("wrapper.json claims %s is %q, the manifest says %q", verb, level, got.Level)
		}
	}
	for v := range manifest.Verbs {
		if _, claimed := d.Claims[string(v)]; !claimed {
			t.Errorf("the manifest answers for %s, which wrapper.json does not claim at all", v)
		}
	}
}

// TestDeclineReasonsCoverExactlyTheDeclinedVerbs guards against the map in
// wrapper.go silently falling out of step with the contract's verb list (a
// new verb in a future draft, or a typo'd wp.Verb constant).
func TestDeclineReasonsCoverExactlyTheDeclinedVerbs(t *testing.T) {
	want := map[wp.Verb]bool{}
	for _, v := range wp.VerbsOf(wp.DraftContractVersion) {
		if v == wp.VerbProbe || v == wp.VerbSearch {
			continue
		}
		want[v] = true
	}
	for v := range declineReasons {
		if !want[v] {
			t.Errorf("declineReasons names %s, which is not a declined verb of the contract", v)
		}
		delete(want, v)
	}
	for v := range want {
		t.Errorf("declineReasons is missing verb %s", v)
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://www.britannica.com/facts/Lionel-Messi": "www.britannica.com",
		"http://example.com":                            "example.com",
		"not a url at all `` \x00":                      "",
	}
	for in, want := range cases {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestApiErrorMessage(t *testing.T) {
	if got := apiErrorMessage([]byte(`{"detail":{"error":"boom"}}`)); got != "boom" {
		t.Errorf("apiErrorMessage = %q, want boom", got)
	}
	if got := apiErrorMessage([]byte("not json")); got != "not json" {
		t.Errorf("apiErrorMessage = %q, want the raw body", got)
	}
	if got := apiErrorMessage(nil); got != "(empty body)" {
		t.Errorf("apiErrorMessage(nil) = %q", got)
	}
}

func init() {
	// Fail fast and clearly if the fixtures above stop matching the shape
	// spec/tavily/usage.md and search.md describe, rather than surfacing as
	// a confusing failure somewhere else.
	var u struct {
		Key     map[string]any `json:"key"`
		Account map[string]any `json:"account"`
	}
	if err := json.Unmarshal([]byte(usageOKBody), &u); err != nil {
		panic(fmt.Sprintf("usageOKBody fixture is not valid JSON: %v", err))
	}
}
