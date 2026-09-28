// Command plausible is the External Tool Wrapper for Plausible Analytics
// (dev/claude_plans/35_tools_outside_the_codebase.md §8): a data source
// honouring probe, read_series and read_total, and declaring every other verb
// of the contract absent.
//
// It reads one request from stdin and writes one response to stdout, per
// README.md, and knows nothing about Mendel beyond that
// protocol. The API it calls is the one cited in README.md beside it, fetched
// when this was written rather than recalled.
//
// Standard library only, so the image is one static binary. The wire types are
// restated here rather than imported from internal/external, which would pull
// Mendel's server into the image; the test holds the two to the same shape by
// running this wrapper's answers through internal/external's own checks.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// contractVersion is the protocol version this wrapper was written against.
const contractVersion = "1"

// wrapperVersion is this wrapper's own version, recorded as its provenance.
const wrapperVersion = "0.1.0"

// runTimeout bounds one run: a handful of HTTP calls.
const runTimeout = 45 * time.Second

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	if err := run(ctx, os.Stdin, os.Stdout, http.DefaultClient); err != nil {
		// Only a request that cannot be read at all ends here; everything
		// else is answered on stdout.
		fmt.Fprintln(os.Stderr, "plausible:", err)
		os.Exit(1)
	}
}

// run answers one request: each call in order, stopping at the first that
// does not succeed.
func run(ctx context.Context, in io.Reader, out io.Writer, client *http.Client) error {
	var req request
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return fmt.Errorf("the request is not readable: %w", err)
	}
	var resp response
	if req.Contract != contractVersion {
		resp.Results = append(resp.Results, result{Verb: "probe",
			Failed: fmt.Sprintf("this wrapper speaks contract %q and was sent %q", contractVersion, req.Contract)})
		return json.NewEncoder(out).Encode(resp)
	}
	api := newAPI(req.Connection, client)
	for _, call := range req.Calls {
		r := answer(ctx, api, call)
		resp.Results = append(resp.Results, r)
		if r.Refused != "" || r.Failed != "" {
			break
		}
	}
	return json.NewEncoder(out).Encode(resp)
}

func answer(ctx context.Context, api *api, call call) result {
	switch call.Verb {
	case "probe":
		return api.probe(ctx)
	case "read_total":
		return api.readTotal(ctx, call)
	case "read_series":
		return api.readSeries(ctx, call)
	}
	return result{Verb: call.Verb, Refused: fmt.Sprintf("this wrapper declares %s absent; see its probe", call.Verb)}
}

// --- The wire format (README.md) ---

type request struct {
	Contract   string     `json:"contract"`
	Connection connection `json:"connection"`
	Calls      []call     `json:"calls"`
}

type connection struct {
	Credentials map[string]string `json:"credentials"`
	AccountID   string            `json:"account_id"`
	Endpoint    string            `json:"endpoint,omitempty"`
}

type call struct {
	Verb        string   `json:"verb"`
	Measure     *measure `json:"measure,omitempty"`
	Window      *window  `json:"window,omitempty"`
	Granularity string   `json:"granularity,omitempty"`
	Filter      *filter  `json:"filter,omitempty"`
}

type measure struct {
	Event       string `json:"event"`
	Aggregation string `json:"aggregation"`
}

type window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type filter struct {
	Dimension string `json:"dimension"`
	Value     string `json:"value"`
}

type response struct {
	Results []result `json:"results"`
}

type result struct {
	Verb     string        `json:"verb"`
	Manifest *manifest     `json:"manifest,omitempty"`
	Series   []seriesPoint `json:"series,omitempty"`
	Total    *reading      `json:"total,omitempty"`
	Refused  string        `json:"refused,omitempty"`
	Failed   string        `json:"failed,omitempty"`
}

type seriesPoint struct {
	At    time.Time `json:"at"`
	Value float64   `json:"value"`
}

type reading struct {
	Value   float64  `json:"value"`
	Quality []string `json:"quality,omitempty"`
}

type verbSupport struct {
	Level  string `json:"level"`
	Caveat string `json:"caveat,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type metricSupport struct {
	Level        string   `json:"level"`
	Reason       string   `json:"reason,omitempty"`
	Aggregations []string `json:"aggregations"`
	Uniqueness   string   `json:"uniqueness,omitempty"`
	Quality      []string `json:"quality,omitempty"`
}

type provenance struct {
	Version    string `json:"version"`
	SpecSource string `json:"spec_source"`
	SpecHash   string `json:"spec_hash"`
}

type manifest struct {
	Contract         string                   `json:"contract"`
	Wrapper          provenance               `json:"wrapper"`
	Verbs            map[string]verbSupport   `json:"verbs"`
	Metrics          map[string]metricSupport `json:"metrics"`
	Granularities    []string                 `json:"granularities"`
	FilterDimensions []string                 `json:"filter_dimensions"`
	Venue            string                   `json:"venue"`
	Idempotency      string                   `json:"idempotency"`
	Entitlements     map[string]any           `json:"entitlements"`
	StoragePolicy    string                   `json:"storage_policy"`
}
