package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// Every verb against recorded responses from a local server standing in for
// Plausible; nothing here calls the real API. The bodies are in the shape the
// Stats API v2 documents (README.md): results[].metrics, results[].dimensions,
// meta.time_labels, and {"error": ...} on a 400.
//
// The wrapper's answers are then read with Mendel's own protocol code --
// wrapperprotocol.ParseWrapperResponse and CapabilityManifest.Check -- so the types
// restated in main.go cannot drift from internal/external's. This is a test
// import only: the binary stays standard library.

type recorded struct {
	status int
	body   string
}

// fakePlausible answers each query with the next recorded response and keeps
// what it was sent.
type fakePlausible struct {
	t         *testing.T
	responses []recorded
	queries   []map[string]any
	auth      []string
}

func (f *fakePlausible) serve() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/query" {
			f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		var q map[string]any
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &q); err != nil {
			f.t.Errorf("the wrapper sent a body that is not JSON: %s", raw)
		}
		f.queries = append(f.queries, q)
		if len(f.responses) == 0 {
			f.t.Fatal("more queries than recorded responses")
		}
		next := f.responses[0]
		f.responses = f.responses[1:]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(next.status)
		w.Write([]byte(next.body))
	}))
}

// do runs the wrapper as a container would be run, and reads its answer with
// Mendel's own parser.
func do(t *testing.T, fake *fakePlausible, calls ...map[string]any) (wrapperprotocol.WrapperResponse, error) {
	t.Helper()
	server := fake.serve()
	defer server.Close()
	req := map[string]any{
		"contract": "1",
		"connection": map[string]any{
			"credentials": map[string]string{"PLAUSIBLE_API_KEY": "key-123"},
			"account_id":  "pong.example", "endpoint": server.URL,
		},
		"calls": calls,
	}
	in, _ := json.Marshal(req)
	var out bytes.Buffer
	if err := run(context.Background(), bytes.NewReader(in), &out, server.Client()); err != nil {
		t.Fatal(err)
	}
	return wrapperprotocol.ParseWrapperResponse(out.Bytes(), len(calls))
}

const aggregateOK = `{"results":[{"metrics":[3],"dimensions":[]}],"meta":{},"query":{}}`

func span(start, end time.Time) map[string]any {
	return map[string]any{"start": start, "end": end}
}

var (
	day0 = time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	day1 = time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
)

func TestProbeAnswersAManifestMendelAccepts(t *testing.T) {
	fake := &fakePlausible{t: t, responses: []recorded{{200, aggregateOK}}}
	resp, err := do(t, fake, map[string]any{"verb": "probe"})
	if err != nil {
		t.Fatal(err)
	}
	res, why := resp.Answer(0)
	if why != "" {
		t.Fatal(why)
	}
	m := res.Manifest
	if why := m.Check(); why != "" {
		t.Fatalf("Mendel refuses the manifest: %s", why)
	}
	if !m.IsDataSource() {
		t.Error("the manifest offers a write verb")
	}
	if u := m.Metrics["visitors"].Uniqueness; !strings.Contains(u, "two visitors") || !strings.Contains(u, "metrics-definitions") {
		t.Errorf("visitors' uniqueness is not Plausible's, cited: %q", u)
	}
	if m.Metrics["bounce_rate"].Level != wrapperprotocol.MetricUnavailable || m.Metrics["bounce_rate"].Reason == "" {
		t.Error("a rate is declared unavailable, with a reason")
	}
	if m.Wrapper.SpecSource != specSource || m.Wrapper.SpecHash != specHash() || !strings.HasPrefix(m.Wrapper.SpecHash, "sha256:") {
		t.Errorf("provenance: %+v", m.Wrapper)
	}
	if fake.auth[0] != "Bearer key-123" {
		t.Errorf("the key was sent as %q", fake.auth[0])
	}
	if fake.queries[0]["site_id"] != "pong.example" || fake.queries[0]["date_range"] != "day" {
		t.Errorf("the probe's query: %v", fake.queries[0])
	}
}

