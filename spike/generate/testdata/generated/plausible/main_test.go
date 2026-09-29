package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// fakePlausible stands in for a self-hosted Plausible's /api/v2/query. It
// never reaches the real API: every response is scripted here from the
// shapes spec/plausible/stats-api.md documents.
func fakePlausible(t *testing.T, wantKey string, handler func(q plausibleQuery) (int, any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/query" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantKey {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(plausibleError{Error: "invalid API key"})
			return
		}
		var q plausibleQuery
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Fatalf("fake server: bad request body: %v", err)
		}
		status, body := handler(q)
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body)
	}))
}

func connectionTo(srv *httptest.Server, key, site string) wrapperprotocol.Connection {
	return wrapperprotocol.Connection{
		Credentials: map[string]string{apiKeyCredential: key},
		AccountID:   site,
		Endpoint:    srv.URL,
	}
}

func TestProbeSucceeds(t *testing.T) {
	srv := fakePlausible(t, "secret-key", func(q plausibleQuery) (int, any) {
		if q.SiteID != "example.com" {
			t.Fatalf("probe queried site %q, want example.com", q.SiteID)
		}
		if len(q.Metrics) != 1 || q.Metrics[0] != "visitors" {
			t.Fatalf("probe requested metrics %v, want [visitors]", q.Metrics)
		}
		return http.StatusOK, plausibleResponse{Results: []plausibleResult{{Metrics: []float64{42}}}}
	})
	defer srv.Close()

	req := wrapperprotocol.WrapperRequest{
		Contract:   wrapperprotocol.ContractVersion,
		Connection: connectionTo(srv, "secret-key", "example.com"),
		Calls:      []wrapperprotocol.VerbCall{{Verb: wrapperprotocol.VerbProbe}},
	}
	resp := Handle(req, srv.Client())
	if len(resp.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(resp.Results))
	}
	res := resp.Results[0]
	if !res.Succeeded() {
		t.Fatalf("probe did not succeed: %s", res.Why())
	}
	if res.Manifest == nil {
		t.Fatal("probe succeeded with no manifest")
	}
	if why := res.Manifest.Check(); why != "" {
		t.Fatalf("manifest fails CapabilityManifest.Check: %s", why)
	}
	if !res.Manifest.IsDataSource() {
		t.Fatal("manifest is not a data source (some write verb is not declined)")
	}
	if res.Manifest.Wrapper.Version != wrapperVersion {
		t.Fatalf("manifest version %q, want %q", res.Manifest.Wrapper.Version, wrapperVersion)
	}
}

func TestProbeFailsOnRejectedKey(t *testing.T) {
	srv := fakePlausible(t, "the-real-key", func(q plausibleQuery) (int, any) {
		return http.StatusOK, plausibleResponse{}
	})
	defer srv.Close()

	req := wrapperprotocol.WrapperRequest{
		Contract:   wrapperprotocol.ContractVersion,
		Connection: connectionTo(srv, "wrong-key", "example.com"),
		Calls:      []wrapperprotocol.VerbCall{{Verb: wrapperprotocol.VerbProbe}},
	}
	resp := Handle(req, srv.Client())
	if len(resp.Results) != 1 {
		t.Fatalf("got %d results, want 1 (stop at the failed probe)", len(resp.Results))
	}
	res := resp.Results[0]
	if res.Succeeded() {
		t.Fatal("probe succeeded with the wrong key")
	}
	if res.Failed == "" {
		t.Fatalf("expected Failed, got refused=%q", res.Refused)
	}
}

func TestWrongContractFailsFirstCallAndStops(t *testing.T) {
	req := wrapperprotocol.WrapperRequest{
		Contract: "2-draft",
		Calls: []wrapperprotocol.VerbCall{
			{Verb: wrapperprotocol.VerbProbe},
			{Verb: wrapperprotocol.VerbReadTotal},
		},
	}
	resp := Handle(req, http.DefaultClient)
	if len(resp.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(resp.Results))
	}
	if resp.Results[0].Failed == "" {
		t.Fatal("expected the first result to be failed")
	}
}

