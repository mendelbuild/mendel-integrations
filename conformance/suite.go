// Package conformance runs an External Tool Wrapper through the contract and
// reports what it did, check by check (doc 35 §8, "Conformance is the asset").
//
// It needs no database and no Mendel: a wrapper is JSON on stdin and stdout,
// so the suite is a sequence of requests and a judgement of each answer. What
// it checks, in the order it runs:
//
//  1. A request for another contract version is answered without acting.
//  2. probe answers a manifest the protocol accepts, as the version and
//     contract its wrapper.json names, and declines nothing wrapper.json claims.
//  3. Every verb the manifest declares absent is attempted and refused, so a
//     wrapper cannot decline lazily; a verb outside the contract is refused too.
//  4. Every verb the manifest declares honoured is exercised: for a data
//     source, every available metric at every aggregation, every declared
//     granularity, and the refusals the contract promises (an unavailable
//     metric, a granularity or a filter dimension not declared).
//  5. Where two answers must agree, they are compared: a count read as a
//     series over a window sums to the count read as a total over it.
//
// Each check says pass, fail, warn (true to the contract, and worth a
// reader's attention) or untested (the suite does not yet know how to
// exercise it, which is never a pass). Every call is timed and counted: the
// score §8 asks for, so two wrappers for one tool can be compared.
package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// Runner runs one request against the wrapper under test, and answers what
// it printed on stdout, checked, and on stderr.
type Runner func(ctx context.Context, req wp.WrapperRequest) (wp.WrapperResponse, string, error)

// Target is a wrapper and the venue account it is run against.
type Target struct {
	Description wp.Description
	Connection  wp.Connection
	Run         Runner
	// Revoke runs authorize's revoke at the end, which ends the venue's
	// grant: a later run needs a person to authorize again.
	Revoke bool
	// Expected is what the venue knows it holds, by "metric/aggregation",
	// over the last complete day before the run's instant: the truth a
	// wrapper's totals are held to. A venue that sent the traffic itself
	// knows it; without it the suite can check consistency, not truth.
	Expected map[string]float64
}

// Outcome is how one check came out.
type Outcome string

const (
	Pass     Outcome = "pass"
	Fail     Outcome = "fail"
	Warn     Outcome = "warn"
	Untested Outcome = "untested"
)

// Check is one thing the suite judged.
type Check struct {
	Name    string        `json:"name"`
	Verb    wp.Verb       `json:"verb,omitempty"`
	Outcome Outcome       `json:"outcome"`
	Detail  string        `json:"detail,omitempty"`
	Calls   int           `json:"calls"`
	Elapsed time.Duration `json:"elapsed_ns"`
}

// Report is the whole run.
type Report struct {
	Tool     string                 `json:"tool"`
	Version  string                 `json:"version"`
	At       time.Time              `json:"at"`
	Checks   []Check                `json:"checks"`
	Manifest *wp.CapabilityManifest `json:"manifest,omitempty"`
}

// Passed reports whether nothing failed and nothing went untested.
func (r Report) Passed() bool {
	for _, c := range r.Checks {
		if c.Outcome == Fail || c.Outcome == Untested {
			return false
		}
	}
	return true
}

// Count is how many checks came out each way.
func (r Report) Count() map[Outcome]int {
	out := map[Outcome]int{}
	for _, c := range r.Checks {
		out[c.Outcome]++
	}
	return out
}

// suite is one run in progress.
type suite struct {
	t      Target
	now    time.Time
	report Report
	// seen is everything the wrapper printed, each response with its
	// credentials taken out and each stderr in full, for the check that no
	// credential leaks into either.
	seen []string
}

// Run runs the suite. now fixes the windows it reads, so a run is repeatable.
func Run(ctx context.Context, t Target, now time.Time) Report {
	s := &suite{t: t, now: now.UTC(), report: Report{Tool: t.Description.Tool.Slug, Version: t.Description.Version, At: now.UTC()}}
	s.contractMismatch(ctx)
	m := s.probe(ctx)
	if m == nil {
		return s.report
	}
	s.report.Manifest = m
	s.declaredAbsent(ctx, m)
	s.unknownVerb(ctx)
	s.honoured(ctx, m)
	s.revokeAtEnd(ctx, m)
	s.credentialsStayPut()
	return s.report
}