func TestReadTotalQueriesTheWindowAsInstants(t *testing.T) {
	fake := &fakePlausible{t: t, responses: []recorded{
		{200, `{"results":[{"metrics":[412],"dimensions":[]}],"meta":{},"query":{}}`},
		{200, `{"results":[{"metrics":[37],"dimensions":[]}],"meta":{},"query":{}}`},
		{200, `{"results":[{"metrics":[5000],"dimensions":[]}],"meta":{},"query":{}}`},
	}}
	resp, err := do(t, fake,
		map[string]any{"verb": "read_total", "measure": map[string]string{"event": "pageviews", "aggregation": "count"},
			"window": span(day0, day1), "filter": map[string]string{"dimension": "visit:utm_content", "value": "hn"}},
		map[string]any{"verb": "read_total", "measure": map[string]string{"event": "Signup", "aggregation": "unique"},
			"window": span(day0, day1)},
		map[string]any{"verb": "read_total", "measure": map[string]string{"event": "visitors", "aggregation": "unique"},
			"window": span(time.Time{}, day1)},
	)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []float64{412, 37, 5000} {
		res, why := resp.Answer(i)
		if why != "" || res.Total == nil || res.Total.Value != want {
			t.Errorf("call %d: %+v %s", i, res, why)
		}
	}
	q := fake.queries[0]
	if dr, _ := json.Marshal(q["date_range"]); string(dr) != `["2026-09-23T00:00:00Z","2026-09-23T23:59:59Z"]` {
		t.Errorf("date_range %s: the API's range is inclusive, so the end is the last second", dr)
	}
	if f, _ := json.Marshal(q["filters"]); string(f) != `[["is","visit:utm_content",["hn"]]]` {
		t.Errorf("filters %s", f)
	}
	// A goal is read through event:goal: unique is its visitors.
	if m, _ := json.Marshal(fake.queries[1]["metrics"]); string(m) != `["visitors"]` {
		t.Errorf("goal metric %s", m)
	}
	if f, _ := json.Marshal(fake.queries[1]["filters"]); string(f) != `[["is","event:goal",["Signup"]]]` {
		t.Errorf("goal filter %s", f)
	}
	// No start is the total to date.
	if fake.queries[2]["date_range"] != "all" {
		t.Errorf("lifetime date_range %v", fake.queries[2]["date_range"])
	}
}

func TestReadSeriesFillsLabelledBucketsAndRefusesAGranularityItLacks(t *testing.T) {
	fake := &fakePlausible{t: t, responses: []recorded{{200, `{
		"results":[{"metrics":[10],"dimensions":["2026-09-21"]},{"metrics":[4],"dimensions":["2026-09-23"]}],
		"meta":{"time_labels":["2026-09-21","2026-09-22","2026-09-23"]},"query":{}}`}}}
	resp, err := do(t, fake,
		map[string]any{"verb": "read_series", "measure": map[string]string{"event": "visits", "aggregation": "count"},
			"window": span(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), day1), "granularity": "day"},
		map[string]any{"verb": "read_series", "measure": map[string]string{"event": "visits", "aggregation": "count"},
			"window": span(day0, day1), "granularity": "minute"},
		map[string]any{"verb": "read_total", "measure": map[string]string{"event": "visits", "aggregation": "count"},
			"window": span(day0, day1)},
	)
	if err != nil {
		t.Fatal(err)
	}
	res, why := resp.Answer(0)
	if why != "" {
		t.Fatal(why)
	}
	var got []float64
	for _, p := range res.Series {
		got = append(got, p.Value)
	}
	if len(got) != 3 || got[0] != 10 || got[1] != 0 || got[2] != 4 {
		t.Errorf("series %v, want 10, 0, 4: a labelled bucket with no row is a zero", got)
	}
	if dims, _ := json.Marshal(fake.queries[0]["dimensions"]); string(dims) != `["time:day"]` {
		t.Errorf("dimensions %s", dims)
	}
	// minute is refused, not substituted; the run stops there.
	if _, why := resp.Answer(1); !strings.Contains(why, "refuses rather than substitute") {
		t.Errorf("minute: %q", why)
	}
	if _, why := resp.Answer(2); !strings.Contains(why, "stopped at read_series") {
		t.Errorf("the call after a refusal: %q", why)
	}
	if len(fake.queries) != 1 {
		t.Errorf("%d queries: a refused call sends nothing, and nothing after it runs", len(fake.queries))
	}
}

