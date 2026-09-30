package wrapperprotocol

import (
	"math"
	"testing"
	"time"
)

// A price per call is declared with the wrapper: for a claimed verb, once,
// in a currency, for a named plan, with where the tool publishes it, and
// overridden only by a setting the connection declares.
func TestAPricePerCallIsDeclaredInFull(t *testing.T) {
	good := func() Description {
		return Description{Tool: Tool{Slug: "acme", Name: "Acme"}, Version: "1", Contract: ContractVersion,
			Image: "acme:dev", Command: []string{"/acme"},
			Connection: ConnectionSpec{Credentials: []Field{{Name: "ACME_KEY", Label: "Key"}},
				Config: []Setting{{Name: "price_per_search", Label: "Your price per search"}}},
			Claims: map[string]string{"search": "supported"},
			Prices: []CallPrice{{Verb: VerbSearch, Amount: 0.008, Currency: "USD", Plan: "Pay as you go",
				Source: "https://acme.example/pricing", PlanSetting: "price_per_search"}}}
	}
	if d := good(); d.Check() != "" {
		t.Fatalf("a good price was refused: %s", d.Check())
	}
	for name, mutate := range map[string]func(*Description){
		"not a verb":          func(d *Description) { d.Prices[0].Verb = "fetch" },
		"priced twice":        func(d *Description) { d.Prices = append(d.Prices, d.Prices[0]) },
		"not claimed":         func(d *Description) { d.Claims = map[string]string{"search": "declined", "probe": "supported"} },
		"negative":            func(d *Description) { d.Prices[0].Amount = -1 },
		"not a number":        func(d *Description) { d.Prices[0].Amount = math.NaN() },
		"a currency by name":  func(d *Description) { d.Prices[0].Currency = "dollars" },
		"no plan":             func(d *Description) { d.Prices[0].Plan = " " },
		"no source":           func(d *Description) { d.Prices[0].Source = "" },
		"undeclared override": func(d *Description) { d.Prices[0].PlanSetting = "rate" },
	} {
		d := good()
		mutate(&d)
		if d.Check() == "" {
			t.Errorf("%s: accepted", name)
		}
	}
}

// At most one metric is what an asset cost, and it is a sum in a currency:
// revenue is a sum in a currency too, and must never be read as spend.
func TestSpendIsOneMetricAndASumInACurrency(t *testing.T) {
	spend := MetricSupport{Level: MetricAvailable, Kind: KindSum, Unit: "USD", Aggregations: []string{"sum"}, Spend: true}
	revenue := MetricSupport{Level: MetricAvailable, Kind: KindSum, Unit: "USD", Aggregations: []string{"sum"}}
	m := publisher()
	m.Metrics = map[string]MetricSupport{"cost": spend, "conversion_value": revenue}
	if why := m.Check(); why != "" {
		t.Fatalf("spend beside revenue was refused: %s", why)
	}
	withheld := MetricSupport{Level: MetricUnavailable, Reason: "the API does not report cost", Spend: true}
	m.Metrics = map[string]MetricSupport{"cost": withheld}
	if why := m.Check(); why != "" {
		t.Errorf("spend declared withheld was refused: %s", why)
	}
	clicks := MetricSupport{Level: MetricAvailable, Kind: KindCount, Aggregations: []string{"count"}, Spend: true}
	seconds := MetricSupport{Level: MetricAvailable, Kind: KindSum, Unit: "seconds", Aggregations: []string{"sum"}, Spend: true}
	for name, metrics := range map[string]map[string]MetricSupport{
		"two spends":        {"cost": spend, "cost_again": spend},
		"a count as spend":  {"clicks": clicks},
		"a sum of seconds":  {"watch_time": seconds},
	} {
		m := publisher()
		m.Metrics = metrics
		if m.Check() == "" {
			t.Errorf("%s: accepted", name)
		}
	}
}

// set_cap is answered with the cap as the tool holds it, and how it holds
// it; an answer without one, or one on another verb, is refused.
func TestSetCapAnswersTheCapTheToolHolds(t *testing.T) {
	total, daily, neg := 100.0, 10.0, -1.0
	ends := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	good := Cap{Currency: "USD", Total: &total, Daily: &daily, EndsAt: ends}
	if why := good.Check(); why != "" {
		t.Fatalf("a good cap was refused: %s", why)
	}
	for name, c := range map[string]Cap{
		"no amount":      {Currency: "USD", EndsAt: ends},
		"no end date":    {Currency: "USD", Total: &total},
		"negative daily": {Currency: "USD", Daily: &neg, EndsAt: ends},
		"no currency":    {Total: &total, EndsAt: ends},
	} {
		if c.Check() == "" {
			t.Errorf("cap %s: accepted", name)
		}
	}
	for name, set := range map[string]CapSet{
		"a target, by how much": {Cap: good, Enforcement: CapTarget},
		"enforced how":          {Cap: good, Enforcement: "soft"},
		"a bad cap held":        {Cap: Cap{Currency: "USD", EndsAt: ends}, Enforcement: CapHard},
	} {
		if set.Check() == "" {
			t.Errorf("answer %s: accepted", name)
		}
	}

	parse := func(raw string) error { _, err := ParseWrapperResponse([]byte(raw), 1); return err }
	const held = `"cap_set":{"cap":{"currency":"USD","total":100,"ends_at":"2026-10-31T00:00:00Z"},"enforcement":"target","tolerance":"up to 2x a day, averaging out over a month (docs, Budgets)"}`
	if err := parse(`{"results":[{"verb":"set_cap",` + held + `}]}`); err != nil {
		t.Errorf("a good answer: %v", err)
	}
	for name, raw := range map[string]string{
		"says nothing held": `{"results":[{"verb":"set_cap"}]}`,
		"a target unbound":  `{"results":[{"verb":"set_cap","cap_set":{"cap":{"currency":"USD","total":1,"ends_at":"2026-10-31T00:00:00Z"},"enforcement":"target"}}]}`,
		"on another verb":   `{"results":[{"verb":"status","status":{"configured":"x","effective":"live"},` + held + `}]}`,
	} {
		if parse(raw) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := parse(`{"results":[{"verb":"set_cap","refused":"this campaign type takes no cap"}]}`); err != nil {
		t.Errorf("a refusal needs no cap: %v", err)
	}
}