func (s *suite) add(c Check) { s.report.Checks = append(s.report.Checks, c) }

func (s *suite) expects(key string) bool { _, ok := s.t.Expected[key]; return ok }

// call runs calls in one request and returns each call's result, or the
// sentence for why there is none.
func (s *suite) call(ctx context.Context, calls ...wp.VerbCall) ([]wp.VerbResult, time.Duration, error) {
	return s.callAs(ctx, s.t.Description.Contract, calls...)
}

func (s *suite) callAs(ctx context.Context, contract string, calls ...wp.VerbCall) ([]wp.VerbResult, time.Duration, error) {
	start := time.Now()
	resp, stderr, err := s.t.Run(ctx, wp.WrapperRequest{Contract: contract, Connection: s.t.Connection, Calls: calls})
	elapsed := time.Since(start)
	s.seen = append(s.seen, stderr)
	if err != nil {
		s.seen = append(s.seen, err.Error())
		return nil, elapsed, err
	}
	kept, _ := json.Marshal(resp.Redacted())
	s.seen = append(s.seen, string(kept))
	return resp.Results, elapsed, nil
}

// one runs a single call and returns its result.
func (s *suite) one(ctx context.Context, c wp.VerbCall) (wp.VerbResult, time.Duration, error) {
	res, elapsed, err := s.call(ctx, c)
	if err != nil {
		return wp.VerbResult{}, elapsed, err
	}
	return res[0], elapsed, nil
}

// --- 1. Another contract version ---

func (s *suite) contractMismatch(ctx context.Context) {
	c := Check{Name: "a request for another contract version is answered without acting", Verb: wp.VerbProbe, Calls: 1}
	res, elapsed, err := s.callAs(ctx, "0-conformance", wp.VerbCall{Verb: wp.VerbProbe})
	c.Elapsed = elapsed
	switch {
	case err != nil:
		c.Outcome, c.Detail = Fail, "the run did not produce an answer: "+err.Error()
	case res[0].Succeeded():
		c.Outcome, c.Detail = Fail, "the wrapper acted on a contract it was not written against"
	default:
		c.Outcome, c.Detail = Pass, res[0].Why()
	}
	s.add(c)
}

// --- 2. Probe ---

func (s *suite) probe(ctx context.Context) *wp.CapabilityManifest {
	d := s.t.Description
	c := Check{Name: "probe answers a manifest the protocol accepts", Verb: wp.VerbProbe, Calls: 1}
	res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: wp.VerbProbe})
	c.Elapsed = elapsed
	switch {
	case err != nil:
		c.Outcome, c.Detail = Fail, err.Error()
	case !res.Succeeded():
		c.Outcome, c.Detail = Fail, res.Why()
	case res.Manifest.CheckAs(d.Contract) != "":
		c.Outcome, c.Detail = Fail, res.Manifest.CheckAs(d.Contract)
	default:
		c.Outcome = Pass
	}
	s.add(c)
	if c.Outcome != Pass {
		return nil
	}
	m := res.Manifest

	who := Check{Name: "the manifest answers as the wrapper.json describes it", Verb: wp.VerbProbe}
	switch {
	case m.Wrapper.Version != d.Version:
		who.Outcome, who.Detail = Fail, fmt.Sprintf("the manifest says version %q; wrapper.json says %q", m.Wrapper.Version, d.Version)
	case m.Contract != d.Contract:
		who.Outcome, who.Detail = Fail, fmt.Sprintf("the manifest says contract %q; wrapper.json says %q", m.Contract, d.Contract)
	case d.SpecSource != "" && m.Wrapper.SpecSource != d.SpecSource:
		who.Outcome, who.Detail = Warn, fmt.Sprintf("the manifest cites %q as its spec; wrapper.json cites %q", m.Wrapper.SpecSource, d.SpecSource)
	default:
		who.Outcome = Pass
	}
	s.add(who)

	claims := Check{Name: "nothing wrapper.json claims is declined against this account", Verb: wp.VerbProbe}
	var over []string
	for _, verb := range sortedKeys(d.Claims) {
		if d.Claims[verb] == string(wp.VerbDeclined) {
			continue
		}
		if m.Verbs[wp.Verb(verb)].Level == wp.VerbDeclined {
			over = append(over, fmt.Sprintf("%s (claimed %s; declined: %s)", verb, d.Claims[verb], m.Verbs[wp.Verb(verb)].Reason))
		}
	}
	if len(over) > 0 {
		claims.Outcome, claims.Detail = Fail, "wrapper.json claims more than the probe shows: "+strings.Join(over, "; ")
	} else {
		claims.Outcome = Pass
	}
	s.add(claims)
	return m
}

