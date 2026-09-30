package wrapperprotocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// publisher is a valid draft manifest for a tool that authorizes and posts.
func publisher() *CapabilityManifest {
	m := &CapabilityManifest{
		Contract: ContractVersion,
		Wrapper:  WrapperProvenance{Version: "0.1.0", SpecSource: "https://acme.example/api", SpecHash: "sha256:x"},
		Verbs:    map[Verb]VerbSupport{},
		Metrics:  map[string]MetricSupport{"favourites": {Level: MetricAvailable, Kind: KindCount, Aggregations: []string{"count"}}},
		Venue:    "reversible_writes", Idempotency: "Idempotency-Key, kept an hour", Entitlements: map[string]any{},
		StoragePolicy: "The account holder owns what is read.",
		Authorization: &AuthorizationSupport{Steps: []string{AuthorizeBegin, AuthorizeComplete, AuthorizeRevoke},
			Scopes: []string{"write:statuses"}, Expires: "never"},
		Kinds: map[string]KindSupport{"social_post": {Level: VerbSupported, Shape: json.RawMessage(`{"type":"object"}`)}},
	}
	for _, v := range ContractVerbs() {
		m.Verbs[v] = VerbSupport{Level: VerbDeclined, Reason: "not here"}
	}
	for _, v := range []Verb{VerbProbe, VerbAuthorize, VerbPublish, VerbRetract, VerbReadBack, VerbReadMetrics} {
		m.Verbs[v] = VerbSupport{Level: VerbSupported}
	}
	return m
}

func TestContractTwoHasFourteenVerbsAndAcceptsAPublisher(t *testing.T) {
	if len(ContractVerbs()) != 14 || !isContractVerb(VerbAuthorize) || isContractVerb("describe_shape") || isContractVerb("limits") {
		t.Errorf("verbs: %v", ContractVerbs())
	}
	if why := publisher().Check(); why != "" {
		t.Fatalf("a valid publisher's manifest was refused: %s", why)
	}
	old := publisher()
	old.Contract = "1"
	if old.Check() == "" {
		t.Error("a contract 1 manifest was accepted")
	}
}

// Every metric says what one reading is, and is read only as that kind
// allows: a rate is never offered as a sum (SPIKE.md finding 14).
func TestAMetricIsReadOnlyAsItsKindAllows(t *testing.T) {
	ok := map[string]MetricSupport{
		"pageviews":   {Level: MetricAvailable, Kind: KindCount, Aggregations: []string{"count"}},
		"visitors":    {Level: MetricAvailable, Kind: KindPeople, Aggregations: []string{"unique"}, Uniqueness: "per day"},
		"revenue":     {Level: MetricAvailable, Kind: KindSum, Unit: "USD", Aggregations: []string{"sum"}},
		"bounce_rate": {Level: MetricAvailable, Kind: KindRate, Per: "visit", Aggregations: []string{"value"}},
		"duration":    {Level: MetricAvailable, Kind: KindAverage, Per: "visit", Unit: "seconds", Aggregations: []string{"value"}},
		"impressions": {Level: MetricUnavailable, Reason: "not counted"},
	}
	m := publisher()
	m.Metrics = ok
	if why := m.Check(); why != "" {
		t.Fatalf("honest kinds were refused: %s", why)
	}
	for name, bad := range map[string]MetricSupport{
		"no kind":           {Level: MetricAvailable, Aggregations: []string{"count"}},
		"a rate as a sum":   {Level: MetricAvailable, Kind: KindRate, Per: "visit", Aggregations: []string{"sum"}},
		"a rate, per what":  {Level: MetricAvailable, Kind: KindRate, Aggregations: []string{"value"}},
		"a sum, no unit":    {Level: MetricAvailable, Kind: KindSum, Aggregations: []string{"sum"}},
		"people as a count": {Level: MetricAvailable, Kind: KindPeople, Aggregations: []string{"count"}},
		"a count as value":  {Level: MetricAvailable, Kind: KindCount, Aggregations: []string{"value"}},
	} {
		m := publisher()
		m.Metrics = map[string]MetricSupport{"x": bad}
		if m.Check() == "" {
			t.Errorf("%s: accepted", name)
		}
	}
	if !KindCount.Additive() || !KindSum.Additive() || KindRate.Additive() || KindPeople.Additive() || KindAverage.Additive() {
		t.Error("additivity by kind is wrong")
	}
}

