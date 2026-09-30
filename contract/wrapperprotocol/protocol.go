// Package wrapperprotocol is the External Tool Wrapper protocol
// (dev/claude_plans/35_tools_outside_the_codebase.md §6, §8): the wire types
// a wrapper reads and writes, the Capability Manifest and its check, and the
// runner that starts a wrapper image.
//
// Standard library only, so everything that speaks the protocol can import it
// without importing the server: the seam package, the tool registry that
// mendel-tool uses to verify a wrapper, and a wrapper's own tests.
package wrapperprotocol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// The External Tool Wrapper protocol (doc 35 §6, §8), as far as cut 1 needs
// it: the verbs a data source declares. One JSON request on stdin holding the
// contract version, the connection injected for the run, and a *list* of verb
// calls; one JSON response on stdout with one result per call, stopping at
// the first that did not succeed. The shape mirrors cmd/mendel-adapter --
// JSON in, JSON out, a report Mendel's own code checks -- and the types here
// are the wire format, which is why a wrapper written in Go can import them
// rather than restating them. The package sits at the module root, outside
// internal/, for exactly that: wrappers live in the mendel-integrations
// repository, never in this one.
//
// A wrapper declares what it can do at probe time and Mendel plans only from
// that. Nothing here reaches around the contract: a wrapper that cannot serve
// a granularity refuses it, and a metric a tool withholds is declared
// unavailable, never zero.
//
// The document this describes for wrapper authors is the mendel-integrations
// repository's README.

// ContractVersion is the version of this protocol. A wrapper records the
// version it was written against, and a request carries the version Mendel
// speaks; they must match, since a wrapper meeting a newer contract should say
// so rather than act on the parts it recognises.
//
// Contract 2 (2026-09-29) is what the wrapper-vetting spike settled
// (doc 35 §19): contract 1 carried a data source and nothing else. It adds
// authorize and the secret credentials field, the action surface's and
// search's arguments and answers (surface.go), and a kind for every metric;
// it drops describe_shape and limits, which the manifest already answers.
const ContractVersion = "2"

// Verb is one of the contract's fourteen verbs.
type Verb string

const (
	VerbProbe         Verb = "probe"
	VerbAuthorize     Verb = "authorize"
	VerbDraft         Verb = "draft"
	VerbPublish       Verb = "publish"
	VerbStatus        Verb = "status"
	VerbAppendUpdate  Verb = "append_update"
	VerbRetract       Verb = "retract"
	VerbReadBack      Verb = "read_back"
	VerbReadMetrics   Verb = "read_metrics"
	VerbSetCap        Verb = "set_cap"
	VerbListOwned     Verb = "list_owned"
	VerbReadSeries    Verb = "read_series"
	VerbReadTotal     Verb = "read_total"
	VerbSearch        Verb = "search"
)

// ContractVerbs is every verb, in the order §6 lists them. A manifest answers
// for all of them: a verb left out is not declined, it is unaccounted for.
func ContractVerbs() []Verb {
	return []Verb{VerbProbe, VerbAuthorize, VerbDraft, VerbPublish, VerbStatus,
		VerbAppendUpdate, VerbRetract, VerbReadBack, VerbReadMetrics, VerbSetCap, VerbListOwned,
		VerbReadSeries, VerbReadTotal, VerbSearch}
}

// WriteVerbs are the verbs that change something at the tool. A data source
// declares every one of them absent.
func WriteVerbs() []Verb {
	return []Verb{VerbDraft, VerbPublish, VerbAppendUpdate, VerbRetract, VerbSetCap}
}

// WrapperRequest is what a wrapper reads from stdin.
type WrapperRequest struct {
	Contract   string     `json:"contract"`
	Connection Connection `json:"connection"`
	Calls      []VerbCall `json:"calls"`
}

