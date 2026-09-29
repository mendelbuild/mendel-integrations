package conformance

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// fake is a small data source that keeps the contract, with switches that
// each break one rule. The suite is only as good as the mutants it catches
// (doc 35 §8: the suite is mutation-tested).
type fake struct {
	actsOnOtherContract bool // answers a probe for a contract it does not speak
	versionSays         string
	declinesClaimed     bool // declines read_total at probe though wrapper.json claims it
	answersDeclined     bool // answers publish though it declares it absent
	failsDeclined       bool // fails, rather than refuses, a verb it declares absent
	zeroForUnavailable  bool // reads an unavailable metric as zero
	substitutes         bool // serves day for a granularity it did not declare
	dropsEmptySteps     bool // leaves empty days out of a series
	ignoresFilter       bool // answers a filter on a dimension it lacks
	seriesDisagrees     bool // a series that does not sum to the total
	partialSeries       bool // declares read_series partial
	leaksToStderr       bool // prints its key on stderr
	doubles             bool // reads every count double, consistently
}

// perDay is the fake's traffic: n pageviews on the nth day of the month, and
// none on Sundays, so a series has empty steps.
func perDay(t time.Time) float64 {
	if t.Weekday() == time.Sunday {
		return 0
	}
	return float64(t.Day())
}

func (f fake) manifest() *wp.CapabilityManifest {
	m := &wp.CapabilityManifest{
		Contract: wp.ContractVersion,
		Wrapper:  wp.WrapperProvenance{Version: "1.0", SpecSource: "https://acme.example/api", SpecHash: "sha256:x"},
		Verbs:    map[wp.Verb]wp.VerbSupport{},
		Metrics: map[string]wp.MetricSupport{
			"pageviews":   {Level: wp.MetricAvailable, Aggregations: []string{"count"}},
			"bounce_rate": {Level: wp.MetricUnavailable, Reason: "a rate, not a count"},
		},
		Granularities:    []string{wp.GranularityDay},
		FilterDimensions: []string{"page"},
		Venue:            "read_only", Idempotency: "none", Entitlements: map[string]any{},
		StoragePolicy: "The customer owns what is read.",
	}
	if f.versionSays != "" {
		m.Wrapper.Version = f.versionSays
	}
	for _, v := range wp.ContractVerbs() {
		m.Verbs[v] = wp.VerbSupport{Level: wp.VerbDeclined, Reason: "a data source"}
	}
	for _, v := range []wp.Verb{wp.VerbProbe, wp.VerbReadSeries, wp.VerbReadTotal} {
		m.Verbs[v] = wp.VerbSupport{Level: wp.VerbSupported}
	}
	if f.partialSeries {
		m.Verbs[wp.VerbReadSeries] = wp.VerbSupport{Level: wp.VerbPartial, Caveat: "days are the site's, not UTC"}
	}
	if f.declinesClaimed {
		m.Verbs[wp.VerbReadTotal] = wp.VerbSupport{Level: wp.VerbDeclined, Reason: "not on this plan"}
	}
	return m
}

func (f fake) run(ctx context.Context, req wp.WrapperRequest) (wp.WrapperResponse, string, error) {
	resp, err := f.respond(ctx, req)
	stderr := ""
	if f.leaksToStderr {
		stderr = "debug: calling with key " + req.Connection.Credentials["ACME_KEY"]
	}
	return resp, stderr, err
}

func (f fake) respond(_ context.Context, req wp.WrapperRequest) (wp.WrapperResponse, error) {
	var out wp.WrapperResponse
	if req.Contract != wp.ContractVersion && !f.actsOnOtherContract {
		out.Results = append(out.Results, wp.VerbResult{Verb: req.Calls[0].Verb, Refused: "this wrapper speaks contract 1"})
		return out, nil
	}
	m := f.manifest()
	for _, c := range req.Calls {
		r := f.answer(m, c)
		out.Results = append(out.Results, r)
		if !r.Succeeded() {
			break
		}
	}
	return out, nil
}

