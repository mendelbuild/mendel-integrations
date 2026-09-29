package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// mux is a tiny method+path router for a fake Mastodon instance, so each
// test says only the endpoints it needs and fails loudly if this wrapper
// calls anything it did not expect.
type mux struct {
	t      *testing.T
	routes map[string]http.HandlerFunc
}

func newMux(t *testing.T) *mux { return &mux{t: t, routes: map[string]http.HandlerFunc{}} }

func (m *mux) handle(method, path string, fn http.HandlerFunc) *mux {
	m.routes[method+" "+path] = fn
	return m
}

func (m *mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path
	fn, ok := m.routes[key]
	if !ok {
		m.t.Fatalf("unexpected request %s (query %q)", key, r.URL.RawQuery)
		return
	}
	fn(w, r)
}

func jsonHandler(status int, body any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
}

func conn(server *httptest.Server, creds map[string]string) wp.Connection {
	return wp.Connection{
		AccountID:   "example.social",
		Endpoint:    server.URL,
		Credentials: creds,
	}
}

func TestProbe(t *testing.T) {
	m := newMux(t)
	m.handle("GET", "/api/v1/accounts/verify_credentials", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("Authorization header = %q", got)
		}
		w.Header().Set("X-RateLimit-Limit", "300")
		w.Header().Set("X-RateLimit-Remaining", "299")
		jsonHandler(200, mstAccount{ID: "1", Username: "mendel", StatusesCount: 12, FollowersCount: 3})(w, r)
	})
	terms := "https://example.social/terms"
	m.handle("GET", "/api/v2/instance", jsonHandler(200, map[string]any{
		"configuration": map[string]any{"urls": map[string]any{"terms_of_service": terms}},
	}))
	server := httptest.NewServer(m)
	defer server.Close()

	resp := run(context.Background(), wp.WrapperRequest{
		Contract:   wp.ContractVersion,
		Connection: conn(server, map[string]string{credAccessToken: "tok"}),
		Calls:      []wp.VerbCall{{Verb: wp.VerbProbe}},
	})
	if len(resp.Results) != 1 || !resp.Results[0].Succeeded() {
		t.Fatalf("probe did not succeed: %+v", resp)
	}
	manifest := resp.Results[0].Manifest
	if manifest == nil {
		t.Fatal("no manifest")
	}
	if why := manifest.Check(); why != "" {
		t.Fatalf("manifest.Check(): %s", why)
	}
	if !strings.Contains(manifest.StoragePolicy, terms) {
		t.Fatalf("storage policy does not cite the terms URL: %s", manifest.StoragePolicy)
	}
	if manifest.Entitlements["requests_per_5min"] != 300 {
		t.Fatalf("entitlements did not read the rate limit header: %+v", manifest.Entitlements)
	}
	if !manifest.IsDataSource() == true {
		// this wrapper writes, so it must NOT report as a pure data source.
	}
	if manifest.IsDataSource() {
		t.Fatal("a wrapper that publishes and retracts should not report IsDataSource")
	}
}

func TestProbeNoToken(t *testing.T) {
	server := httptest.NewServer(newMux(t))
	defer server.Close()
	resp := run(context.Background(), wp.WrapperRequest{
		Contract:   wp.ContractVersion,
		Connection: conn(server, nil),
		Calls:      []wp.VerbCall{{Verb: wp.VerbProbe}},
	})
	if resp.Results[0].Failed == "" {
		t.Fatalf("expected probe without a token to fail, got %+v", resp.Results[0])
	}
}

func TestAuthorizeBeginRegistersApplication(t *testing.T) {
	m := newMux(t)
	m.handle("POST", "/api/v1/apps", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["redirect_uris"] != "https://mendel.example/cb" {
			t.Fatalf("redirect_uris = %v", body["redirect_uris"])
		}
		jsonHandler(200, mstApplication{ClientID: "cid", ClientSecret: "csec"})(w, r)
	})
	server := httptest.NewServer(m)
	defer server.Close()

	resp := run(context.Background(), wp.WrapperRequest{
		Contract:   wp.ContractVersion,
		Connection: conn(server, nil),
		Calls: []wp.VerbCall{{
			Verb: wp.VerbAuthorize, Step: wp.AuthorizeBegin,
			RedirectURI: "https://mendel.example/cb", State: "xyz",
		}},
	})
	res := resp.Results[0]
	if !res.Succeeded() {
		t.Fatalf("begin did not succeed: %+v", res)
	}
	if res.Credentials[credClientID] != "cid" || res.Credentials[credClientSecret] != "csec" {
		t.Fatalf("begin did not return the registered client: %+v", res.Credentials)
	}
	if !strings.Contains(res.AuthorizeURL, "client_id=cid") || !strings.Contains(res.AuthorizeURL, "state=xyz") {
		t.Fatalf("authorize URL missing client_id or state: %s", res.AuthorizeURL)
	}
}