// --- 3. Declared absent ---

func (s *suite) declaredAbsent(ctx context.Context, m *wp.CapabilityManifest) {
	for _, v := range wp.VerbsOf(s.t.Description.Contract) {
		if m.Verbs[v].Level != wp.VerbDeclined {
			continue
		}
		c := Check{Name: "a verb declared absent is refused", Verb: v, Calls: 1}
		res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: v})
		c.Elapsed = elapsed
		switch {
		case err != nil:
			c.Outcome, c.Detail = Fail, err.Error()
		case res.Succeeded():
			c.Outcome, c.Detail = Fail, "declared absent, and yet it answered"
		case res.Refused == "":
			c.Outcome, c.Detail = Fail, "declared absent, and it failed rather than refusing: "+res.Failed
		default:
			c.Outcome, c.Detail = Pass, res.Refused
		}
		s.add(c)
	}
}

func (s *suite) unknownVerb(ctx context.Context) {
	c := Check{Name: "a verb outside the contract is refused", Verb: "conformance_not_a_verb", Calls: 1}
	res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: "conformance_not_a_verb"})
	c.Elapsed = elapsed
	switch {
	case err != nil:
		c.Outcome, c.Detail = Fail, err.Error()
	case res.Refused == "":
		c.Outcome, c.Detail = Fail, "a verb the contract does not have was not refused"
	default:
		c.Outcome, c.Detail = Pass, res.Refused
	}
	s.add(c)
}

// --- 4 and 5. Honoured verbs ---

func (s *suite) honoured(ctx context.Context, m *wp.CapabilityManifest) {
	// The action surface is exercised as one lifecycle, which is how it is
	// judged: what was published is read back, counted and retracted.
	lifecycle := map[wp.Verb]bool{wp.VerbPublish: true, wp.VerbStatus: true, wp.VerbReadBack: true,
		wp.VerbReadMetrics: true, wp.VerbRetract: true}
	for _, v := range wp.VerbsOf(s.t.Description.Contract) {
		level := m.Verbs[v].Level
		if level == wp.VerbDeclined || v == wp.VerbProbe || (lifecycle[v] && v != wp.VerbPublish) {
			continue
		}
		switch v {
		case wp.VerbReadTotal:
			s.readTotals(ctx, m)
		case wp.VerbReadSeries:
			s.readSeries(ctx, m)
		case wp.VerbAuthorize:
			s.authorize(ctx, m)
		case wp.VerbPublish:
			s.publishLifecycle(ctx, m)
		case wp.VerbSearch:
			s.search(ctx, m)
		default:
			s.add(Check{Name: "an honoured verb is exercised", Verb: v, Outcome: Untested,
				Detail: fmt.Sprintf("declared %s; the suite cannot exercise %s yet", level, v)})
		}
	}
	if m.Verbs[wp.VerbReadTotal].Level != wp.VerbDeclined && m.Verbs[wp.VerbReadSeries].Level != wp.VerbDeclined {
		s.seriesAgreesWithTotal(ctx, m)
	}
}

// lastDay is the last complete UTC day before now.
func (s *suite) lastDay() *wp.Window {
	end := s.now.Truncate(24 * time.Hour)
	return &wp.Window{Start: end.Add(-24 * time.Hour), End: end}
}