func TestReadTotalMapsMeasureAndFilter(t *testing.T) {
	srv := fakePlausible(t, "k", func(q plausibleQuery) (int, any) {
		if len(q.Metrics) != 1 || q.Metrics[0] != "pageviews" {
			t.Fatalf("metrics = %v, want [pageviews]", q.Metrics)
		}
		wantRange := []string{"2026-09-23T00:00:00Z", "2026-09-23T23:59:59Z"}
		gotRange, ok := q.DateRange.([]interface{})
		if !ok || len(gotRange) != 2 || gotRange[0] != wantRange[0] || gotRange[1] != wantRange[1] {
			t.Fatalf("date_range = %#v, want %v", q.DateRange, wantRange)
		}
		if len(q.Filters) != 1 {
			t.Fatalf("filters = %v, want one", q.Filters)
		}
		return http.StatusOK, plausibleResponse{Results: []plausibleResult{{Metrics: []float64{412}}}}
	})
	defer srv.Close()

	start := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	req := wrapperprotocol.WrapperRequest{
		Contract:   wrapperprotocol.ContractVersion,
		Connection: connectionTo(srv, "k", "example.com"),
		Calls: []wrapperprotocol.VerbCall{{
			Verb:    wrapperprotocol.VerbReadTotal,
			Measure: &wrapperprotocol.Measure{Event: "pageviews", Aggregation: "count"},
			Window:  &wrapperprotocol.Window{Start: start, End: end},
			Filter:  &wrapperprotocol.Filter{Dimension: "visit:utm_content", Value: "hn"},
		}},
	}
	resp := Handle(req, srv.Client())
	res := resp.Results[0]
	if !res.Succeeded() {
		t.Fatalf("read_total did not succeed: %s", res.Why())
	}
	if res.Total == nil || res.Total.Value != 412 {
		t.Fatalf("total = %#v, want 412", res.Total)
	}
}

func TestReadTotalZeroStartMeansAll(t *testing.T) {
	srv := fakePlausible(t, "k", func(q plausibleQuery) (int, any) {
		if q.DateRange != "all" {
			t.Fatalf("date_range = %#v, want \"all\"", q.DateRange)
		}
		return http.StatusOK, plausibleResponse{Results: []plausibleResult{{Metrics: []float64{9}}}}
	})
	defer srv.Close()

	req := wrapperprotocol.WrapperRequest{
		Contract:   wrapperprotocol.ContractVersion,
		Connection: connectionTo(srv, "k", "example.com"),
		Calls: []wrapperprotocol.VerbCall{{
			Verb:    wrapperprotocol.VerbReadTotal,
			Measure: &wrapperprotocol.Measure{Event: "visitors", Aggregation: "unique"},
			Window:  &wrapperprotocol.Window{End: time.Now()},
		}},
	}
	resp := Handle(req, srv.Client())
	res := resp.Results[0]
	if !res.Succeeded() {
		t.Fatalf("read_total did not succeed: %s", res.Why())
	}
	if res.Total.Value != 9 {
		t.Fatalf("total = %#v, want 9", res.Total)
	}
}

func TestReadTotalRefusesUnknownMetric(t *testing.T) {
	req := wrapperprotocol.WrapperRequest{
		Contract:   wrapperprotocol.ContractVersion,
		Connection: wrapperprotocol.Connection{AccountID: "example.com", Credentials: map[string]string{apiKeyCredential: "k"}},
		Calls: []wrapperprotocol.VerbCall{{
			Verb:    wrapperprotocol.VerbReadTotal,
			Measure: &wrapperprotocol.Measure{Event: "time_on_page", Aggregation: "sum"},
			Window:  &wrapperprotocol.Window{Start: time.Now().Add(-time.Hour), End: time.Now()},
		}},
	}
	resp := Handle(req, http.DefaultClient)
	res := resp.Results[0]
	if res.Refused == "" {
		t.Fatalf("expected a refusal, got %#v", res)
	}
}