func TestAManifestThatDoesNotSayEnoughIsRefused(t *testing.T) {
	for name, mutate := range map[string]func(*CapabilityManifest){
		"no authorization":      func(m *CapabilityManifest) { m.Authorization = nil },
		"no complete step":      func(m *CapabilityManifest) { m.Authorization.Steps = []string{AuthorizeBegin} },
		"an unknown step":       func(m *CapabilityManifest) { m.Authorization.Steps = append(m.Authorization.Steps, "renew") },
		"silent on expiry":      func(m *CapabilityManifest) { m.Authorization.Expires = "" },
		"publishes no kind":     func(m *CapabilityManifest) { m.Kinds = nil },
		"a kind with no shape":  func(m *CapabilityManifest) { m.Kinds["social_post"] = KindSupport{Level: VerbSupported} },
		"a declined kind, mute": func(m *CapabilityManifest) { m.Kinds["listing"] = KindSupport{Level: VerbDeclined} },
		"search, no horizon":    func(m *CapabilityManifest) { m.Verbs[VerbSearch] = VerbSupport{Level: VerbSupported} },
		"authorize unanswered":  func(m *CapabilityManifest) { delete(m.Verbs, VerbAuthorize) },
		"reads, lists nothing":  func(m *CapabilityManifest) { m.Metrics = nil },
	} {
		m := publisher()
		mutate(m)
		if m.Check() == "" {
			t.Errorf("%s: accepted", name)
		}
	}
	// A publisher that reads no numbers owes no metrics.
	m := publisher()
	m.Metrics = nil
	m.Verbs[VerbReadMetrics] = VerbSupport{Level: VerbDeclined, Reason: "no counts"}
	if why := m.Check(); why != "" {
		t.Errorf("a publisher that reads nothing: %s", why)
	}
}

// Credentials are secret: only authorize may carry them, output that names
// them is never quoted in an error, and what Mendel keeps has them out.
func TestCredentialsAreKeptOutOfEverythingMendelKeeps(t *testing.T) {
	const secret = "tok-SEEKRIT-123"
	elsewhere := `{"results":[{"verb":"publish","ref":"1","credentials":{"TOKEN":"` + secret + `"}}]}`
	if _, err := ParseWrapperResponse([]byte(elsewhere), 1); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("credentials on publish: %v", err)
	}
	broken := `{"results":[{"verb":"authorize","credentials":{"TOKEN":"` + secret + `"}`
	if _, err := ParseWrapperResponse([]byte(broken), 1); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("unreadable output holding a credential was quoted: %v", err)
	}

	ok := `{"results":[{"verb":"authorize","credentials":{"TOKEN":"` + secret + `"}}]}`
	resp, err := ParseWrapperResponse([]byte(ok), 1)
	if err != nil || resp.Results[0].Credentials["TOKEN"] != secret {
		t.Fatalf("authorize's credentials: %+v %v", resp, err)
	}
	kept, _ := json.Marshal(resp.Redacted())
	if strings.Contains(string(kept), secret) || !strings.Contains(string(kept), `"TOKEN"`) {
		t.Errorf("the redacted response: %s", kept)
	}
	if resp.Results[0].Credentials["TOKEN"] != secret {
		t.Error("redacting changed the response it was taken from")
	}

	asked := JobInstruction{InvocationID: "i", ReportTo: "https://m", Token: "t", Request: WrapperRequest{
		Contract: ContractVersion, Connection: Connection{Credentials: map[string]string{"CLIENT_SECRET": secret}},
		Calls: []VerbCall{{Verb: VerbAuthorize, Step: AuthorizeComplete, Code: secret}}}}
	stored, _ := json.Marshal(asked.Redacted())
	if strings.Contains(string(stored), secret) {
		t.Errorf("the stored instruction: %s", stored)
	}
	if asked.Request.Calls[0].Code != secret {
		t.Error("redacting changed the instruction it was taken from")
	}
}

// Every verb of the contract says what it is for.
func TestEveryVerbHasAPurpose(t *testing.T) {
	for _, v := range ContractVerbs() {
		if len(v.Purpose()) < 40 {
			t.Errorf("%s: %q", v, v.Purpose())
		}
	}
	if len(verbPurposes) != len(ContractVerbs()) {
		t.Errorf("%d purposes for %d verbs", len(verbPurposes), len(ContractVerbs()))
	}
}