func (s *suite) readTotals(ctx context.Context, m *wp.CapabilityManifest) {
	for _, nm := range wp.SortedMetrics(m.Metrics) {
		if nm.Support.Level == wp.MetricUnavailable {
			c := Check{Name: "an unavailable metric is refused, never read as zero", Verb: wp.VerbReadTotal, Calls: 1}
			res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: wp.VerbReadTotal,
				Measure: &wp.Measure{Event: nm.Name, Aggregation: "count"}, Window: s.lastDay()})
			c.Elapsed = elapsed
			switch {
			case err != nil:
				c.Outcome, c.Detail = Fail, err.Error()
			case res.Succeeded():
				c.Outcome, c.Detail = Fail, fmt.Sprintf("%s is declared unavailable, and yet read as %v", nm.Name, res.Total.Value)
			case res.Refused == "":
				c.Outcome, c.Detail = Fail, "it failed rather than refusing: "+res.Failed
			default:
				c.Outcome, c.Detail = Pass, nm.Name+": "+res.Refused
			}
			s.add(c)
			continue
		}
		for _, agg := range nm.Support.Aggregations {
			c := Check{Name: "an available metric is read as a total", Verb: wp.VerbReadTotal, Calls: 1}
			res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: wp.VerbReadTotal,
				Measure: &wp.Measure{Event: nm.Name, Aggregation: agg}, Window: s.lastDay()})
			c.Elapsed = elapsed
			switch {
			case err != nil:
				c.Outcome, c.Detail = Fail, err.Error()
			case !res.Succeeded():
				c.Outcome, c.Detail = Fail, fmt.Sprintf("%s/%s: %s", nm.Name, agg, res.Why())
			case res.Total == nil:
				c.Outcome, c.Detail = Fail, fmt.Sprintf("%s/%s answered no total", nm.Name, agg)
			case res.Total.Value < 0:
				c.Outcome, c.Detail = Fail, fmt.Sprintf("%s/%s read as %v; a count is never negative", nm.Name, agg, res.Total.Value)
			case !covers(res.Total.Quality, nm.Support.Quality):
				c.Outcome, c.Detail = Fail, fmt.Sprintf("%s/%s carries quality %v; the manifest says every read carries %v",
					nm.Name, agg, res.Total.Quality, nm.Support.Quality)
			case s.expects(nm.Name+"/"+agg) && res.Total.Value != s.t.Expected[nm.Name+"/"+agg]:
				c.Outcome, c.Detail = Fail, fmt.Sprintf("%s/%s over %s read %v; the venue holds %v", nm.Name, agg,
					day(s.lastDay()), res.Total.Value, s.t.Expected[nm.Name+"/"+agg])
			default:
				c.Outcome, c.Detail = Pass, fmt.Sprintf("%s/%s over %s: %v", nm.Name, agg, day(s.lastDay()), res.Total.Value)
				if s.expects(nm.Name + "/" + agg) {
					c.Detail += ", as the venue holds"
				}
			}
			s.add(c)
		}
	}
	s.refusesUndeclaredFilter(ctx, m, wp.VerbReadTotal)
}

// steps is how many points a series over w at g must have, or 0 when g does
// not divide w into whole steps from its start (month is calendar-shaped).
func steps(w *wp.Window, g string) int {
	switch g {
	case wp.GranularityHour:
		return int(w.End.Sub(w.Start) / time.Hour)
	case wp.GranularityDay:
		return int(w.End.Sub(w.Start) / (24 * time.Hour))
	case wp.GranularityWeek:
		return int(w.End.Sub(w.Start) / (7 * 24 * time.Hour))
	}
	return 0
}

// seriesWindow is a window a granularity divides into a handful of steps.
func (s *suite) seriesWindow(g string) *wp.Window {
	day := s.now.Truncate(24 * time.Hour)
	switch g {
	case wp.GranularityHour:
		return &wp.Window{Start: day.Add(-24 * time.Hour), End: day}
	case wp.GranularityWeek:
		// Four whole weeks ending on the last Monday, the ISO week's start.
		monday := day.Add(-time.Duration((int(day.Weekday())+6)%7) * 24 * time.Hour)
		return &wp.Window{Start: monday.Add(-28 * 24 * time.Hour), End: monday}
	case wp.GranularityMonth:
		first := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
		return &wp.Window{Start: first.AddDate(0, -3, 0), End: first}
	}
	return &wp.Window{Start: day.Add(-7 * 24 * time.Hour), End: day}
}