func TestReadTotalRefusesWrongAggregation(t *testing.T) {
	req := wrapperprotocol.WrapperRequest{
		Contract:   wrapperprotocol.ContractVersion,
		Connection: wrapperprotocol.Connection{AccountID: "example.com", Credentials: map[string]string{apiKeyCredential: "k"}},
		Calls: []wrapperprotocol.VerbCall{{
			Verb:    wrapperprotocol.VerbReadTotal,
			Measure: &wrapperprotocol.Measure{Event: "pageviews", Aggregation: "unique"},
			Window:  &wrapperprotocol.Window{Start: time.Now().Add(-time.Hour), End: time.Now()},
		}},
	}
	resp := Handle(req, http.DefaultClient)
	res := resp.Results[0]
	if res.Refused == "" {
		t.Fatalf("expected a refusal, got %#v", res)
	}
}

func TestReadTotalRefusesUnknownFilterDimension(t *testing.T) {
	req := wrapperprotocol.WrapperRequest{
		Contract:   wrapperprotocol.ContractVersion,
		Connection: wrapperprotocol.Connection{AccountID: "example.com", Credentials: map[string]string{apiKeyCredential: "k"}},
		Calls: []wrapperprotocol.VerbCall{{
			Verb:    wrapperprotocol.VerbReadTotal,
			Measure: &wrapperprotocol.Measure{Event: "pageviews", Aggregation: "count"},
			Window:  &wrapperprotocol.Window{Start: time.Now().Add(-time.Hour), End: time.Now()},
			Filter:  &wrapperprotocol.Filter{Dimension: "visit:not_a_real_dimension", Value: "x"},
		}},
	}
	resp := Handle(req, http.DefaultClient)
	res := resp.Results[0]
	if res.Refused == "" {
		t.Fatalf("expected a refusal, got %#v", res)
	}
}

func TestReadSeriesDay(t *testing.T) {
	srv := fakePlausible(t, "k", func(q plausibleQuery) (int, any) {
		if len(q.Dimensions) != 1 || q.Dimensions[0] != "time:day" {
			t.Fatalf("dimensions = %v, want [time:day]", q.Dimensions)
		}
		return http.StatusOK, plausibleResponse{Results: []plausibleResult{
			{Dimensions: []string{"2026-09-23"}, Metrics: []float64{10}},
			{Dimensions: []string{"2026-09-24"}, Metrics: []float64{20}},
		}}
	})
	defer srv.Close()

	req := wrapperprotocol.WrapperRequest{
		Contract:   wrapperprotocol.ContractVersion,
		Connection: connectionTo(srv, "k", "example.com"),
		Calls: []wrapperprotocol.VerbCall{{
			Verb:        wrapperprotocol.VerbReadSeries,
			Measure:     &wrapperprotocol.Measure{Event: "visits", Aggregation: "count"},
			Window:      &wrapperprotocol.Window{Start: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)},
			Granularity: wrapperprotocol.GranularityDay,
		}},
	}
	resp := Handle(req, srv.Client())
	res := resp.Results[0]
	if !res.Succeeded() {
		t.Fatalf("read_series did not succeed: %s", res.Why())
	}
	if len(res.Series) != 2 {
		t.Fatalf("got %d points, want 2", len(res.Series))
	}
	want := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	if !res.Series[0].At.Equal(want) {
		t.Fatalf("first point at %v, want %v", res.Series[0].At, want)
	}
	if res.Series[0].Value != 10 || res.Series[1].Value != 20 {
		t.Fatalf("values = %v, %v; want 10, 20", res.Series[0].Value, res.Series[1].Value)
	}
}

