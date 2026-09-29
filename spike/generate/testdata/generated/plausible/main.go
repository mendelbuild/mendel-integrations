// Command plausible is an External Tool Wrapper for Plausible Analytics
// (https://plausible.io), speaking Mendel's wrapper protocol, contract "1"
// (github.com/mendelbuild/mendelbuild/wrapperprotocol). It is a read-only
// data source: it answers probe, read_series and read_total against the
// Stats API v2 (POST /api/v2/query) and declines every write verb.
//
// It is written to run against a self-hosted Plausible Community Edition,
// reached through the connection's endpoint; see README.md for what was
// fetched and when.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// wrapperVersion must agree with plausible/wrapper.json's "version": a
// wrapper's probe answers as the version it was registered under, and
// TestWrapperJSONAgrees checks the two never drift apart.
const wrapperVersion = "0.1.0"

// specSource is the URL the wrapper was written against; cited with the date
// fetched in README.md.
const specSource = "https://plausible.io/docs/stats-api"

// specHash is a short, human content hash of the fetched spec files this
// wrapper was written against (spec/plausible/*), so a changed spec is
// visible in the manifest even though the wrapper does not fetch it live.
const specHash = "sha256-ed9a0a1a48ed-2026-09-29"

// apiKeyCredential is the name a project's Plausible API key arrives under
// (wrapper.json's connection.credentials).
const apiKeyCredential = "PLAUSIBLE_API_KEY"

// defaultEndpoint is the tool's own hosted instance, used when the
// connection names no self-hosted one.
const defaultEndpoint = "https://plausible.io"

func main() {
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plausible: reading stdin:", err)
		os.Exit(1)
	}
	var req wrapperprotocol.WrapperRequest
	if err := json.Unmarshal(in, &req); err != nil {
		fmt.Fprintln(os.Stderr, "plausible: request is not readable JSON:", err)
		os.Exit(1)
	}
	resp := Handle(req, http.DefaultClient)
	out, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plausible: could not marshal response:", err)
		os.Exit(1)
	}
	os.Stdout.Write(out)
}

// Handle answers every call in req, in order, stopping at the first result
// that does not succeed, per the protocol. httpClient is injected so tests
// never reach the real API.
func Handle(req wrapperprotocol.WrapperRequest, httpClient *http.Client) wrapperprotocol.WrapperResponse {
	var results []wrapperprotocol.VerbResult
	if req.Contract != wrapperprotocol.ContractVersion {
		if len(req.Calls) == 0 {
			return wrapperprotocol.WrapperResponse{Results: results}
		}
		return wrapperprotocol.WrapperResponse{Results: []wrapperprotocol.VerbResult{{
			Verb:   req.Calls[0].Verb,
			Failed: fmt.Sprintf("this wrapper speaks contract %q; the request is for contract %q", wrapperprotocol.ContractVersion, req.Contract),
		}}}
	}

	c := newClient(req.Connection, httpClient)

	for _, call := range req.Calls {
		res := dispatch(c, req.Connection, call)
		results = append(results, res)
		if !res.Succeeded() {
			break
		}
	}
	return wrapperprotocol.WrapperResponse{Results: results}
}

func dispatch(c *client, conn wrapperprotocol.Connection, call wrapperprotocol.VerbCall) wrapperprotocol.VerbResult {
	switch call.Verb {
	case wrapperprotocol.VerbProbe:
		return probe(c, conn)
	case wrapperprotocol.VerbReadTotal:
		return readTotal(c, call)
	case wrapperprotocol.VerbReadSeries:
		return readSeries(c, call)
	default:
		reason, ok := declinedReasons[call.Verb]
		if !ok {
			reason = "this wrapper is a read-only data source and does not honour this verb"
		}
		return wrapperprotocol.VerbResult{Verb: call.Verb, Refused: reason}
	}
}

// --- HTTP client ---

type client struct {
	http     *http.Client
	endpoint string
	apiKey   string
	siteID   string
}

func newClient(conn wrapperprotocol.Connection, httpClient *http.Client) *client {
	endpoint := strings.TrimRight(conn.Endpoint, "/")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return &client{
		http:     httpClient,
		endpoint: endpoint,
		apiKey:   conn.Credentials[apiKeyCredential],
		siteID:   conn.AccountID,
	}
}

// plausibleQuery is the body of POST /api/v2/query (spec/plausible/stats-api.md,
// "Request structure", fetched 2026-09-29).
type plausibleQuery struct {
	SiteID     string          `json:"site_id"`
	DateRange  interface{}     `json:"date_range"`
	Metrics    []string        `json:"metrics"`
	Dimensions []string        `json:"dimensions,omitempty"`
	Filters    [][]interface{} `json:"filters,omitempty"`
}