// firstCountable is the first available metric readable as a count, or "".
func firstCountable(m *wp.CapabilityManifest) string {
	for _, nm := range wp.SortedMetrics(m.Metrics) {
		if nm.Support.Level != wp.MetricAvailable {
			continue
		}
		for _, a := range nm.Support.Aggregations {
			if a == "count" {
				return nm.Name
			}
		}
	}
	return ""
}

func (s *suite) readSeries(ctx context.Context, m *wp.CapabilityManifest) {
	metric := firstCountable(m)
	if metric == "" {
		s.add(Check{Name: "a series is read at each declared granularity", Verb: wp.VerbReadSeries, Outcome: Untested,
			Detail: "no available metric can be read as a count"})
		return
	}
	for _, g := range m.Granularities {
		c := Check{Name: "a series is read at a declared granularity", Verb: wp.VerbReadSeries, Calls: 1}
		w := s.seriesWindow(g)
		res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: wp.VerbReadSeries,
			Measure: &wp.Measure{Event: metric, Aggregation: "count"}, Window: w, Granularity: g})
		c.Elapsed = elapsed
		if err != nil {
			c.Outcome, c.Detail = Fail, err.Error()
		} else if !res.Succeeded() {
			c.Outcome, c.Detail = Fail, fmt.Sprintf("%s by %s: %s", metric, g, res.Why())
		} else {
			c.Outcome, c.Detail = judgeSeries(res.Series, w, g)
			if c.Outcome == Pass {
				c.Detail = fmt.Sprintf("%s by %s: %d points", metric, g, len(res.Series))
			}
		}
		s.add(c)
	}

	declared := map[string]bool{}
	for _, g := range m.Granularities {
		declared[g] = true
	}
	for _, g := range []string{wp.GranularityHour, wp.GranularityDay, wp.GranularityWeek, wp.GranularityMonth, "fortnight"} {
		if declared[g] {
			continue
		}
		c := Check{Name: "a granularity not declared is refused, never substituted", Verb: wp.VerbReadSeries, Calls: 1}
		res, elapsed, err := s.one(ctx, wp.VerbCall{Verb: wp.VerbReadSeries,
			Measure: &wp.Measure{Event: metric, Aggregation: "count"}, Window: s.seriesWindow(wp.GranularityDay), Granularity: g})
		c.Elapsed = elapsed
		switch {
		case err != nil:
			c.Outcome, c.Detail = Fail, err.Error()
		case res.Refused == "":
			c.Outcome, c.Detail = Fail, fmt.Sprintf("%q was not declared, and yet was %s", g, map[bool]string{true: "answered", false: "failed: " + res.Failed}[res.Succeeded()])
		default:
			c.Outcome, c.Detail = Pass, fmt.Sprintf("%s: %s", g, res.Refused)
		}
		s.add(c)
	}
	s.refusesUndeclaredFilter(ctx, m, wp.VerbReadSeries)
}

// judgeSeries checks a series has one point per step, in order, inside the
// window.
func judgeSeries(series []wp.SeriesPoint, w *wp.Window, g string) (Outcome, string) {
	for i, p := range series {
		if p.At.Before(w.Start) || !p.At.Before(w.End) {
			return Fail, fmt.Sprintf("point %d at %s is outside [%s, %s)", i, p.At.Format(time.RFC3339),
				w.Start.Format(time.RFC3339), w.End.Format(time.RFC3339))
		}
		if i > 0 && !p.At.After(series[i-1].At) {
			return Fail, fmt.Sprintf("point %d is not after point %d", i, i-1)
		}
		if p.Value < 0 {
			return Fail, fmt.Sprintf("point %d is negative", i)
		}
	}
	if want := steps(w, g); want > 0 && len(series) != want {
		return Fail, fmt.Sprintf("%d points for %d steps of a %s; the contract is one point per step, empty steps included",
			len(series), want, g)
	}
	return Pass, ""
}