func TestReadSeriesHourParsesTimestampWithSeconds(t *testing.T) {
	srv := fakePlausible(t, "k", func(q plausibleQuery) (int, any) {
		return http.StatusOK, plausibleResponse{Results: []plausibleResult{
			{Dimensions: []string{"2026-09-23 14:00:00"}, Metrics: []float64{3}},
		}}
	})
	defer srv.Close()

	req := wrapperprotocol.WrapperRequest{
		Contract:   wrapperprotocol.ContractVersion,
		Connection: connectionTo(srv, "k", "example.com"),
		Calls: []wrapperprotocol.VerbCall{{
			Verb:        wrapperprotocol.VerbReadSeries,
			Measure:     &wrapperprotocol.Measure{Event: "pageviews", Aggregation: "count"},
			Window:      &wrapperprotocol.Window{Start: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)},
			Granularity: wrapperprotocol.GranularityHour,
		}},
	}
	resp := Handle(req, srv.Client())
	res := resp.Results[0]
	if !res.Succeeded() {
		t.Fatalf("read_series did not succeed: %s", res.Why())
	}
	// The window covers 24 hourly steps; the contract wants one point per
	// step, empty steps included (GUIDE.md, "Verbs in contract 1"), so
	// Plausible's one row for 14:00 must not shrink the series to one point.
	if len(res.Series) != 24 {
		t.Fatalf("got %d points, want 24 (one per hour of the window)", len(res.Series))
	}
	want := time.Date(2026, 9, 23, 14, 0, 0, 0, time.UTC)
	for i, p := range res.Series {
		wantAt := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
		if !p.At.Equal(wantAt) {
			t.Fatalf("point %d at %v, want %v", i, p.At, wantAt)
		}
		if p.At.Equal(want) {
			if p.Value != 3 {
				t.Fatalf("point at %v = %v, want 3", want, p.Value)
			}
			continue
		}
		if p.Value != 0 {
			t.Fatalf("empty point at %v = %v, want 0", p.At, p.Value)
		}
	}
}

func TestReadSeriesFillsEmptyStepsWithZero(t *testing.T) {
	srv := fakePlausible(t, "k", func(q plausibleQuery) (int, any) {
		if len(q.Dimensions) != 1 || q.Dimensions[0] != "time:week" {
			t.Fatalf("dimensions = %v, want [time:week]", q.Dimensions)
		}
		// Plausible answers only the weeks it has data for.
		return http.StatusOK, plausibleResponse{Results: []plausibleResult{
			{Dimensions: []string{"2026-08-31"}, Metrics: []float64{5}},
		}}
	})
	defer srv.Close()

	req := wrapperprotocol.WrapperRequest{
		Contract:   wrapperprotocol.ContractVersion,
		Connection: connectionTo(srv, "k", "example.com"),
		Calls: []wrapperprotocol.VerbCall{{
			Verb:        wrapperprotocol.VerbReadSeries,
			Measure:     &wrapperprotocol.Measure{Event: "visits", Aggregation: "count"},
			Window:      &wrapperprotocol.Window{Start: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)},
			Granularity: wrapperprotocol.GranularityWeek,
		}},
	}
	resp := Handle(req, srv.Client())
	res := resp.Results[0]
	if !res.Succeeded() {
		t.Fatalf("read_series did not succeed: %s", res.Why())
	}
	if len(res.Series) != 4 {
		t.Fatalf("got %d points, want 4 (one per week of the window)", len(res.Series))
	}
	if res.Series[0].Value != 5 {
		t.Fatalf("first week = %v, want 5", res.Series[0].Value)
	}
	for i := 1; i < len(res.Series); i++ {
		if res.Series[i].Value != 0 {
			t.Fatalf("week %d = %v, want 0", i, res.Series[i].Value)
		}
	}
}

func TestReadSeriesRefusesUnknownGranularity(t *testing.T) {
	req := wrapperprotocol.WrapperRequest{
		Contract:   wrapperprotocol.ContractVersion,
		Connection: wrapperprotocol.Connection{AccountID: "example.com", Credentials: map[string]string{apiKeyCredential: "k"}},
		Calls: []wrapperprotocol.VerbCall{{
			Verb:        wrapperprotocol.VerbReadSeries,
			Measure:     &wrapperprotocol.Measure{Event: "pageviews", Aggregation: "count"},
			Window:      &wrapperprotocol.Window{Start: time.Now().Add(-time.Hour), End: time.Now()},
			Granularity: "fortnight",
		}},
	}
	resp := Handle(req, http.DefaultClient)
	res := resp.Results[0]
	if res.Refused == "" {
		t.Fatalf("expected a refusal, got %#v", res)
	}
}