func (f fake) answer(m *wp.CapabilityManifest, c wp.VerbCall) wp.VerbResult {
	r := wp.VerbResult{Verb: c.Verb}
	support, known := m.Verbs[c.Verb]
	switch {
	case !known:
		r.Refused = "not a verb of the contract"
		return r
	case c.Verb == wp.VerbPublish && f.answersDeclined:
		return r
	case support.Level == wp.VerbDeclined && f.failsDeclined:
		r.Failed = "not implemented"
		return r
	case support.Level == wp.VerbDeclined:
		r.Refused = "declared absent"
		return r
	case c.Verb == wp.VerbProbe:
		r.Manifest = m
		return r
	}
	if c.Measure.Event == "bounce_rate" {
		if f.zeroForUnavailable {
			r.Total = &wp.Reading{}
			return r
		}
		r.Refused = "bounce_rate is a rate"
		return r
	}
	if c.Filter != nil && c.Filter.Dimension != "page" && !f.ignoresFilter {
		r.Refused = "no dimension " + c.Filter.Dimension
		return r
	}
	switch c.Verb {
	case wp.VerbReadTotal:
		var sum float64
		for t := c.Window.Start; t.Before(c.Window.End); t = t.Add(24 * time.Hour) {
			sum += perDay(t)
		}
		if f.doubles {
			sum *= 2
		}
		r.Total = &wp.Reading{Value: sum}
	case wp.VerbReadSeries:
		if c.Granularity != wp.GranularityDay && !f.substitutes {
			r.Refused = "by day only"
			return r
		}
		for t := c.Window.Start; t.Before(c.Window.End); t = t.Add(24 * time.Hour) {
			v := perDay(t)
			if v == 0 && f.dropsEmptySteps {
				continue
			}
			if f.seriesDisagrees {
				v++
			}
			if f.doubles {
				v *= 2
			}
			r.Series = append(r.Series, wp.SeriesPoint{At: t, Value: v})
		}
	}
	return r
}

func description() wp.Description {
	return wp.Description{
		Tool: wp.Tool{Slug: "acme", Name: "Acme"}, Version: "1.0", Contract: wp.ContractVersion,
		Image: "acme:dev", Command: []string{"/acme"}, SpecSource: "https://acme.example/api",
		Connection: wp.ConnectionSpec{Credentials: []wp.Field{{Name: "ACME_KEY", Label: "Key"}}},
		Claims:     map[string]string{"probe": "supported", "read_total": "supported", "read_series": "supported"},
	}
}

var now = time.Date(2026, 9, 24, 10, 30, 0, 0, time.UTC) // a Thursday

func runFake(f fake) Report {
	return Run(context.Background(), Target{Description: description(), Run: f.run,
		Connection: wp.Connection{AccountID: "acme.example", Credentials: map[string]string{"ACME_KEY": "acme-key-SECRET"}}}, now)
}

func TestAWrapperThatKeepsTheContractPasses(t *testing.T) {
	r := runFake(fake{})
	for _, c := range r.Checks {
		if c.Outcome != Pass {
			t.Errorf("%s (%s): %s: %s", c.Name, c.Verb, c.Outcome, c.Detail)
		}
	}
	if !r.Passed() || len(r.Checks) < 20 {
		t.Errorf("passed %v with %d checks", r.Passed(), len(r.Checks))
	}
}