// plausibleResult is one row of /api/v2/query's "results" (same doc,
// "Response structure").
type plausibleResult struct {
	Dimensions []string  `json:"dimensions"`
	Metrics    []float64 `json:"metrics"`
}

type plausibleResponse struct {
	Results []plausibleResult `json:"results"`
}

type plausibleError struct {
	Error string `json:"error"`
}

// query runs one Stats API v2 query and returns its rows, or an error
// sentence suitable for Failed.
func (c *client) query(q plausibleQuery) ([]plausibleResult, error) {
	body, err := json.Marshal(q)
	if err != nil {
		return nil, fmt.Errorf("building the query: %w", err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, c.endpoint+"/api/v2/query", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building the request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", c.endpoint, err)
	}
	defer httpResp.Body.Close()
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading the response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		var pe plausibleError
		msg := strings.TrimSpace(string(respBody))
		if json.Unmarshal(respBody, &pe) == nil && pe.Error != "" {
			msg = pe.Error
		}
		return nil, fmt.Errorf("Plausible answered %d: %s", httpResp.StatusCode, msg)
	}
	var pr plausibleResponse
	if err := json.Unmarshal(respBody, &pr); err != nil {
		return nil, fmt.Errorf("the response is not readable as JSON: %w", err)
	}
	return pr.Results, nil
}

// --- metrics and dimensions this wrapper offers ---

// metricInfo is how one contract measure.Event maps onto a Plausible Stats
// API metric name (spec/plausible/stats-api.md, "metrics", fetched
// 2026-09-29).
type metricInfo struct {
	plausibleMetric string
	aggregations    []string
	uniqueness      string
	quality         []string
}

// metricCatalog is every measure.Event this wrapper reads. count/unique are
// used where Plausible's own metric already means exactly that ("visitors"
// is Plausible's unique count, "visits"/"pageviews"/"events" are plain
// counts); "sum" is used for the metrics that arrive already aggregated as a
// single float or int with no separate raw/unique variant (bounce rate,
// visit duration, views per visit), since the contract offers no fourth
// bucket for "the tool's own pre-aggregated figure".
var metricCatalog = map[string]metricInfo{
	"visitors": {
		plausibleMetric: "visitors",
		aggregations:    []string{"unique"},
		uniqueness: "a person is one unique visitor per day regardless of how many sessions or devices they use that " +
			"day, and Plausible uses no cookies or other persistent identifiers to recognise them across days " +
			"(spec/plausible/metrics-definitions.md, \"Unique Visitors\", fetched 2026-09-29)",
	},
	"visits": {
		plausibleMetric: "visits",
		aggregations:    []string{"count"},
	},
	"pageviews": {
		plausibleMetric: "pageviews",
		aggregations:    []string{"count"},
	},
	"events": {
		plausibleMetric: "events",
		aggregations:    []string{"count"},
	},
	"bounce_rate": {
		plausibleMetric: "bounce_rate",
		aggregations:    []string{"sum"},
		quality:         []string{"percentage"},
	},
	"visit_duration": {
		plausibleMetric: "visit_duration",
		aggregations:    []string{"sum"},
		quality:         []string{"seconds"},
	},
	"views_per_visit": {
		plausibleMetric: "views_per_visit",
		aggregations:    []string{"sum"},
	},
}

// unavailableMetrics are Stats API metrics this wrapper declines to read
// because they need a filter, dimension or account setup this wrapper does
// not supply generically (spec/plausible/stats-api.md, "metrics" table's
// "Requirements" column, fetched 2026-09-29).
var unavailableMetrics = map[string]string{
	"scroll_depth": "requires an event:page filter or dimension being set, which this wrapper does not add on its own",
	"percentage": "requires a non-empty dimensions list; it names a share of a group, not a single total or " +
		"series value the contract can express",
	"conversion_rate":       "requires an event:goal filter or dimension naming which goal converted",
	"group_conversion_rate": "requires an event:goal filter or dimension naming which goal converted",
	"average_revenue":       "requires revenue goals configured on the site and an event:goal filter or dimension for one",
	"total_revenue":         "requires revenue goals configured on the site and an event:goal filter or dimension for one",
	"time_on_page":          "requires an event:page filter or dimension being set, which this wrapper does not add on its own",
}