func TestRefusalsAndFailures(t *testing.T) {
	cases := []struct {
		name     string
		response []recorded
		call     map[string]any
		refused  string
		failed   string
	}{
		{"an unknown goal", []recorded{{400, `{"error":"The goal ` + "`Nope`" + ` is not configured for this site"}`}},
			map[string]any{"verb": "read_total", "measure": map[string]string{"event": "Nope", "aggregation": "count"},
				"window": span(day0, day1)}, "not configured", ""},
		{"a rejected key", []recorded{{401, `{"error":"Invalid API key"}`}},
			map[string]any{"verb": "probe"}, "", "refused the API key"},
		{"no such site", []recorded{{404, `{"error":"Site not found"}`}},
			map[string]any{"verb": "probe"}, "", "no site pong.example"},
		{"rate limited", []recorded{{429, `{}`}},
			map[string]any{"verb": "probe"}, "", "600 requests an hour"},
		{"a rate asked for", nil,
			map[string]any{"verb": "read_total", "measure": map[string]string{"event": "bounce_rate", "aggregation": "count"},
				"window": span(day0, day1)}, "unavailable", ""},
		{"visitors counted", nil,
			map[string]any{"verb": "read_total", "measure": map[string]string{"event": "visitors", "aggregation": "count"},
				"window": span(day0, day1)}, "read with unique", ""},
		{"a filter on an unknown dimension", nil,
			map[string]any{"verb": "read_total", "measure": map[string]string{"event": "pageviews", "aggregation": "count"},
				"window": span(day0, day1), "filter": map[string]string{"dimension": "visit:mood", "value": "x"}},
			"not a dimension", ""},
		{"a write verb", nil, map[string]any{"verb": "publish"}, "declares publish absent", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakePlausible{t: t, responses: c.response}
			resp, err := do(t, fake, c.call)
			if err != nil {
				t.Fatal(err)
			}
			res, _ := resp.Answer(0)
			if c.refused != "" && !strings.Contains(res.Refused, c.refused) {
				t.Errorf("refused %q, want %q (failed %q)", res.Refused, c.refused, res.Failed)
			}
			if c.failed != "" && !strings.Contains(res.Failed, c.failed) {
				t.Errorf("failed %q, want %q (refused %q)", res.Failed, c.failed, res.Refused)
			}
		})
	}
}

func TestAnotherContractIsAnsweredAndNotActedOn(t *testing.T) {
	var out bytes.Buffer
	in := `{"contract":"2","connection":{},"calls":[{"verb":"probe"},{"verb":"read_total"}]}`
	if err := run(context.Background(), strings.NewReader(in), &out, http.DefaultClient); err != nil {
		t.Fatal(err)
	}
	resp, err := wrapperprotocol.ParseWrapperResponse(out.Bytes(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, why := resp.Answer(0); !strings.Contains(why, `sent "2"`) {
		t.Errorf("answer %q", why)
	}
	if err := run(context.Background(), strings.NewReader("not json"), &out, http.DefaultClient); err == nil {
		t.Error("an unreadable request is an error")
	}
}

// wrapper.json is how Mendel learns this wrapper exists, before it has probed
// anything. It must agree with the code: the same version and contract, so a
// probe records against the row the registry seeded, the same spec, and no
// claim the probe does not back.
func TestWrapperJSONAgreesWithWhatTheWrapperAnswers(t *testing.T) {
	raw, err := os.ReadFile("wrapper.json")
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Version    string            `json:"version"`
		Contract   string            `json:"contract"`
		Command    []string          `json:"command"`
		SpecSource string            `json:"spec_source"`
		Claims     map[string]string `json:"claims"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}

	// The command is what the image runs, which a Job in a project's cluster
	// cannot learn from the image: Mendel's shim replaces the entrypoint and
	// runs this instead. So it is the Dockerfile's ENTRYPOINT, exactly.
	dockerfile, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	var entrypoint []string
	for _, line := range strings.Split(string(dockerfile), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ENTRYPOINT "); ok {
			if err := json.Unmarshal([]byte(rest), &entrypoint); err != nil {
				t.Fatalf("the ENTRYPOINT is not in exec form, so it names no command a Job could run: %s", rest)
			}
		}
	}
	if strings.Join(entrypoint, "\x00") != strings.Join(w.Command, "\x00") || len(entrypoint) == 0 {
		t.Errorf("wrapper.json's command is %q; the Dockerfile's ENTRYPOINT is %q", w.Command, entrypoint)
	}
	if w.Version != wrapperVersion || w.Contract != contractVersion || w.SpecSource != specSource {
		t.Errorf("wrapper.json says version %q contract %q spec %q; the code says %q %q %q",
			w.Version, w.Contract, w.SpecSource, wrapperVersion, contractVersion, specSource)
	}
	answered := verbs()
	for verb, claimed := range w.Claims {
		got, ok := answered[verb]
		if !ok {
			t.Errorf("wrapper.json claims %s, which the probe does not answer for", verb)
			continue
		}
		if claimed != "declined" && got.Level == "declined" {
			t.Errorf("wrapper.json claims %s is %s; the probe declines it", verb, claimed)
		}
	}
}
