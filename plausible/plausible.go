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
	"strings"
	"time"
)

// Everything in this file is written against the Stats API v2 as README.md
// cites it: POST /api/v2/query, a Bearer key, site_id, metrics, date_range,
// dimensions, filters. Nothing here is from memory of the API.

// specSource is the page the query endpoint was written against.
const specSource = "https://plausible.io/docs/stats-api"

// credentialName is the name the connection carries the Stats API key under.
const credentialName = "PLAUSIBLE_API_KEY"

// defaultEndpoint is Plausible's hosted service. A self-hosted instance
// arrives as the connection's endpoint.
const defaultEndpoint = "https://plausible.io"

// readme is the record of the cited spec. Its hash is the wrapper's spec hash,
// so a change to what was cited is a change of provenance.
//
//go:embed README.md
var readme []byte

func specHash() string {
	sum := sha256.Sum256(readme)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// countMetrics are the Stats API metrics that are counts of events, read
// with aggregation count.
var countMetrics = map[string]bool{"pageviews": true, "visits": true, "events": true}

// visitorsUniqueness is what "unique" means for visitors, in Plausible's own
// words (https://plausible.io/docs/metrics-definitions, fetched 2026-09-24).
const visitorsUniqueness = "Plausible counts a person once per day and site, by a hash of " +
	"a daily-rotated salt, the domain, the IP address and the user agent; no cookie. A person " +
	"visiting on two days, or from two devices, is two visitors, so unique visitors over a " +
	"week or month counts each person once per day they came, not once. " +
	"(plausible.io/docs/metrics-definitions, plausible.io/data-policy)"

// filterDimensions are the dimensions an equality filter may name, from the
// Stats API's dimension list. event:props:<name> is allowed by prefix.
var filterDimensions = []string{
	"event:page", "event:hostname", "event:props:<name>",
	"visit:entry_page", "visit:exit_page", "visit:source", "visit:referrer", "visit:channel",
	"visit:utm_medium", "visit:utm_source", "visit:utm_campaign", "visit:utm_content", "visit:utm_term",
	"visit:device", "visit:browser", "visit:os", "visit:country", "visit:region", "visit:city",
	"visit:country_name", "visit:region_name", "visit:city_name",
}

func filterable(dimension string) bool {
	if strings.HasPrefix(dimension, "event:props:") && len(dimension) > len("event:props:") {
		return true
	}
	for _, d := range filterDimensions {
		if d == dimension && d != "event:props:<name>" {
			return true
		}
	}
	return false
}

// granularities are the time dimensions the API groups by. Anything else is
// refused, never substituted.
var granularities = map[string]string{
	"hour": "time:hour", "day": "time:day", "week": "time:week", "month": "time:month",
}

type api struct {
	endpoint, key, site string
	client              *http.Client
}

func newAPI(c connection, client *http.Client) *api {
	endpoint := strings.TrimRight(c.Endpoint, "/")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return &api{endpoint: endpoint, key: c.Credentials[credentialName], site: c.AccountID, client: client}
}

// query is the Stats API v2 request body.
type query struct {
	SiteID     string          `json:"site_id"`
	Metrics    []string        `json:"metrics"`
	DateRange  any             `json:"date_range"`
	Dimensions []string        `json:"dimensions,omitempty"`
	Filters    []any           `json:"filters,omitempty"`
	Include    map[string]bool `json:"include,omitempty"`
}

type queryResult struct {
	Results []struct {
		Metrics    []float64 `json:"metrics"`
		Dimensions []string  `json:"dimensions"`
	} `json:"results"`
	Meta struct {
		TimeLabels []string `json:"time_labels"`
	} `json:"meta"`
}

// post sends one query. refused is Plausible's own sentence for a query it
// would not answer (400); err is anything else that went wrong.
func (a *api) post(ctx context.Context, q query) (res queryResult, refused string, err error) {
	if a.key == "" {
		return res, "", fmt.Errorf("the connection carries no %s", credentialName)
	}
	if a.site == "" {
		return res, "", fmt.Errorf("the connection names no site (account_id)")
	}
	q.SiteID = a.site
	body, _ := json.Marshal(q)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/api/v2/query", bytes.NewReader(body))
	if err != nil {
		return res, "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return res, "", fmt.Errorf("Plausible did not answer: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusOK:
		if err := json.Unmarshal(raw, &res); err != nil {
			return res, "", fmt.Errorf("Plausible's answer is not readable: %v", err)
		}
		return res, "", nil
	case resp.StatusCode == http.StatusBadRequest:
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(raw))
		}
		return res, "Plausible refused the query: " + e.Error, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return res, "", fmt.Errorf("Plausible refused the API key for site %s (%d); it needs a Stats API key "+
			"of the team that owns the site", a.site, resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound:
		return res, "", fmt.Errorf("Plausible has no site %s this key can read (404)", a.site)
	case resp.StatusCode == http.StatusTooManyRequests:
		return res, "", fmt.Errorf("Plausible is rate limiting this key (429); the documented default is " +
			"600 requests an hour")
	}
	return res, "", fmt.Errorf("Plausible answered %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
}

// probe confirms the key can read the site with the cheapest query there is,
// then answers the manifest. What it observed is the access; the rate limit
// is the documented default and says so.
func (a *api) probe(ctx context.Context) result {
	_, refused, err := a.post(ctx, query{Metrics: []string{"visitors"}, DateRange: "day"})
	if err != nil {
		return result{Verb: "probe", Failed: err.Error()}
	}
	if refused != "" {
		return result{Verb: "probe", Failed: refused}
	}
	return result{Verb: "probe", Manifest: &manifest{
		Contract:         contractVersion,
		Wrapper:          provenance{Version: wrapperVersion, SpecSource: specSource, SpecHash: specHash()},
		Verbs:            verbs(),
		Metrics:          metrics(),
		Granularities:    []string{"hour", "day", "week", "month"},
		FilterDimensions: filterDimensions,
		Venue:            "read_only",
		Idempotency:      "not applicable: every verb this wrapper honours is a read",
		Entitlements: map[string]any{
			"site":                           a.site,
			"site_readable_with_key":         true,
			"documented_rate_limit_per_hour": 600,
		},
		StoragePolicy: "Plausible's terms say the customer retains full ownership and control of the " +
			"site's data, and place no restriction on keeping or showing statistics read through the " +
			"API; stats are deleted when the account is, or when a subscription lapses. Nothing read " +
			"is personal: visitors are not identifiable across days. (plausible.io/terms, " +
			"plausible.io/data-policy, fetched 2026-09-24)",
	}}
}

func verbs() map[string]verbSupport {
	declined := func(reason string) verbSupport { return verbSupport{Level: "declined", Reason: reason} }
	readOnly := declined("Plausible is a data source: this wrapper reads and never writes.")
	return map[string]verbSupport{
		"probe":          {Level: "supported"},
		"authorize":      declined("Plausible is connected with a Stats API key a person types."),
		"draft":          readOnly,
		"publish":        readOnly,
		"status":         declined("There is no placed asset to report the status of."),
		"append_update":  readOnly,
		"retract":        readOnly,
		"read_back":      declined("There is no placed asset to read back."),
		"read_metrics":   declined("Metrics are per asset; a data source is read with read_series and read_total."),
		"set_cap":        readOnly,
		"list_owned":     declined("Mendel owns nothing in Plausible."),
		"read_series": {Level: "partial", Caveat: "Buckets are hours, days, weeks and months in the site's " +
			"reporting timezone, not UTC, and a week begins where Plausible begins it. Counts only: a rate or an " +
			"average has no value in a step nobody visited, so it is read a window at a time with read_total."},
		"read_total": {Level: "supported"},
		"search":     declined("Plausible holds no items to search."),
	}
}

func metrics() map[string]metricSupport {
	// Counts are preferred: a rate or an average is offered only where
	// Plausible gives no counts to take the ratio of, since Mendel divides
	// two counts itself (contract 2's rule for a metric's kind).
	divide := func(what, of string) metricSupport {
		return metricSupport{Level: "unavailable", Aggregations: []string{},
			Reason: what + " is " + of + ", both of which are read as counts here: read them and divide."}
	}
	notYet := func(what string) metricSupport {
		return metricSupport{Level: "unavailable", Aggregations: []string{},
			Reason: what + " is an average this version does not read."}
	}
	return map[string]metricSupport{
		"pageviews": {Level: "available", Kind: "count", Aggregations: []string{"count"}},
		"visits":    {Level: "available", Kind: "count", Aggregations: []string{"count"}},
		// Any other event name is read as a goal configured on the site
		// (plan, below). The contract has no way to declare an open family
		// of metrics, so that rule is in README.md and not in the manifest.
		"events":   {Level: "available", Kind: "count", Aggregations: []string{"count"}},
		"visitors": {Level: "available", Kind: "people", Aggregations: []string{"unique"}, Uniqueness: visitorsUniqueness},
		// Plausible reports no count of bounces and no total of time, so
		// these two are only to be had as the rate and the average it
		// computes over the window asked for.
		"bounce_rate":     {Level: "available", Kind: "rate", Per: "visit", Aggregations: []string{"value"}},
		"visit_duration":  {Level: "available", Kind: "average", Per: "visit", Unit: "seconds", Aggregations: []string{"value"}},
		"views_per_visit": divide("views_per_visit", "pageviews divided by visits"),
		"conversion_rate": divide("conversion_rate", "a goal's unique visitors divided by visitors"),
		"scroll_depth":    notYet("scroll_depth"),
		"time_on_page":    notYet("time_on_page"),
		"total_revenue": {Level: "unavailable", Aggregations: []string{},
			Reason: "Revenue goals are not read yet: a sum of money needs its currency declared, per goal."},
	}
}

// plan turns a measure and an optional filter into the query's metric and
// filters, or the sentence refusing it.
func plan(m *measure, f *filter) (metric string, filters []any, refused string) {
	if m == nil || m.Event == "" {
		return "", nil, "the call names no measure"
	}
	switch {
	case countMetrics[m.Event]:
		if m.Aggregation != "count" {
			return "", nil, fmt.Sprintf("%s is read with count, not %s", m.Event, m.Aggregation)
		}
		metric = m.Event
	case m.Event == "visitors":
		if m.Aggregation != "unique" {
			return "", nil, "visitors is read with unique: it is already a count of people, per the manifest"
		}
		metric = "visitors"
	case metrics()[m.Event].Kind == "rate" || metrics()[m.Event].Kind == "average":
		if m.Aggregation != "value" {
			return "", nil, fmt.Sprintf("%s is a %s and is read as its value, not %s: it does not add up",
				m.Event, metrics()[m.Event].Kind, m.Aggregation)
		}
		metric = m.Event
	case metrics()[m.Event].Level == "unavailable":
		return "", nil, fmt.Sprintf("%s is unavailable: %s", m.Event, metrics()[m.Event].Reason)
	default:
		// A goal configured on the site, by its name.
		switch m.Aggregation {
		case "count":
			metric = "events"
		case "unique":
			metric = "visitors"
		default:
			return "", nil, fmt.Sprintf("a goal is read with count or unique, not %s", m.Aggregation)
		}
		filters = append(filters, []any{"is", "event:goal", []string{m.Event}})
	}
	if f != nil {
		if !filterable(f.Dimension) {
			return "", nil, fmt.Sprintf("%s is not a dimension this wrapper filters on", f.Dimension)
		}
		filters = append(filters, []any{"is", f.Dimension, []string{f.Value}})
	}
	return metric, filters, ""
}

// dateRange is a window as the API takes it: two timestamps with an offset.
// The API's range is inclusive and the contract's is half-open, so the end is
// the last second before the window ends.
func dateRange(w *window) []string {
	return []string{w.Start.UTC().Format(time.RFC3339), w.End.Add(-time.Second).UTC().Format(time.RFC3339)}
}

func (a *api) readTotal(ctx context.Context, c call) result {
	const verb = "read_total"
	metric, filters, refused := plan(c.Measure, c.Filter)
	if refused != "" {
		return result{Verb: verb, Refused: refused}
	}
	if c.Window == nil || !c.Window.End.After(c.Window.Start) {
		return result{Verb: verb, Refused: "the call needs a window whose end is after its start"}
	}
	// A window with no start is the total to date: Plausible's "all".
	var dr any = "all"
	if !c.Window.Start.IsZero() {
		dr = dateRange(c.Window)
	}
	// A rate or an average is asked with the visits it is per, because over
	// a window nobody visited it has no value, and Plausible answers 0.
	perVisit := isPerVisit(metric)
	asked := []string{metric}
	if perVisit {
		asked = append(asked, "visits")
	}
	res, refused, err := a.post(ctx, query{Metrics: asked, DateRange: dr, Filters: filters})
	if err != nil {
		return result{Verb: verb, Failed: err.Error()}
	}
	if refused != "" {
		return result{Verb: verb, Refused: refused}
	}
	if len(res.Results) != 1 || len(res.Results[0].Metrics) != len(asked) {
		return result{Verb: verb, Failed: fmt.Sprintf("an aggregate query answered %d rows, not one", len(res.Results))}
	}
	if perVisit && res.Results[0].Metrics[1] == 0 {
		return result{Verb: verb, Refused: fmt.Sprintf("nobody visited in the window, so there is no %s: a %s over "+
			"no visits has no value, and is not zero", metric, metrics()[metric].Kind)}
	}
	return result{Verb: verb, Total: &reading{Value: res.Results[0].Metrics[0]}}
}

func (a *api) readSeries(ctx context.Context, c call) result {
	const verb = "read_series"
	dimension, ok := granularities[c.Granularity]
	if !ok {
		return result{Verb: verb, Refused: fmt.Sprintf("Plausible groups by hour, day, week or month, not %q; "+
			"this wrapper refuses rather than substitute", c.Granularity)}
	}
	metric, filters, refused := plan(c.Measure, c.Filter)
	if refused != "" {
		return result{Verb: verb, Refused: refused}
	}
	if isPerVisit(metric) {
		return result{Verb: verb, Refused: fmt.Sprintf("%s is a %s, which has no value in a step nobody visited; "+
			"read it a window at a time with read_total", metric, metrics()[metric].Kind)}
	}
	if c.Window == nil || c.Window.Start.IsZero() || !c.Window.End.After(c.Window.Start) {
		return result{Verb: verb, Refused: "the call needs a window with a start, and an end after it"}
	}
	res, refused, err := a.post(ctx, query{Metrics: []string{metric}, DateRange: dateRange(c.Window),
		Dimensions: []string{dimension}, Filters: filters, Include: map[string]bool{"time_labels": true}})
	if err != nil {
		return result{Verb: verb, Failed: err.Error()}
	}
	if refused != "" {
		return result{Verb: verb, Refused: refused}
	}
	values := map[string]float64{}
	for _, row := range res.Results {
		if len(row.Dimensions) != 1 || len(row.Metrics) != 1 {
			return result{Verb: verb, Failed: "a time-grouped row did not carry one label and one value"}
		}
		values[row.Dimensions[0]] = row.Metrics[0]
	}
	// Every labelled bucket, with the ones Plausible returned no row for as
	// zero: the label says the bucket was in range, so its absence is a
	// count of nothing rather than an unknown.
	labels := res.Meta.TimeLabels
	if len(labels) == 0 {
		for label := range values {
			labels = append(labels, label)
		}
	}
	series := make([]seriesPoint, 0, len(labels))
	for _, label := range labels {
		at, err := parseLabel(label)
		if err != nil {
			return result{Verb: verb, Failed: err.Error()}
		}
		series = append(series, seriesPoint{At: at, Value: values[label]})
	}
	sortSeries(series)
	return result{Verb: verb, Series: series}
}

// parseLabel reads a time bucket's label. The label is a wall-clock reading in
// the site's reporting timezone, which the API does not name; it is returned
// as that reading in UTC, which read_series' caveat in the manifest discloses.
func parseLabel(label string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, label); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("the time label %q is not one this wrapper can read", label)
}

func sortSeries(s []seriesPoint) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].At.Before(s[j-1].At); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// isPerVisit reports whether a metric is a rate or an average per visit.
func isPerVisit(metric string) bool {
	k := metrics()[metric].Kind
	return (k == "rate" || k == "average") && metrics()[metric].Per == "visit"
}