// TestEveryOtherVerbIsRefusedAndDeclared checks that every verb besides
// probe, read_series and read_total is refused when called directly, and
// declined in the manifest, matching wrapper.json's claims.
func TestEveryOtherVerbIsRefusedAndDeclared(t *testing.T) {
	srv := fakePlausible(t, "k", func(q plausibleQuery) (int, any) {
		return http.StatusOK, plausibleResponse{Results: []plausibleResult{{Metrics: []float64{1}}}}
	})
	defer srv.Close()

	manifest := buildManifest()
	for _, verb := range wrapperprotocol.ContractVerbs() {
		if verb == wrapperprotocol.VerbProbe || verb == wrapperprotocol.VerbReadSeries || verb == wrapperprotocol.VerbReadTotal {
			continue
		}
		support, ok := manifest.Verbs[verb]
		if !ok {
			t.Fatalf("manifest does not answer for verb %s", verb)
		}
		if support.Level != wrapperprotocol.VerbDeclined {
			t.Fatalf("verb %s has level %s, want declined", verb, support.Level)
		}

		req := wrapperprotocol.WrapperRequest{
			Contract:   wrapperprotocol.ContractVersion,
			Connection: connectionTo(srv, "k", "example.com"),
			Calls:      []wrapperprotocol.VerbCall{{Verb: verb}},
		}
		resp := Handle(req, srv.Client())
		res := resp.Results[0]
		if res.Refused == "" {
			t.Fatalf("verb %s was not refused: %#v", verb, res)
		}
	}
}

func TestWrapperJSONAgreesWithProbe(t *testing.T) {
	raw, err := os.ReadFile("wrapper.json")
	if err != nil {
		t.Fatal(err)
	}
	var desc wrapperprotocol.Description
	if err := json.Unmarshal(raw, &desc); err != nil {
		t.Fatal(err)
	}
	if why := desc.Check(); why != "" {
		t.Fatalf("wrapper.json fails Description.Check: %s", why)
	}
	if desc.Tool.Slug != "plausible" {
		t.Fatalf("tool.slug = %q, want plausible", desc.Tool.Slug)
	}
	if desc.Version != wrapperVersion {
		t.Fatalf("wrapper.json version %q does not match the wrapper's %q", desc.Version, wrapperVersion)
	}
	if desc.Contract != wrapperprotocol.ContractVersion {
		t.Fatalf("wrapper.json contract %q does not match the wrapper's %q", desc.Contract, wrapperprotocol.ContractVersion)
	}
	if len(desc.Command) == 0 || desc.Command[0] != "/plausible" {
		t.Fatalf("wrapper.json command = %v, want [/plausible] (the Dockerfile's ENTRYPOINT)", desc.Command)
	}

	manifest := buildManifest()
	if why := manifest.Check(); why != "" {
		t.Fatalf("manifest fails Check: %s", why)
	}
	// The wrapper's own tests check its probe does not decline anything
	// wrapper.json claims (GUIDE.md, "wrapper.json").
	for verb, claim := range desc.Claims {
		support, ok := manifest.Verbs[wrapperprotocol.Verb(verb)]
		if !ok {
			t.Fatalf("wrapper.json claims %s but the manifest does not answer for it", verb)
		}
		if claim == "declined" {
			continue
		}
		if support.Level == wrapperprotocol.VerbDeclined {
			t.Fatalf("wrapper.json claims %s as %q but the manifest declines it", verb, claim)
		}
	}
}

func TestFakeServerHonoursAuthorizationHeader(t *testing.T) {
	srv := fakePlausible(t, "right-key", func(q plausibleQuery) (int, any) {
		return http.StatusOK, plausibleResponse{Results: []plausibleResult{{Metrics: []float64{1}}}}
	})
	defer srv.Close()

	c := &client{http: srv.Client(), endpoint: srv.URL, apiKey: "right-key", siteID: "example.com"}
	if _, err := c.query(plausibleQuery{SiteID: "example.com", DateRange: "day", Metrics: []string{"visitors"}}); err != nil {
		t.Fatalf("query with the right key failed: %v", err)
	}
	c.apiKey = "wrong-key"
	if _, err := c.query(plausibleQuery{SiteID: "example.com", DateRange: "day", Metrics: []string{"visitors"}}); err == nil {
		t.Fatal("query with the wrong key succeeded")
	}
}