func TestAuthorizeBeginReusesRegisteredApplication(t *testing.T) {
	server := httptest.NewServer(newMux(t)) // no /api/v1/apps route: a call there fails the test
	defer server.Close()

	resp := run(context.Background(), wp.WrapperRequest{
		Contract:   wp.ContractVersion,
		Connection: conn(server, map[string]string{credClientID: "cid", credClientSecret: "csec"}),
		Calls: []wp.VerbCall{{
			Verb: wp.VerbAuthorize, Step: wp.AuthorizeBegin, RedirectURI: wp.OutOfBandRedirect,
		}},
	})
	res := resp.Results[0]
	if !res.Succeeded() {
		t.Fatalf("begin did not succeed: %+v", res)
	}
	if !strings.Contains(res.AuthorizeURL, "client_id=cid") {
		t.Fatalf("authorize URL did not reuse the existing client: %s", res.AuthorizeURL)
	}
}

func TestAuthorizeComplete(t *testing.T) {
	m := newMux(t)
	m.handle("POST", "/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["code"] != "authcode" || body["client_id"] != "cid" {
			t.Fatalf("token exchange body = %+v", body)
		}
		jsonHandler(200, mstToken{AccessToken: "acctok"})(w, r)
	})
	server := httptest.NewServer(m)
	defer server.Close()

	resp := run(context.Background(), wp.WrapperRequest{
		Contract:   wp.ContractVersion,
		Connection: conn(server, map[string]string{credClientID: "cid", credClientSecret: "csec"}),
		Calls: []wp.VerbCall{{
			Verb: wp.VerbAuthorize, Step: wp.AuthorizeComplete, Code: "authcode",
		}},
	})
	res := resp.Results[0]
	if !res.Succeeded() {
		t.Fatalf("complete did not succeed: %+v", res)
	}
	if res.Credentials[credAccessToken] != "acctok" {
		t.Fatalf("complete did not return the access token: %+v", res.Credentials)
	}
}

func TestAuthorizeRevoke(t *testing.T) {
	m := newMux(t)
	m.handle("POST", "/oauth/revoke", jsonHandler(200, map[string]any{}))
	server := httptest.NewServer(m)
	defer server.Close()

	resp := run(context.Background(), wp.WrapperRequest{
		Contract: wp.ContractVersion,
		Connection: conn(server, map[string]string{
			credAccessToken: "acctok", credClientID: "cid", credClientSecret: "csec",
		}),
		Calls: []wp.VerbCall{{Verb: wp.VerbAuthorize, Step: wp.AuthorizeRevoke}},
	})
	if !resp.Results[0].Succeeded() {
		t.Fatalf("revoke did not succeed: %+v", resp.Results[0])
	}
}

func TestAuthorizeUnknownStepIsRefused(t *testing.T) {
	server := httptest.NewServer(newMux(t))
	defer server.Close()
	resp := run(context.Background(), wp.WrapperRequest{
		Contract:   wp.ContractVersion,
		Connection: conn(server, nil),
		Calls:      []wp.VerbCall{{Verb: wp.VerbAuthorize, Step: wp.AuthorizeRefresh}},
	})
	if resp.Results[0].Refused == "" {
		t.Fatalf("expected refresh to be refused, got %+v", resp.Results[0])
	}
}