// Connection is one project's account with the tool, injected for the
// duration of one run and never stored by the wrapper.
type Connection struct {
	// Credentials by name, as the wrapper's connection spec names them.
	// Decrypted for this run only.
	Credentials map[string]string `json:"credentials"`
	// AccountID is the tool's own handle on the account: a site, an ad
	// account, a workspace.
	AccountID string `json:"account_id"`
	// Endpoint is the API's base URL when the project's instance is not the
	// tool's hosted one (a self-hosted instance). Empty means the tool's own.
	Endpoint string `json:"endpoint,omitempty"`
	// Config is the project's settings for the tool, per doc 35 §9: the
	// parent hierarchy it attaches to and the like. Empty for a data source.
	Config map[string]any `json:"config,omitempty"`
}

// VerbCall is one call in a run. Only the fields the verb reads are set.
type VerbCall struct {
	Verb Verb `json:"verb"`

	// Measure, Window, Granularity and Filter are read_series's and
	// read_total's arguments (§6).
	Measure     *Measure `json:"measure,omitempty"`
	Window      *Window  `json:"window,omitempty"`
	Granularity string   `json:"granularity,omitempty"`
	Filter      *Filter  `json:"filter,omitempty"`

	// Authorize's step and its arguments, the action surface's asset and
	// reference, and search's query (surface.go). Code is a secret, as the
	// credentials are.
	Step           string          `json:"step,omitempty"`
	RedirectURI    string          `json:"redirect_uri,omitempty"`
	State          string          `json:"state,omitempty"`
	Code           string          `json:"code,omitempty"`
	AssetKind      string          `json:"asset_kind,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	Ref            string          `json:"ref,omitempty"`
	When           *When           `json:"when,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	// Name is Mendel's name for an asset it drafts or publishes, starting
	// with its naming prefix; the wrapper keeps it where the tool keeps a
	// name, and list_owned finds assets by it.
	Name string `json:"name,omitempty"`
	Query          string          `json:"query,omitempty"`
	Limit          int             `json:"limit,omitempty"`
	Prefix         string          `json:"prefix,omitempty"`
	// Cap is set_cap's argument, on the asset Ref names.
	Cap *Cap `json:"cap,omitempty"`
}

// Measure is what is counted: an event and how it is aggregated. The
// uniqueness semantics of a "unique" aggregation are the tool's, declared in
// the manifest for the metric, because "unique" means three different things
// across six analytics tools (Appendix B).
type Measure struct {
	Event       string `json:"event"`
	Aggregation string `json:"aggregation"` // count, unique, sum
}

// Window is the half-open interval [Start, End) a read covers, as instants.
// The wrapper converts to whatever the tool wants; a tool that reports in its
// site's zone says so with a quality flag.
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Filter is one equality filter, the only kind the contract allows. The
// dimension is in the tool's vocabulary as the manifest lists it.
type Filter struct {
	Dimension string `json:"dimension"`
	Value     string `json:"value"`
}

// Granularities a read_series may ask for. A wrapper serves the ones it lists
// in its manifest and refuses the rest; it never substitutes.
const (
	GranularityHour  = "hour"
	GranularityDay   = "day"
	GranularityWeek  = "week"
	GranularityMonth = "month"
)

// WrapperResponse is what a wrapper writes to stdout: one result per call it
// got to, in order. Fewer results than calls means it stopped at the first
// that did not succeed, and the last result says why.
type WrapperResponse struct {
	Results []VerbResult `json:"results"`
}