// filterDimensions are the equality dimensions this wrapper's manifest
// offers (spec/plausible/stats-api.md, "dimensions", fetched 2026-09-29).
// event:props:* custom properties are left out: they are per-site and this
// wrapper cannot list them without an extra call this account may not permit.
var filterDimensions = []string{
	"event:goal", "event:page", "event:hostname",
	"visit:entry_page", "visit:entry_page_hostname", "visit:exit_page", "visit:exit_page_hostname",
	"visit:source", "visit:referrer", "visit:channel",
	"visit:utm_medium", "visit:utm_source", "visit:utm_campaign", "visit:utm_content", "visit:utm_term",
	"visit:device", "visit:browser", "visit:browser_version", "visit:os", "visit:os_version",
	"visit:country", "visit:region", "visit:city", "visit:country_name", "visit:region_name", "visit:city_name",
}

var filterDimensionSet = func() map[string]bool {
	m := make(map[string]bool, len(filterDimensions))
	for _, d := range filterDimensions {
		m[d] = true
	}
	return m
}()

// granularityDimension maps a contract granularity to the Plausible time
// dimension it groups by (spec/plausible/stats-api.md, "Time dimensions",
// fetched 2026-09-29).
var granularityDimension = map[string]string{
	wrapperprotocol.GranularityHour:  "time:hour",
	wrapperprotocol.GranularityDay:   "time:day",
	wrapperprotocol.GranularityWeek:  "time:week",
	wrapperprotocol.GranularityMonth: "time:month",
}

// declinedReasons is why every verb but probe, read_series and read_total is
// declined, reused both in the manifest and as the sentence a direct call to
// one of them is refused with.
var declinedReasons = map[wrapperprotocol.Verb]string{
	wrapperprotocol.VerbDescribeShape: "Plausible Analytics is a read-only data source; this wrapper answers only probe, read_series and read_total",
	wrapperprotocol.VerbLimits:        "Plausible Analytics is a read-only data source; this wrapper answers only probe, read_series and read_total",
	wrapperprotocol.VerbDraft:         "this wrapper is a read-only data source and never writes to Plausible",
	wrapperprotocol.VerbPublish:       "this wrapper is a read-only data source and never writes to Plausible",
	wrapperprotocol.VerbStatus:        "this wrapper is a read-only data source; there is nothing published by it to have a status",
	wrapperprotocol.VerbAppendUpdate:  "this wrapper is a read-only data source and never writes to Plausible",
	wrapperprotocol.VerbRetract:       "this wrapper is a read-only data source and never writes to Plausible",
	wrapperprotocol.VerbReadBack:      "this wrapper is a read-only data source; there is nothing published by it to read back",
	wrapperprotocol.VerbReadMetrics:   "Plausible Analytics is a read-only data source; this wrapper answers only probe, read_series and read_total",
	wrapperprotocol.VerbSetCap:        "this wrapper is a read-only data source and never writes to Plausible",
	wrapperprotocol.VerbListOwned:     "this wrapper is a read-only data source; there is nothing published by it to list",
	wrapperprotocol.VerbSearch:        "Plausible Analytics's Stats API answers aggregate metrics, not a searchable list of items",
}

// --- probe ---

func probe(c *client, conn wrapperprotocol.Connection) wrapperprotocol.VerbResult {
	if conn.AccountID == "" {
		return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbProbe, Failed: "the connection names no site (account_id)"}
	}
	if c.apiKey == "" {
		return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbProbe, Failed: fmt.Sprintf("the connection carries no %s credential", apiKeyCredential)}
	}
	// The cheapest call that proves the key can read this site: one metric,
	// today, no dimensions or filters.
	_, err := c.query(plausibleQuery{
		SiteID:    conn.AccountID,
		DateRange: "day",
		Metrics:   []string{"visitors"},
	})
	if err != nil {
		return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbProbe, Failed: fmt.Sprintf("probing Plausible: %v", err)}
	}
	return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbProbe, Manifest: buildManifest()}
}