func mustPayload(t *testing.T, post socialPost) []byte {
	t.Helper()
	raw, err := json.Marshal(post)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func strPtr(s string) *string { return &s }

func TestPublish(t *testing.T) {
	m := newMux(t)
	m.handle("POST", "/api/v1/statuses", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		status, _ := body["status"].(string)
		if !strings.Contains(status, "hello") || !strings.Contains(status, "https://example.com/x") {
			t.Fatalf("posted status = %q", status)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "key-1" {
			t.Fatalf("Idempotency-Key = %q", got)
		}
		jsonHandler(200, mstStatus{ID: "42", URL: "https://example.social/@mendel/42"})(w, r)
	})
	server := httptest.NewServer(m)
	defer server.Close()

	payload := mustPayload(t, socialPost{Text: "hello", Link: strPtr("https://example.com/x")})
	resp := run(context.Background(), wp.WrapperRequest{
		Contract:   wp.ContractVersion,
		Connection: conn(server, map[string]string{credAccessToken: "tok"}),
		Calls: []wp.VerbCall{{
			Verb: wp.VerbPublish, AssetKind: socialPostKind, Payload: payload,
			When: &wp.When{Mode: wp.WhenNow}, IdempotencyKey: "key-1",
		}},
	})
	res := resp.Results[0]
	if !res.Succeeded() {
		t.Fatalf("publish did not succeed: %+v", res)
	}
	if res.Ref != "42" || res.URL != "https://example.social/@mendel/42" {
		t.Fatalf("publish result = %+v", res)
	}
}

func TestPublishRefusesScheduling(t *testing.T) {
	server := httptest.NewServer(newMux(t)) // no routes: scheduling must not reach the network
	defer server.Close()
	at := time.Now().Add(time.Hour)
	payload := mustPayload(t, socialPost{Text: "hello"})
	resp := run(context.Background(), wp.WrapperRequest{
		Contract:   wp.ContractVersion,
		Connection: conn(server, map[string]string{credAccessToken: "tok"}),
		Calls: []wp.VerbCall{{
			Verb: wp.VerbPublish, Payload: payload, When: &wp.When{Mode: wp.WhenAt, At: &at},
		}},
	})
	if resp.Results[0].Refused == "" {
		t.Fatalf("expected scheduled publish to be refused, got %+v", resp.Results[0])
	}
}

func TestAppendUpdate(t *testing.T) {
	m := newMux(t)
	m.handle("PUT", "/api/v1/statuses/42", func(w http.ResponseWriter, r *http.Request) {
		jsonHandler(200, mstStatus{ID: "42", URL: "https://example.social/@mendel/42"})(w, r)
	})
	server := httptest.NewServer(m)
	defer server.Close()

	payload := mustPayload(t, socialPost{Text: "edited"})
	resp := run(context.Background(), wp.WrapperRequest{
		Contract:   wp.ContractVersion,
		Connection: conn(server, map[string]string{credAccessToken: "tok"}),
		Calls:      []wp.VerbCall{{Verb: wp.VerbAppendUpdate, Ref: "42", Payload: payload}},
	})
	if !resp.Results[0].Succeeded() {
		t.Fatalf("append_update did not succeed: %+v", resp.Results[0])
	}
}

func TestRetractIsIdempotent(t *testing.T) {
	calls := 0
	m := newMux(t)
	m.handle("DELETE", "/api/v1/statuses/42", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			jsonHandler(200, mstStatus{ID: "42"})(w, r)
		} else {
			jsonHandler(404, mstError{Error: "Record not found"})(w, r)
		}
	})
	server := httptest.NewServer(m)
	defer server.Close()

	for i := 0; i < 2; i++ {
		resp := run(context.Background(), wp.WrapperRequest{
			Contract:   wp.ContractVersion,
			Connection: conn(server, map[string]string{credAccessToken: "tok"}),
			Calls:      []wp.VerbCall{{Verb: wp.VerbRetract, Ref: "42"}},
		})
		res := resp.Results[0]
		if !res.Succeeded() || res.Retracted != wp.RetractedDeleted {
			t.Fatalf("retract call %d = %+v", i, res)
		}
	}
}