// VerbResult is the answer to one call. Exactly one of the outcome fields is
// set: the verb's payload, Refused, or Failed.
type VerbResult struct {
	Verb Verb `json:"verb"`

	// Manifest is probe's answer.
	Manifest *CapabilityManifest `json:"manifest,omitempty"`
	// Series is read_series's answer, one point per granularity step.
	Series []SeriesPoint `json:"series,omitempty"`
	// Total is read_total's answer.
	Total *Reading `json:"total,omitempty"`

	// The rest are authorize's and the action surface's (surface.go).
	//
	// Credentials are what authorize produced, for Mendel to hold encrypted
	// and hand back on every later run. They are secret: only authorize may
	// carry them (ParseWrapperResponse refuses them anywhere else), and what
	// Mendel keeps of a response is Redacted first.
	Credentials  map[string]string       `json:"credentials,omitempty"`
	AuthorizeURL string                  `json:"authorize_url,omitempty"`
	Ref          string                  `json:"ref,omitempty"`
	URL          string                  `json:"url,omitempty"`
	Status       *AssetStatus            `json:"status,omitempty"`
	Asset        json.RawMessage         `json:"asset,omitempty"`
	AssetMetrics map[string]FieldReading `json:"asset_metrics,omitempty"`
	Retracted    string                  `json:"retracted,omitempty"`
	Items        []Item                  `json:"items,omitempty"`
	Owned        []string                `json:"owned,omitempty"`
	// CapSet is set_cap's answer: the cap as the tool now holds it.
	CapSet *CapSet `json:"cap_set,omitempty"`

	// Refused is a designed outcome the wrapper will not do: a granularity
	// the tool lacks, a metric declared unavailable, a filter on a dimension
	// it does not have. The sentence names what was refused and why.
	Refused string `json:"refused,omitempty"`
	// Failed is something that went wrong: the API answered 500, the
	// credential was rejected, the response could not be read.
	Failed string `json:"failed,omitempty"`
}

// Succeeded reports whether the call produced its payload.
func (r VerbResult) Succeeded() bool { return r.Refused == "" && r.Failed == "" }

// Why is the sentence for a result that did not succeed, or "".
func (r VerbResult) Why() string {
	if r.Refused != "" {
		return "refused: " + r.Refused
	}
	if r.Failed != "" {
		return "failed: " + r.Failed
	}
	return ""
}

// SeriesPoint is one step of a series: the value over [At, At+granularity).
type SeriesPoint struct {
	At    time.Time `json:"at"`
	Value float64   `json:"value"`
}

// Reading is one number and what qualifies it.
type Reading struct {
	Value float64 `json:"value"`
	// Quality flags, from the manifest's vocabulary for the metric: sampled,
	// thresholded, cached, reported in the site's zone. Empty is a plain read.
	Quality []string `json:"quality,omitempty"`
}

// --- The Capability Manifest ---

// VerbLevel is how far a wrapper honours a verb.
type VerbLevel string

const (
	VerbSupported VerbLevel = "supported"
	VerbPartial   VerbLevel = "partial"
	VerbDeclined  VerbLevel = "declined"
)

