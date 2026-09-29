package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// probe answers what the wrapper can do against this account, having proved
// the credential works. Per GUIDE.md "Writing one" (§2), it uses the
// cheapest call that proves the credential can read the account: Tavily's
// GET /usage, which spec/tavily/rate-limits.md meters on its own separate
// (and generous) limit and spec/tavily/api-credits.md never lists a credit
// cost for, unlike /search which spends real credits on every call.
func (w *Wrapper) probe(conn Connection) VerbResult {
	key, ok := conn.Credentials[credentialName]
	if !ok || isBlank(key) {
		return VerbResult{Failed: fmt.Sprintf("the connection has no %s credential", credentialName)}
	}

	status, raw, err := w.call(http.MethodGet, "/usage", nil, key)
	if err != nil {
		return VerbResult{Failed: fmt.Sprintf("calling Tavily's usage endpoint: %v", err)}
	}
	if status != http.StatusOK {
		return VerbResult{Failed: fmt.Sprintf("Tavily rejected the API key (%d %s): %s",
			status, http.StatusText(status), apiErrorMessage(raw))}
	}

	var usage struct {
		Key     map[string]any `json:"key"`
		Account map[string]any `json:"account"`
	}
	if err := json.Unmarshal(raw, &usage); err != nil {
		return VerbResult{Failed: fmt.Sprintf("Tavily's usage response is not readable JSON: %v", err)}
	}

	return VerbResult{Manifest: buildManifest(usage.Key, usage.Account)}
}

// buildManifest is probe's answer once the credential is known good: every
// verb accounted for, and the entitlements /usage reported.
func buildManifest(key, account map[string]any) *wp.CapabilityManifest {
	verbs := map[wp.Verb]wp.VerbSupport{
		wp.VerbProbe:  {Level: wp.VerbSupported},
		wp.VerbSearch: {Level: wp.VerbSupported},
	}
	for _, v := range wp.VerbsOf(wp.DraftContractVersion) {
		if _, done := verbs[v]; done {
			continue
		}
		verbs[v] = wp.VerbSupport{Level: wp.VerbDeclined, Reason: declineReasonFor(v)}
	}

	entitlements := map[string]any{
		"key":     key,
		"account": account,
		"rate_limit_rpm": "100 requests/minute for a Development key, 1,000 for a Production key " +
			"(spec/tavily/rate-limits.md, fetched 2026-09-29); GET /usage does not say which kind this key is",
	}

	return &wp.CapabilityManifest{
		Contract: wp.DraftContractVersion,
		Wrapper: wp.WrapperProvenance{
			Version:    Version,
			SpecSource: SpecSource,
			SpecHash:   SpecHash,
		},
		Verbs:            verbs,
		Metrics:          nil,
		Granularities:    nil,
		FilterDimensions: nil,
		Venue:            "read_only",
		Idempotency: "none: Tavily ranks and returns results fresh on every call; there is no idempotency " +
			"key and no guarantee that the same query returns the same results later",
		Entitlements:  entitlements,
		StoragePolicy: storagePolicy,
		SearchHorizon: searchHorizon,
	}
}