func TestStatusGone(t *testing.T) {
	m := newMux(t)
	m.handle("GET", "/api/v1/statuses/42", jsonHandler(404, mstError{Error: "Record not found"}))
	server := httptest.NewServer(m)
	defer server.Close()

	resp := run(context.Background(), wp.WrapperRequest{
		Contract:   wp.ContractVersion,
		Connection: conn(server, map[string]string{credAccessToken: "tok"}),
		Calls:      []wp.VerbCall{{Verb: wp.VerbStatus, Ref: "42"}},
	})
	res := resp.Results[0]
	if !res.Succeeded() || res.Status == nil || res.Status.Effective != wp.EffectiveGone {
		t.Fatalf("status of a gone post = %+v", res)
	}
}

func TestStatusLive(t *testing.T) {
	m := newMux(t)
	m.handle("GET", "/api/v1/statuses/42", jsonHandler(200, mstStatus{ID: "42", Visibility: "public"}))
	server := httptest.NewServer(m)
	defer server.Close()

	resp := run(context.Background(), wp.WrapperRequest{
		Contract:   wp.ContractVersion,
		Connection: conn(server, map[string]string{credAccessToken: "tok"}),
		Calls:      []wp.VerbCall{{Verb: wp.VerbStatus, Ref: "42"}},
	})
	res := resp.Results[0]
	if !res.Succeeded() || res.Status == nil || res.Status.Effective != wp.EffectiveLive || res.Status.Configured != "public" {
		t.Fatalf("status of a live post = %+v", res)
	}
}

func TestReadBack(t *testing.T) {
	m := newMux(t)
	m.handle("GET", "/api/v1/statuses/42/source", jsonHandler(200, mstStatusSource{ID: "42", Text: "hello world"}))
	m.handle("GET", "/api/v1/statuses/42", jsonHandler(200, mstStatus{
		ID: "42", Card: &mstCard{URL: "https://example.com/x"},
		MediaAttachments: []mstMediaAttachment{{Description: "a cat"}},
	}))
	server := httptest.NewServer(m)
	defer server.Close()

	resp := run(context.Background(), wp.WrapperRequest{
		Contract:   wp.ContractVersion,
		Connection: conn(server, map[string]string{credAccessToken: "tok"}),
		Calls:      []wp.VerbCall{{Verb: wp.VerbReadBack, Ref: "42"}},
	})
	res := resp.Results[0]
	if !res.Succeeded() {
		t.Fatalf("read_back did not succeed: %+v", res)
	}
	var post socialPost
	if err := json.Unmarshal(res.Asset, &post); err != nil {
		t.Fatal(err)
	}
	if post.Text != "hello world" || post.Link == nil || *post.Link != "https://example.com/x" ||
		post.Media == nil || *post.Media != "a cat" {
		t.Fatalf("read_back asset = %+v", post)
	}
}

func TestReadMetrics(t *testing.T) {
	m := newMux(t)
	m.handle("GET", "/api/v1/statuses/42", jsonHandler(200, mstStatus{
		ID: "42", FavouritesCount: 5, ReblogsCount: 2, RepliesCount: 1, QuotesCount: 3,
	}))
	server := httptest.NewServer(m)
	defer server.Close()

	resp := run(context.Background(), wp.WrapperRequest{
		Contract:   wp.ContractVersion,
		Connection: conn(server, map[string]string{credAccessToken: "tok"}),
		Calls:      []wp.VerbCall{{Verb: wp.VerbReadMetrics, Ref: "42"}},
	})
	res := resp.Results[0]
	if !res.Succeeded() {
		t.Fatalf("read_metrics did not succeed: %+v", res)
	}
	if *res.AssetMetrics["favourites_count"].Value != 5 || *res.AssetMetrics["quotes_count"].Value != 3 {
		t.Fatalf("read_metrics = %+v", res.AssetMetrics)
	}
}

func TestListOwned(t *testing.T) {
	m := newMux(t)
	m.handle("GET", "/api/v1/accounts/verify_credentials", jsonHandler(200, mstAccount{ID: "1"}))
	m.handle("GET", "/api/v1/accounts/1/statuses", jsonHandler(200, []mstStatus{{ID: "43"}, {ID: "42"}}))
	server := httptest.NewServer(m)
	defer server.Close()

	resp := run(context.Background(), wp.WrapperRequest{
		Contract:   wp.ContractVersion,
		Connection: conn(server, map[string]string{credAccessToken: "tok"}),
		Calls:      []wp.VerbCall{{Verb: wp.VerbListOwned}},
	})
	res := resp.Results[0]
	if !res.Succeeded() || len(res.Owned) != 2 || res.Owned[0] != "43" {
		t.Fatalf("list_owned = %+v", res)
	}
}