// Every mutant fails, and fails the check that is about what it broke.
func TestEveryMutantIsCaught(t *testing.T) {
	for name, tc := range map[string]struct {
		f     fake
		check string
	}{
		"acts on another contract":  {fake{actsOnOtherContract: true}, "another contract version"},
		"answers as another version": {fake{versionSays: "0.9"}, "answers as the wrapper.json"},
		"declines what it claims":   {fake{declinesClaimed: true}, "nothing wrapper.json claims"},
		"answers a declined verb":   {fake{answersDeclined: true}, "declared absent is refused"},
		"fails a declined verb":     {fake{failsDeclined: true}, "declared absent is refused"},
		"zero for unavailable":      {fake{zeroForUnavailable: true}, "unavailable metric is refused"},
		"substitutes a granularity": {fake{substitutes: true}, "granularity not declared"},
		"drops empty steps":         {fake{dropsEmptySteps: true}, "series is read at a declared granularity"},
		"ignores a filter":          {fake{ignoresFilter: true}, "dimension not declared"},
		"series disagrees":          {fake{seriesDisagrees: true}, "sums to the count"},
		"leaks its key":             {fake{leaksToStderr: true}, "no credential appears"},
	} {
		r := runFake(tc.f)
		if r.Passed() {
			t.Errorf("%s: passed", name)
			continue
		}
		caught := false
		for _, c := range r.Checks {
			if c.Outcome == Fail && strings.Contains(c.Name, tc.check) {
				caught = true
			}
		}
		if !caught {
			var failed []string
			for _, c := range r.Checks {
				if c.Outcome != Pass {
					failed = append(failed, fmt.Sprintf("%s: %s", c.Name, c.Outcome))
				}
			}
			t.Errorf("%s: not caught by %q; what did not pass: %v", name, tc.check, failed)
		}
	}
}

// A disagreement a wrapper declared, by marking read_series partial, is a
// warning rather than a failure.
func TestADeclaredCaveatWarnsRatherThanFails(t *testing.T) {
	r := runFake(fake{seriesDisagrees: true, partialSeries: true})
	for _, c := range r.Checks {
		if strings.Contains(c.Name, "sums to the count") && c.Outcome != Warn {
			t.Errorf("a declared caveat: %s: %s", c.Outcome, c.Detail)
		}
	}
}

// A verb the suite cannot exercise yet is reported untested, which is never
// a pass.
func TestAnHonouredVerbTheSuiteCannotExerciseIsUntested(t *testing.T) {
	f := fake{}
	r := Run(context.Background(), Target{Description: description(), Run: func(ctx context.Context, req wp.WrapperRequest) (wp.WrapperResponse, string, error) {
		resp, err := f.respond(ctx, req)
		for i := range resp.Results {
			if resp.Results[i].Manifest != nil {
				resp.Results[i].Manifest.Verbs[wp.VerbListOwned] = wp.VerbSupport{Level: wp.VerbSupported}
			}
		}
		return resp, "", err
	}}, now)
	if r.Passed() || r.Count()[Untested] != 1 {
		t.Errorf("passed %v, untested %d", r.Passed(), r.Count()[Untested])
	}
}

// A wrapper wrong in a consistent way passes every check of consistency, and
// only the venue's truth catches it (SPIKE.md finding 5).
func TestOnlyTheVenuesTruthCatchesAConsistentlyWrongWrapper(t *testing.T) {
	if r := runFake(fake{doubles: true}); !r.Passed() {
		t.Fatal("without the venue's truth, a consistent wrapper was expected to pass; the test's premise is wrong")
	}
	target := Target{Description: description(), Run: fake{doubles: true}.run, Expected: map[string]float64{"pageviews/count": 23},
		Connection: wp.Connection{Credentials: map[string]string{"ACME_KEY": "acme-key-SECRET"}}}
	caught := false
	for _, c := range Run(context.Background(), target, now).Checks {
		caught = caught || (c.Outcome == Fail && strings.Contains(c.Detail, "the venue holds 23"))
	}
	if !caught {
		t.Error("the venue's truth did not catch a wrapper reading every count double")
	}
	target.Run = fake{}.run
	if r := Run(context.Background(), target, now); !r.Passed() {
		t.Error("a correct wrapper failed against the venue's truth")
	}
}