func buildManifest() *wrapperprotocol.CapabilityManifest {
	verbs := map[wrapperprotocol.Verb]wrapperprotocol.VerbSupport{
		wrapperprotocol.VerbProbe:     {Level: wrapperprotocol.VerbSupported},
		wrapperprotocol.VerbReadTotal: {Level: wrapperprotocol.VerbSupported},
		wrapperprotocol.VerbReadSeries: {
			Level: wrapperprotocol.VerbPartial,
			Caveat: "Plausible labels each time bucket in the site's own Reporting Timezone, not UTC " +
				"(spec/plausible/stats-api.md, \"Time dimensions\", fetched 2026-09-29); this wrapper cannot resolve " +
				"that zone generically, so a series's \"at\" is that wall-clock label read as if it were UTC",
		},
	}
	for verb, reason := range declinedReasons {
		verbs[verb] = wrapperprotocol.VerbSupport{Level: wrapperprotocol.VerbDeclined, Reason: reason}
	}

	metrics := map[string]wrapperprotocol.MetricSupport{}
	for name, info := range metricCatalog {
		metrics[name] = wrapperprotocol.MetricSupport{
			Level:        wrapperprotocol.MetricAvailable,
			Aggregations: info.aggregations,
			Uniqueness:   info.uniqueness,
			Quality:      info.quality,
		}
	}
	for name, reason := range unavailableMetrics {
		metrics[name] = wrapperprotocol.MetricSupport{Level: wrapperprotocol.MetricUnavailable, Reason: reason}
	}

	granularities := make([]string, 0, len(granularityDimension))
	for g := range granularityDimension {
		granularities = append(granularities, g)
	}
	sort.Strings(granularities)

	dims := make([]string, len(filterDimensions))
	copy(dims, filterDimensions)
	sort.Strings(dims)

	return &wrapperprotocol.CapabilityManifest{
		Contract: wrapperprotocol.ContractVersion,
		Wrapper: wrapperprotocol.WrapperProvenance{
			Version:    wrapperVersion,
			SpecSource: specSource,
			SpecHash:   specHash,
		},
		Verbs:            verbs,
		Metrics:          metrics,
		Granularities:    granularities,
		FilterDimensions: dims,
		Venue:            "read_only",
		Idempotency:      "not applicable: a read-only data source performs no writes",
		Entitlements: map[string]any{
			"note": "the Stats API exposes no plan or rate-limit field on its own; plausible.io's documented " +
				"default is 600 requests/hour per key (spec/plausible/stats-api.md, \"Authentication\", " +
				"fetched 2026-09-29), which a self-hosted Community Edition instance need not enforce",
		},
		StoragePolicy: "Plausible's Stats API returns the connected account's own analytics for the sites its key " +
			"can query (spec/plausible/stats-api.md, \"Authentication\": \"A Stats API key can query sites owned " +
			"by the team it was created for\", fetched 2026-09-29); the docs state no separate licence over what a " +
			"caller may keep of what it reads, so this wrapper treats a reading as the account's own data, fine to " +
			"keep and show back to that same account.",
	}
}

// --- read_total ---

func readTotal(c *client, call wrapperprotocol.VerbCall) wrapperprotocol.VerbResult {
	if refusal := checkMeasureAndWindow(call); refusal != "" {
		return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbReadTotal, Refused: refusal}
	}
	info, ok := lookupMetric(*call.Measure)
	if ok != "" {
		return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbReadTotal, Refused: ok}
	}
	filters, refusal := buildFilters(call.Filter)
	if refusal != "" {
		return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbReadTotal, Refused: refusal}
	}

	q := plausibleQuery{
		SiteID:    "", // filled by caller below
		DateRange: dateRangeFor(*call.Window),
		Metrics:   []string{info.plausibleMetric},
		Filters:   filters,
	}
	q.SiteID = c.siteID

	rows, err := c.query(q)
	if err != nil {
		return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbReadTotal, Failed: fmt.Sprintf("reading total: %v", err)}
	}
	value := 0.0
	if len(rows) > 0 && len(rows[0].Metrics) > 0 {
		value = rows[0].Metrics[0]
	}
	reading := &wrapperprotocol.Reading{Value: value, Quality: info.quality}
	return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbReadTotal, Total: reading}
}

// --- read_series ---

func readSeries(c *client, call wrapperprotocol.VerbCall) wrapperprotocol.VerbResult {
	if refusal := checkMeasureAndWindow(call); refusal != "" {
		return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbReadSeries, Refused: refusal}
	}
	timeDim, ok := granularityDimension[call.Granularity]
	if !ok {
		return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbReadSeries,
			Refused: fmt.Sprintf("granularity %q is not one this wrapper serves (hour, day, week, month)", call.Granularity)}
	}
	info, refusal := lookupMetric(*call.Measure)
	if refusal != "" {
		return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbReadSeries, Refused: refusal}
	}
	filters, refusal := buildFilters(call.Filter)
	if refusal != "" {
		return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbReadSeries, Refused: refusal}
	}

	q := plausibleQuery{
		SiteID:     c.siteID,
		DateRange:  dateRangeFor(*call.Window),
		Metrics:    []string{info.plausibleMetric},
		Dimensions: []string{timeDim},
		Filters:    filters,
	}
	rows, err := c.query(q)
	if err != nil {
		return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbReadSeries, Failed: fmt.Sprintf("reading series: %v", err)}
	}

	layout := "2006-01-02"
	if call.Granularity == wrapperprotocol.GranularityHour {
		layout = "2006-01-02 15:04:05"
	}
	// Plausible answers only the buckets it has rows for, but the contract
	// wants one point per step, empty steps included (GUIDE.md, "Verbs in
	// contract 1"). Read what came back into a lookup keyed by the bucket's
	// own label, then walk every step the window covers, filling the gaps
	// with zero.
	values := make(map[string]float64, len(rows))
	for _, row := range rows {
		if len(row.Dimensions) == 0 || len(row.Metrics) == 0 {
			continue
		}
		if _, err := time.Parse(layout, row.Dimensions[0]); err != nil {
			return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbReadSeries,
				Failed: fmt.Sprintf("could not read time bucket %q: %v", row.Dimensions[0], err)}
		}
		values[row.Dimensions[0]] = row.Metrics[0]
	}

	steps := seriesSteps(call.Window.Start, call.Window.End, call.Granularity)
	series := make([]wrapperprotocol.SeriesPoint, 0, len(steps))
	for _, at := range steps {
		series = append(series, wrapperprotocol.SeriesPoint{At: at, Value: values[at.Format(layout)]})
	}
	return wrapperprotocol.VerbResult{Verb: wrapperprotocol.VerbReadSeries, Series: series}
}

