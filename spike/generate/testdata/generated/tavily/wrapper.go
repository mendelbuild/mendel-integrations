package main

import (
	"fmt"
	"net/http"
	"strings"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// Aliases so the rest of this package reads as Tavily's own vocabulary while
// staying exactly wrapperprotocol's wire types: this wrapper imports the real
// package rather than restating it, per "standard library and the
// wrapperprotocol package only".
type (
	WrapperRequest  = wp.WrapperRequest
	WrapperResponse = wp.WrapperResponse
	VerbCall        = wp.VerbCall
	VerbResult      = wp.VerbResult
	Connection      = wp.Connection
)

// Version is this wrapper's own version. wrapper.json's "version" must agree
// (checked in wrapper_test.go), since Mendel refuses a probe that answers as
// another version.
const Version = "0.1.0"

// SpecSource names what this wrapper was written against (probe's
// manifest.wrapper.spec_source). See README.md for the citations, with dates.
const SpecSource = "https://docs.tavily.com/api-reference/endpoint/search, " +
	"https://docs.tavily.com/api-reference/endpoint/usage, " +
	"https://docs.tavily.com/guides/api-credits, " +
	"https://docs.tavily.com/guides/rate-limits, " +
	"https://tavily.com/terms"

// SpecHash is sha256 of search.md + usage.md + api-credits.md +
// rate-limits.md + terms.txt, concatenated in that order, as fetched into
// spec/tavily/ on 2026-09-29 (README.md explains and can reproduce it).
const SpecHash = "sha256:799d02149d52787de984725d61588d7f9ccb9d9d9ca3c332bfcceb42acd7566e"

// credentialName is the credential wrapper.json asks a project for, and the
// key it arrives under in connection.credentials.
const credentialName = "TAVILY_API_KEY"

// defaultBaseURL is Tavily's one hosted API (spec/tavily/search.md's
// `servers`); Tavily offers no self-hosted variant, so wrapper.json asks for
// no endpoint override and this is always what is called.
const defaultBaseURL = "https://api.tavily.com"

// maxResults is search's documented ceiling (spec/tavily/search.md,
// max_results: minimum 0, maximum 20). A call asking for more is refused
// rather than silently capped.
const maxResults = 20

// Wrapper is the running instance: an HTTP client (swapped for a test
// server's in wrapper_test.go) and the base URL to call (same reason).
type Wrapper struct {
	HTTP    *http.Client
	BaseURL string
}

func (w *Wrapper) client() *http.Client {
	if w.HTTP != nil {
		return w.HTTP
	}
	return http.DefaultClient
}

func (w *Wrapper) baseURL() string {
	if w.BaseURL != "" {
		return w.BaseURL
	}
	return defaultBaseURL
}

// Handle answers a whole run: one result per call, in order, stopping at the
// first that does not succeed (GUIDE.md "The protocol").
func (w *Wrapper) Handle(req WrapperRequest) WrapperResponse {
	if req.Contract != wp.DraftContractVersion {
		// "A wrapper written against another one answers its first call
		// failed and stops."
		if len(req.Calls) == 0 {
			return WrapperResponse{}
		}
		return WrapperResponse{Results: []VerbResult{{
			Verb: req.Calls[0].Verb,
			Failed: fmt.Sprintf("this wrapper speaks contract %q; the request is contract %q",
				wp.DraftContractVersion, req.Contract),
		}}}
	}

	var results []VerbResult
	for _, call := range req.Calls {
		res := w.dispatch(req.Connection, call)
		res.Verb = call.Verb
		results = append(results, res)
		if !res.Succeeded() {
			break
		}
	}
	return WrapperResponse{Results: results}
}

func (w *Wrapper) dispatch(conn Connection, call VerbCall) VerbResult {
	switch call.Verb {
	case wp.VerbProbe:
		return w.probe(conn)
	case wp.VerbSearch:
		return w.search(conn, call)
	default:
		reason, ok := declineReasons[call.Verb]
		if !ok {
			// Every verb of the contract is in declineReasons or handled
			// above; wrapper_test.go checks this holds for every verb the
			// package knows, so reaching here means the protocol grew a
			// verb this wrapper has not been told about.
			reason = fmt.Sprintf("tavily does not know verb %q", call.Verb)
		}
		return VerbResult{Refused: reason}
	}
}

// declineReasons is every verb this wrapper answers declined, each with the
// sentence probe's manifest also carries for it (buildManifest uses the same
// map, so the two can never disagree).
var declineReasons = map[wp.Verb]string{
	wp.VerbAuthorize: "Tavily connects with a bearer API key a person copies from app.tavily.com; " +
		"there is no OAuth-style exchange for this wrapper to drive",
	wp.VerbDescribeShape: "Tavily is a search tool with no External Asset Kind for describe_shape to describe",
	wp.VerbLimits: "Tavily's limits are fixed by plan and documented (spec/tavily/rate-limits.md), not a " +
		"per-call value this wrapper could read; probe's entitlements carry what /usage reports instead",
	wp.VerbDraft:        "Tavily is a search tool, not a publisher; it has nothing for draft to create",
	wp.VerbPublish:      "Tavily is a search tool, not a publisher; it has nothing for publish to make live",
	wp.VerbStatus:       "Tavily is a search tool, not a publisher; there is no asset for status to report on",
	wp.VerbAppendUpdate: "Tavily is a search tool, not a publisher; there is no asset for append_update to update",
	wp.VerbRetract:      "Tavily is a search tool, not a publisher; there is no asset for retract to take down",
	wp.VerbReadBack:     "Tavily is a search tool, not a publisher; there is no asset for read_back to read",
	wp.VerbReadMetrics: "Tavily is a search tool, not a publisher; there is no asset for read_metrics to " +
		"read a metric of",
	wp.VerbSetCap:    "Tavily is a search tool, not a publisher; there is no cap for set_cap to set",
	wp.VerbListOwned: "Tavily is a search tool, not a publisher; there is nothing owned for list_owned to list",
	wp.VerbReadSeries: "Tavily exposes no analytics event stream to bucket into a series; " +
		"this wrapper is a search tool, not a data source",
	wp.VerbReadTotal: "Tavily exposes no analytics event stream to total; " +
		"this wrapper is a search tool, not a data source",
}

// storagePolicy is what Tavily's Platform Terms of Service say about keeping
// and showing what is read, cited in README.md and probe's manifest
// (storage_policy).
const storagePolicy = "Tavily's Platform Terms of Service (fetched 2026-09-29, https://tavily.com/terms): " +
	"§9.2 grants Tavily a worldwide, perpetual, irrevocable licence to collect, host, store, use and " +
	"otherwise process Customer Input -- which includes the search query -- to provide, support and " +
	"improve the Services, perform market research, and develop new products; §7 lets Tavily share " +
	"Customer Input with its affiliates and vendors as needed to provide the Services, including making " +
	"queries on third-party public web indexes. Tavily makes no commitment to delete a query or its " +
	"results on request. This wrapper never asks for include_answer, so no LLM-generated answer -- " +
	"'AI Functionality' Output under §6, which §6.5 says Tavily and its AI providers may separately " +
	"retain to train their models -- is ever produced by it."

// searchHorizon is probe's manifest.search_horizon: how far back search
// reaches.
const searchHorizon = "whatever is presently in Tavily's live web index; a query is not bounded to any " +
	"fixed archive. A call with a window narrows it with Tavily's own start_date/end_date and " +
	"filter_by_published_date (spec/tavily/search.md), which also drops any result with no detectable " +
	"published date, since nothing then says it falls in the window; a call with no window leaves every " +
	"result in, dated or not. Tavily's date filters reach as recent as a day or as far as about a year back."

// declineReasonFor is used by buildManifest; kept as a thin wrapper so a
// missing entry fails loudly rather than probe answering half a manifest.
func declineReasonFor(v wp.Verb) string {
	r, ok := declineReasons[v]
	if !ok {
		panic(fmt.Sprintf("tavily: no decline reason recorded for verb %q", v))
	}
	return r
}

// isBlank reports whether a credential was not usably supplied.
func isBlank(s string) bool { return strings.TrimSpace(s) == "" }