// VerbSupport is a manifest's answer for one verb. Partial carries the
// caveat; declined carries the reason. Both are sentences for a reader.
type VerbSupport struct {
	Level  VerbLevel `json:"level"`
	Caveat string    `json:"caveat,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

// MetricLevel is whether a measure can be read.
type MetricLevel string

const (
	MetricAvailable   MetricLevel = "available"
	MetricUnavailable MetricLevel = "unavailable"
)

// MetricKind is what one reading of a metric is, which decides what may be
// done with readings: a count or a sum over a window is the sum of it over
// the window's parts, and nothing else here is. A rate summed across days,
// or people counted twice for coming back, is a number nobody should plan
// against -- and a wrapper can report one while every reading is right for
// its own window (SPIKE.md finding 14, where a generated wrapper declared
// bounce rate a "sum"). The kind is the wrapper saying which it is.
type MetricKind string

const (
	// KindCount is a number of events: additive across windows. Read as
	// count, or as unique (the distinct people behind the events).
	KindCount MetricKind = "count"
	// KindPeople is a number of distinct people: not additive, since one
	// person in two windows is one person. Read as unique.
	KindPeople MetricKind = "people"
	// KindSum is a quantity summed, in a unit: additive. Read as sum.
	KindSum MetricKind = "sum"
	// KindRate is a ratio of two counts the tool computes over the window
	// asked for, per something: not additive. Read as value.
	KindRate MetricKind = "rate"
	// KindAverage is a mean the tool computes over the window, in a unit,
	// per something: not additive. Read as value.
	KindAverage MetricKind = "average"
)

// Additive reports whether a reading over a window is the sum of readings
// over its parts, and so may be added up.
func (k MetricKind) Additive() bool { return k == KindCount || k == KindSum }

// kindAggregations are the aggregations each kind may be read with.
var kindAggregations = map[MetricKind][]string{
	KindCount: {"count", "unique"}, KindPeople: {"unique"}, KindSum: {"sum"},
	KindRate: {"value"}, KindAverage: {"value"},
}

// MetricSupport is a manifest's answer for one metric the tool exposes.
type MetricSupport struct {
	Level MetricLevel `json:"level"`
	// Reason is why it is unavailable. Zendesk withholds article views by
	// design; the manifest says so, never zero.
	Reason string `json:"reason,omitempty"`
	// Kind is what one reading is (MetricKind); required when available.
	// Prefer counts: offer a rate or an average only where the tool gives no
	// counts to take the ratio of, since Mendel can divide two counts itself.
	Kind MetricKind `json:"kind,omitempty"`
	// Per is what a rate or an average is per ("visit", "page view");
	// required for those kinds.
	Per string `json:"per,omitempty"`
	// Unit is what a sum or an average is measured in ("USD", "seconds");
	// required for those kinds.
	Unit string `json:"unit,omitempty"`
	// Aggregations this metric can be read with, as its kind allows: count
	// and unique for a count, unique for people, sum for a sum, value for a
	// rate or an average.
	Aggregations []string `json:"aggregations"`
	// Uniqueness is what "unique" means for this metric, in the tool's own
	// words, cited. Required when Aggregations includes unique.
	Uniqueness string `json:"uniqueness,omitempty"`
	// Quality flags every read of this metric carries.
	Quality []string `json:"quality,omitempty"`
	// Spend marks the metric that is what an asset has cost at the tool: a
	// sum in a currency, and at most one per manifest. Kind and unit cannot
	// tell spend from revenue on a platform that reports both, and
	// recording one as the other is a silent wrong answer.
	Spend bool `json:"spend,omitempty"`
}

// WrapperProvenance is what the wrapper says about itself: the version, and
// the spec it was written against, fed in and never recalled (§8).
type WrapperProvenance struct {
	Version    string `json:"version"`
	SpecSource string `json:"spec_source"`
	SpecHash   string `json:"spec_hash"`
}

// CapabilityManifest is probe's answer (§6): per verb and per metric,
// supported / partial / declined, with the venue, the entitlements observed on
// this account, and what the tool's terms say about keeping what is read.
type CapabilityManifest struct {
	Contract string            `json:"contract"`
	Wrapper  WrapperProvenance `json:"wrapper"`

	Verbs   map[Verb]VerbSupport     `json:"verbs"`
	Metrics map[string]MetricSupport `json:"metrics"`
	// Granularities read_series can serve. Anything else is refused.
	Granularities []string `json:"granularities"`
	// FilterDimensions an equality filter may name.
	FilterDimensions []string `json:"filter_dimensions"`

	// Venue is what the conformance venue could prove (§8): test_account,
	// reversible_writes, read_only, nothing_safe.
	Venue string `json:"venue"`
	// Idempotency the tool offers natively: none, or a sentence.
	Idempotency string `json:"idempotency"`
	// Entitlements observed on this account: plan, rate limit, retention.
	Entitlements map[string]any `json:"entitlements"`
	// StoragePolicy is what the tool's terms say about keeping and showing
	// what is read (§12), with the source cited. The data-licensing
	// obligation is judged against it.
	StoragePolicy string `json:"storage_policy"`

	// The rest are contract 2-draft's (draft.go): how the wrapper connects
	// an account, the External Asset Kinds it can act on, and how far back a
	// search reaches.
	Authorization *AuthorizationSupport  `json:"authorization,omitempty"`
	Kinds         map[string]KindSupport `json:"kinds,omitempty"`
	SearchHorizon string                 `json:"search_horizon,omitempty"`
}

// manifestVenues are the venues §8 names.
var manifestVenues = []string{"test_account", "reversible_writes", "read_only", "nothing_safe"}

// Check refuses a malformed manifest, the way experiment.MigrationContract
// refuses a malformed contract at the probe: a manifest Mendel plans from
// has to answer every question, and "did not say" is not an answer.
func (m *CapabilityManifest) Check() string {
	if m == nil {
		return "the probe returned no manifest"
	}
	if m.Contract != ContractVersion {
		return fmt.Sprintf("the manifest is for contract %q; Mendel speaks %q", m.Contract, ContractVersion)
	}
	if m.Wrapper.Version == "" || m.Wrapper.SpecSource == "" || m.Wrapper.SpecHash == "" {
		return "the manifest does not say which wrapper version answered or which spec it was written against"
	}
	for _, v := range ContractVerbs() {
		s, ok := m.Verbs[v]
		if !ok {
			return fmt.Sprintf("the manifest does not answer for verb %s; a verb it lacks is declined, not omitted", v)
		}
		switch s.Level {
		case VerbSupported:
			if s.Caveat != "" || s.Reason != "" {
				return fmt.Sprintf("verb %s is supported and yet carries a caveat or reason", v)
			}
		case VerbPartial:
			if s.Caveat == "" {
				return fmt.Sprintf("verb %s is partial without saying what the caveat is", v)
			}
		case VerbDeclined:
			if s.Reason == "" {
				return fmt.Sprintf("verb %s is declined without a reason", v)
			}
		default:
			return fmt.Sprintf("verb %s has level %q; want supported, partial or declined", v, s.Level)
		}
	}
	for v := range m.Verbs {
		if !isContractVerb(v) {
			return fmt.Sprintf("the manifest answers for %q, which is not a verb of the contract", v)
		}
	}
	if m.Verbs[VerbProbe].Level != VerbSupported {
		return "a wrapper that answered probe must declare probe supported"
	}
	if len(m.Metrics) == 0 && m.readsNumbers() {
		return "the manifest reads numbers and lists no metrics"
	}
	if why := m.checkSurface(); why != "" {
		return why
	}
	spend := ""
	for _, ms := range SortedMetrics(m.Metrics) {
		if ms.Support.Spend {
			switch {
			case spend != "":
				return fmt.Sprintf("metrics %s and %s are both marked spend; at most one metric is what an asset cost",
					spend, ms.Name)
			case ms.Support.Level == MetricAvailable &&
				(ms.Support.Kind != KindSum || !currencyPattern.MatchString(ms.Support.Unit)):
				return fmt.Sprintf("metric %s is marked spend and is not a sum in a currency (kind %q, unit %q)",
					ms.Name, ms.Support.Kind, ms.Support.Unit)
			}
			spend = ms.Name
		}
		switch ms.Support.Level {
		case MetricAvailable:
			allowed, known := kindAggregations[ms.Support.Kind]
			switch {
			case !known:
				return fmt.Sprintf("metric %s has kind %q; say what one reading is: count, people, sum, rate or average",
					ms.Name, ms.Support.Kind)
			case (ms.Support.Kind == KindRate || ms.Support.Kind == KindAverage) && strings.TrimSpace(ms.Support.Per) == "":
				return fmt.Sprintf("metric %s is a %s without saying what it is per", ms.Name, ms.Support.Kind)
			case (ms.Support.Kind == KindSum || ms.Support.Kind == KindAverage) && strings.TrimSpace(ms.Support.Unit) == "":
				return fmt.Sprintf("metric %s is a %s without saying its unit", ms.Name, ms.Support.Kind)
			case len(ms.Support.Aggregations) == 0:
				return fmt.Sprintf("metric %s is available with no aggregation it can be read with", ms.Name)
			}
			for _, a := range ms.Support.Aggregations {
				ok := false
				for _, want := range allowed {
					ok = ok || a == want
				}
				if !ok {
					return fmt.Sprintf("metric %s is a %s and offers aggregation %q; a %s is read as %s",
						ms.Name, ms.Support.Kind, a, ms.Support.Kind, strings.Join(allowed, " or "))
				}
				if a == "unique" && strings.TrimSpace(ms.Support.Uniqueness) == "" {
					return fmt.Sprintf("metric %s can be read unique without saying what unique means for it", ms.Name)
				}
			}
		case MetricUnavailable:
			if ms.Support.Reason == "" {
				return fmt.Sprintf("metric %s is unavailable without a reason", ms.Name)
			}
		default:
			return fmt.Sprintf("metric %s has level %q; want available or unavailable", ms.Name, ms.Support.Level)
		}
	}
	if m.Verbs[VerbReadSeries].Level != VerbDeclined && len(m.Granularities) == 0 {
		return "read_series is offered with no granularity it can serve"
	}
	for _, g := range m.Granularities {
		switch g {
		case GranularityHour, GranularityDay, GranularityWeek, GranularityMonth:
		default:
			return fmt.Sprintf("granularity %q is not one the contract names", g)
		}
	}
	venueKnown := false
	for _, v := range manifestVenues {
		if m.Venue == v {
			venueKnown = true
		}
	}
	if !venueKnown {
		return fmt.Sprintf("venue %q is not one of %s", m.Venue, strings.Join(manifestVenues, ", "))
	}
	if strings.TrimSpace(m.StoragePolicy) == "" {
		return "the manifest does not say what the tool's terms allow to be kept of what is read (storage_policy)"
	}
	return ""
}

// IsDataSource reports whether the manifest declares a read-only source: every
// write verb declined. A source that can write is not one Mendel reads Key
// Results from without a different gate.
func (m *CapabilityManifest) IsDataSource() bool {
	for _, v := range WriteVerbs() {
		if m.Verbs[v].Level != VerbDeclined {
			return false
		}
	}
	return true
}

func isContractVerb(v Verb) bool {
	for _, c := range ContractVerbs() {
		if c == v {
			return true
		}
	}
	return false
}

type NamedMetric struct {
	Name    string
	Support MetricSupport
}

// SortedMetrics walks a manifest's metrics in name order, so Check names the
// same problem on every run.
func SortedMetrics(m map[string]MetricSupport) []NamedMetric {
	out := make([]NamedMetric, 0, len(m))
	for name, s := range m {
		out = append(out, NamedMetric{name, s})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// --- Running one ---

// Runner runs a wrapper image once with a request and waits for what it
// printed. DockerRunner is the one implementation, and is what a Mendel
// admin's verification runs (`mendel-tool tools verify`), on their own
// machine against a venue account. A project's runs do not go through a
// Runner: they are Jobs in the project's own cluster (doc 35 §19), started
// and answered separately, through JobInstruction and JobReport below.
type Runner interface {
	Run(ctx context.Context, image string, req WrapperRequest) (WrapperResponse, error)
	// Name says where it runs, as the run log and the ledger record it: a
	// hosting platform's slug where a rate card prices it, anything else
	// where nothing is billed.
	Name() string
}

// wrapperRunTimeout bounds one run. A data source run is a handful of HTTP
// calls; a minute is generous and short enough that a hung wrapper does not
// hold the reconcile loop.
const wrapperRunTimeout = 60 * time.Second

// DockerRunner runs the image on this machine: `docker run -i --rm <image>`,
// the request on stdin, the response from stdout. The credential is in the
// request body and never on the command line or in the environment, so it
// does not show in `ps` or the daemon's logs.
type DockerRunner struct{}

// Name is where a DockerRunner runs: this machine, which no rate card
// prices, so its runs are logged and cost nothing in the ledger.
func (DockerRunner) Name() string { return "local-docker" }

func (DockerRunner) Run(ctx context.Context, image string, req WrapperRequest) (WrapperResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, wrapperRunTimeout)
	defer cancel()
	body, err := json.Marshal(req)
	if err != nil {
		return WrapperResponse{}, err
	}
	cmd := exec.CommandContext(ctx, "docker", "run", "-i", "--rm", image)
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return WrapperResponse{}, fmt.Errorf("running %s: %v: %s", image, err, tail(stderr.String(), 2048))
	}
	return ParseWrapperResponse(stdout.Bytes(), len(req.Calls))
}

// --- Running one as a Job in the project's cluster ---

// A wrapper speaks stdin and stdout, and nothing can pipe into a container in
// someone else's cluster (doc 35 §19). So the Job's pod has an init container
// that copies Mendel's small static shim (cmd/mendel-wrapper-shim) into a
// volume it shares with the wrapper's own container, and the wrapper's
// container runs the shim instead of its entrypoint. The shim reads a
// JobInstruction, runs the wrapper's command with the request on stdin,
// checks what it printed with ParseWrapperResponse, and POSTs one JobReport
// with the invocation's token. The wrapper image stays pure protocol.

// JobInstructionEnv is the variable the shim reads its instruction from,
// filled from the Job's Secret.
const JobInstructionEnv = "MENDEL_WRAPPER_INSTRUCTION"

// JobInstruction is what one wrapper Job is told.
type JobInstruction struct {
	InvocationID string `json:"invocation_id"`
	// ReportTo and Token are where the shim reports and what it presents.
	// The token is the whole of the authentication.
	ReportTo string `json:"report_to"`
	Token    string `json:"token"`
	// Request is what the wrapper reads on stdin, credentials included. It
	// exists in readable form only in the Job's Secret.
	Request WrapperRequest `json:"request"`
	// TimeoutSeconds bounds the wrapper's command. Zero is the default.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// Validate says why an instruction cannot be acted on, or "".
func (i JobInstruction) Validate() string {
	switch {
	case i.InvocationID == "":
		return "the instruction names no invocation, so a report could not say what it answers"
	case i.ReportTo == "" || i.Token == "":
		return "the instruction names nowhere to report or no token to report with"
	case len(i.Request.Calls) == 0:
		return "the request makes no calls"
	case i.TimeoutSeconds < 0:
		return "the timeout is negative"
	}
	return ""
}

// Timeout is how long the wrapper's command may run.
func (i JobInstruction) Timeout() time.Duration {
	if i.TimeoutSeconds > 0 {
		return time.Duration(i.TimeoutSeconds) * time.Second
	}
	return wrapperRunTimeout
}

// Redacted is the instruction as Mendel keeps it: the question as asked, with
// the token and every credential's value taken out. What a report is checked
// against needs neither, so keeping them would be storing secrets for no
// purpose in the one place designed to be read back later.
func (i JobInstruction) Redacted() JobInstruction {
	out := i
	out.Token = ""
	creds := make(map[string]string, len(i.Request.Connection.Credentials))
	for name := range i.Request.Connection.Credentials {
		creds[name] = ""
	}
	out.Request.Connection.Credentials = creds
	out.Request.Calls = redactedCalls(i.Request.Calls)
	return out
}

// JobReport is the one thing a wrapper Job sends Mendel: the wrapper's
// response, already checked against the calls it was sent, or why there is
// none.
type JobReport struct {
	InvocationID string `json:"invocation_id"`
	// Response is what the wrapper printed, as ParseWrapperResponse accepted
	// it. Set exactly when Failed is empty.
	Response *WrapperResponse `json:"response,omitempty"`
	// Failed is why the run produced no response Mendel can read: the
	// command could not start, ran out of time, exited non-zero, or printed
	// something the protocol refuses.
	Failed string `json:"failed,omitempty"`
	// Stderr is the end of what the command said on stderr, for a reader
	// working out why.
	Stderr string `json:"stderr,omitempty"`
}

// Check reads a report against the instruction it answers, on Mendel's side:
// the right invocation, exactly one of a response and a failure, and a
// response that answers the calls that were sent, in order. A shim that has
// been replaced, or a report that was not written by one, is refused here.
func (r JobReport) Check(asked JobInstruction) string {
	switch {
	case r.InvocationID != asked.InvocationID:
		return fmt.Sprintf("the report answers invocation %q, not %q", r.InvocationID, asked.InvocationID)
	case (r.Response == nil) == (r.Failed == ""):
		return "a report carries a response or a failure, and exactly one"
	case r.Response == nil:
		return ""
	}
	raw, err := json.Marshal(r.Response)
	if err != nil {
		return err.Error()
	}
	if _, err := ParseWrapperResponse(raw, len(asked.Request.Calls)); err != nil {
		return err.Error()
	}
	for i, res := range r.Response.Results {
		if res.Verb != asked.Request.Calls[i].Verb {
			return fmt.Sprintf("result %d answers %s, and the call was %s", i, res.Verb, asked.Request.Calls[i].Verb)
		}
	}
	return ""
}

// ParseWrapperResponse checks what a wrapper printed against the calls it was
// sent, on Mendel's side, so a wrapper cannot answer for calls it was not
// asked or claim success for a call it stopped before.
func ParseWrapperResponse(stdout []byte, calls int) (WrapperResponse, error) {
	var resp WrapperResponse
	if err := json.Unmarshal(stdout, &resp); err != nil {
		return WrapperResponse{}, fmt.Errorf("the wrapper's answer is not readable as JSON: %v: %s", err, quotable(stdout))
	}
	for i, r := range resp.Results {
		if len(r.Credentials) > 0 && r.Verb != VerbAuthorize {
			return WrapperResponse{}, fmt.Errorf("result %d (%s) carries credentials, which only authorize may return", i, r.Verb)
		}
		if r.CapSet != nil && r.Verb != VerbSetCap {
			return WrapperResponse{}, fmt.Errorf("result %d (%s) carries a cap_set, which only set_cap answers", i, r.Verb)
		}
		if r.Verb == VerbSetCap && r.Succeeded() {
			if r.CapSet == nil {
				return WrapperResponse{}, fmt.Errorf("result %d (set_cap) succeeded without saying what cap the tool now holds", i)
			}
			if why := r.CapSet.Check(); why != "" {
				return WrapperResponse{}, fmt.Errorf("result %d (set_cap): %s", i, why)
			}
		}
		if r.Status != nil && !effectiveStates[r.Status.Effective] {
			return WrapperResponse{}, fmt.Errorf("result %d (%s) says the asset is %q, which is not a state the contract has (not_live, live, gone)",
				i, r.Verb, r.Status.Effective)
		}
	}
	if len(resp.Results) == 0 {
		return WrapperResponse{}, fmt.Errorf("the wrapper answered nothing for %d calls", calls)
	}
	if len(resp.Results) > calls {
		return WrapperResponse{}, fmt.Errorf("the wrapper answered %d results for %d calls", len(resp.Results), calls)
	}
	for i, r := range resp.Results[:len(resp.Results)-1] {
		if !r.Succeeded() {
			return WrapperResponse{}, fmt.Errorf("result %d (%s) did not succeed and yet the wrapper went on to the next call", i, r.Verb)
		}
	}
	last := resp.Results[len(resp.Results)-1]
	if len(resp.Results) < calls && last.Succeeded() {
		return WrapperResponse{}, fmt.Errorf("the wrapper stopped after %d of %d calls without saying why", len(resp.Results), calls)
	}
	return resp, nil
}

// Answer is the result for one call, or the sentence for why the run did not
// get there: the earlier result that stopped it, or its own refusal.
func (r WrapperResponse) Answer(i int) (VerbResult, string) {
	if i < len(r.Results) {
		res := r.Results[i]
		if !res.Succeeded() {
			return res, res.Why()
		}
		return res, ""
	}
	stopped := r.Results[len(r.Results)-1]
	return VerbResult{}, fmt.Sprintf("the run stopped at %s, which %s", stopped.Verb, stopped.Why())
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