// seriesSteps enumerates the start instant of every step in the half-open
// window [start, end), stepping by granularity, so read_series can answer one
// point per step regardless of which ones the tool had rows for.
func seriesSteps(start, end time.Time, granularity string) []time.Time {
	var steps []time.Time
	for t := start; t.Before(end); t = nextStep(t, granularity) {
		steps = append(steps, t)
	}
	return steps
}

// nextStep advances one step of granularity from t. Plausible groups
// time:week by calendar week start and time:month by calendar month start
// (spec/plausible/stats-api.md, "Time dimensions", fetched 2026-09-29), so a
// week steps by seven days and a month by one calendar month, not a fixed
// duration.
func nextStep(t time.Time, granularity string) time.Time {
	switch granularity {
	case wrapperprotocol.GranularityHour:
		return t.Add(time.Hour)
	case wrapperprotocol.GranularityWeek:
		return t.AddDate(0, 0, 7)
	case wrapperprotocol.GranularityMonth:
		return t.AddDate(0, 1, 0)
	default: // day
		return t.AddDate(0, 0, 1)
	}
}

// --- shared helpers ---

func checkMeasureAndWindow(call wrapperprotocol.VerbCall) string {
	if call.Measure == nil {
		return "no measure was given"
	}
	if call.Window == nil {
		return "no window was given"
	}
	return ""
}

// lookupMetric answers the metric a measure reads, or a refusal sentence.
func lookupMetric(m wrapperprotocol.Measure) (metricInfo, string) {
	if reason, unavailable := unavailableMetrics[m.Event]; unavailable {
		return metricInfo{}, fmt.Sprintf("metric %q is unavailable: %s", m.Event, reason)
	}
	info, ok := metricCatalog[m.Event]
	if !ok {
		return metricInfo{}, fmt.Sprintf("%q is not a metric this wrapper reads", m.Event)
	}
	for _, a := range info.aggregations {
		if a == m.Aggregation {
			return info, ""
		}
	}
	return metricInfo{}, fmt.Sprintf("metric %q cannot be read with aggregation %q; it offers %s",
		m.Event, m.Aggregation, strings.Join(info.aggregations, ", "))
}

// buildFilters turns the contract's one equality filter into a Stats API
// "is" filter, or a refusal if the dimension is not one this wrapper offers.
func buildFilters(f *wrapperprotocol.Filter) ([][]interface{}, string) {
	if f == nil {
		return nil, ""
	}
	if !filterDimensionSet[f.Dimension] {
		return nil, fmt.Sprintf("filter dimension %q is not one Plausible exposes to this wrapper", f.Dimension)
	}
	return [][]interface{}{{"is", f.Dimension, []string{f.Value}}}, ""
}

// dateRangeFor turns the contract's half-open window into a Stats API
// date_range: "all" when Start is zero ("the total to date", per the
// contract), else a custom ISO8601 instant pair. The end instant is nudged
// back one second, since the API's custom range is documented only by
// example and every example there is closed on both ends
// (spec/plausible/stats-api.md, "date_range", fetched 2026-09-29), while the
// contract's window is half-open.
func dateRangeFor(w wrapperprotocol.Window) interface{} {
	if w.Start.IsZero() {
		return "all"
	}
	end := w.End.Add(-time.Second)
	if !end.After(w.Start) {
		end = w.Start
	}
	return []string{w.Start.Format(time.RFC3339), end.Format(time.RFC3339)}
}