func (s *suite) refusesUndeclaredFilter(ctx context.Context, m *wp.CapabilityManifest, v wp.Verb) {
	metric := firstCountable(m)
	if metric == "" {
		return
	}
	c := Check{Name: "a filter on a dimension not declared is refused", Verb: v, Calls: 1}
	call := wp.VerbCall{Verb: v, Measure: &wp.Measure{Event: metric, Aggregation: "count"},
		Window: s.lastDay(), Filter: &wp.Filter{Dimension: "conformance:not_a_dimension", Value: "x"}}
	if v == wp.VerbReadSeries {
		call.Granularity = m.Granularities[0]
		call.Window = s.seriesWindow(call.Granularity)
	}
	res, elapsed, err := s.one(ctx, call)
	c.Elapsed = elapsed
	switch {
	case err != nil:
		c.Outcome, c.Detail = Fail, err.Error()
	case res.Refused == "":
		c.Outcome, c.Detail = Fail, "an undeclared dimension was not refused"
	default:
		c.Outcome, c.Detail = Pass, res.Refused
	}
	s.add(c)
}

// seriesAgreesWithTotal reads one count both ways over the same days. A
// wrapper that buckets in a zone other than UTC may legitimately disagree at
// the edges, which it says by declaring read_series partial; then a mismatch
// is a warning, not a failure.
func (s *suite) seriesAgreesWithTotal(ctx context.Context, m *wp.CapabilityManifest) {
	metric := firstCountable(m)
	declaredDay := false
	for _, g := range m.Granularities {
		declaredDay = declaredDay || g == wp.GranularityDay
	}
	if metric == "" || !declaredDay {
		return
	}
	w := s.seriesWindow(wp.GranularityDay)
	measure := &wp.Measure{Event: metric, Aggregation: "count"}
	c := Check{Name: "a count read as a series sums to the count read as a total", Verb: wp.VerbReadSeries, Calls: 1}
	res, elapsed, err := s.call(ctx,
		wp.VerbCall{Verb: wp.VerbReadSeries, Measure: measure, Window: w, Granularity: wp.GranularityDay},
		wp.VerbCall{Verb: wp.VerbReadTotal, Measure: measure, Window: w})
	c.Elapsed = elapsed
	if err != nil || len(res) < 2 || !res[1].Succeeded() {
		why := "the reads did not both succeed"
		if err != nil {
			why = err.Error()
		} else if len(res) > 0 {
			why = res[len(res)-1].Why()
		}
		c.Outcome, c.Detail = Fail, why
		s.add(c)
		return
	}
	var sum float64
	for _, p := range res[0].Series {
		sum += p.Value
	}
	total := res[1].Total.Value
	switch {
	case sum == total:
		c.Outcome, c.Detail = Pass, fmt.Sprintf("%s over %s: %v both ways", metric, span(w), total)
	case m.Verbs[wp.VerbReadSeries].Level == wp.VerbPartial:
		c.Outcome, c.Detail = Warn, fmt.Sprintf("%s over %s: the series sums to %v and the total is %v; read_series is "+
			"declared partial (%s)", metric, span(w), sum, total, m.Verbs[wp.VerbReadSeries].Caveat)
	default:
		c.Outcome, c.Detail = Fail, fmt.Sprintf("%s over %s: the series sums to %v and the total is %v", metric, span(w), sum, total)
	}
	s.add(c)
}

// --- helpers ---

func covers(have, want []string) bool {
	set := map[string]bool{}
	for _, q := range have {
		set[q] = true
	}
	for _, q := range want {
		if !set[q] {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func day(w *wp.Window) string { return w.Start.Format("2006-01-02") }

func span(w *wp.Window) string {
	return w.Start.Format("2006-01-02") + ".." + w.End.Add(-time.Nanosecond).Format("2006-01-02")
}