func TestDeclinedVerbsAreRefused(t *testing.T) {
	server := httptest.NewServer(newMux(t)) // no routes: a declined verb must never reach the network
	defer server.Close()
	for _, verb := range []wp.Verb{wp.VerbDraft, wp.VerbReadSeries, wp.VerbReadTotal, wp.VerbSearch, wp.VerbSetCap} {
		resp := run(context.Background(), wp.WrapperRequest{
			Contract:   wp.ContractVersion,
			Connection: conn(server, map[string]string{credAccessToken: "tok"}),
			Calls:      []wp.VerbCall{{Verb: verb}},
		})
		if resp.Results[0].Refused == "" {
			t.Fatalf("verb %s: expected refused, got %+v", verb, resp.Results[0])
		}
	}
}

func TestContractMismatchFailsAtOnce(t *testing.T) {
	resp := run(context.Background(), wp.WrapperRequest{
		Contract: "1",
		Calls:    []wp.VerbCall{{Verb: wp.VerbProbe}},
	})
	if len(resp.Results) != 1 || resp.Results[0].Failed == "" {
		t.Fatalf("expected one failed result for a contract mismatch, got %+v", resp)
	}
}

func TestStopsAtFirstFailure(t *testing.T) {
	server := httptest.NewServer(newMux(t)) // probe has no token and fails; retract must never be called
	defer server.Close()
	resp := run(context.Background(), wp.WrapperRequest{
		Contract:   wp.ContractVersion,
		Connection: conn(server, nil),
		Calls:      []wp.VerbCall{{Verb: wp.VerbProbe}, {Verb: wp.VerbRetract, Ref: "1"}},
	})
	if len(resp.Results) != 1 {
		t.Fatalf("expected to stop after the first failing result, got %d results", len(resp.Results))
	}
}

// --- wrapper.json agrees with what the wrapper answers ---

func TestWrapperJSONAgreesWithProbe(t *testing.T) {
	raw, err := os.ReadFile("wrapper.json")
	if err != nil {
		t.Fatal(err)
	}
	var desc wp.Description
	if err := json.Unmarshal(raw, &desc); err != nil {
		t.Fatal(err)
	}
	if why := desc.Check(); why != "" {
		t.Fatalf("wrapper.json: %s", why)
	}
	if desc.Version != wrapperVersion {
		t.Fatalf("wrapper.json version %q does not match the wrapper's own %q", desc.Version, wrapperVersion)
	}
	if desc.Contract != wp.ContractVersion {
		t.Fatalf("wrapper.json contract %q does not match the protocol's %q", desc.Contract, wp.ContractVersion)
	}

	m := buildManifest(mstAccount{}, map[string]any{}, "cited for the test")
	if why := m.Check(); why != "" {
		t.Fatalf("manifest.Check(): %s", why)
	}
	for verb, level := range desc.Claims {
		support, ok := m.Verbs[wp.Verb(verb)]
		if !ok {
			t.Fatalf("wrapper.json claims %s, which the probe does not answer for at all", verb)
		}
		if level != "declined" && support.Level == wp.VerbDeclined {
			t.Fatalf("wrapper.json claims %s as %s, and the probe declines it: %s", verb, level, support.Reason)
		}
	}
}

func TestDockerfileEntrypointMatchesCommand(t *testing.T) {
	raw, err := os.ReadFile("wrapper.json")
	if err != nil {
		t.Fatal(err)
	}
	var desc wp.Description
	if err := json.Unmarshal(raw, &desc); err != nil {
		t.Fatal(err)
	}
	dockerfile, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	wantEntrypoint := "ENTRYPOINT ["
	for i, part := range desc.Command {
		if i > 0 {
			wantEntrypoint += ", "
		}
		wantEntrypoint += "\"" + part + "\""
	}
	wantEntrypoint += "]"
	if !strings.Contains(string(dockerfile), wantEntrypoint) {
		t.Fatalf("Dockerfile does not contain %q to match wrapper.json's command %v", wantEntrypoint, desc.Command)
	}
}
