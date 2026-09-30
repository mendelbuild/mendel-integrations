package wrapperprotocol

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Authorize, the action surface and search: what contract 2 added to a
// data source's contract (doc 35 §6; the wrapper-vetting spike, §19).
//
// Two decisions shape it (§19, 2026-09-28):
//
//   - Authorization is one verb, authorize, with a step, and the wrapper owns
//     it. Each tool's variant of OAuth -- registering a client per instance,
//     signing every request with a bound key, sending the secret in a Basic
//     header -- lives in its wrapper. Mendel's part is generic: host a
//     redirect, show a person a link, keep what comes back, call refresh.
//   - What authorize produces is Credentials, a field that is always secret.
//     Mendel moves it into encrypted storage on arrival and keeps and logs
//     responses only Redacted. ParseWrapperResponse refuses credentials on
//     any other verb, and never quotes a wrapper's output that may hold them.

// Steps of authorize.
//
//   - begin: Mendel sends the redirect URI it hosts and a state it generated;
//     the wrapper answers the URL a person opens, and any credentials it
//     needs kept until complete (a client registered on the person's
//     instance, a PKCE verifier). With credentials from an earlier begin in
//     the connection, a wrapper reuses them rather than registering again.
//   - complete: Mendel sends the code from the redirect, with the connection
//     holding what begin produced; the wrapper answers the credentials every
//     later run needs.
//   - refresh: the wrapper answers new credentials for the ones in the
//     connection. Declared absent where a tool's tokens do not expire.
//   - revoke: the wrapper asks the tool to forget the grant. Retreat is never
//     conditional, so Mendel calls it on disconnecting whatever else holds.
const (
	AuthorizeBegin    = "begin"
	AuthorizeComplete = "complete"
	AuthorizeRefresh  = "refresh"
	AuthorizeRevoke   = "revoke"
)

// OutOfBandRedirect is the redirect URI that asks a tool to show the person
// the code rather than redirect them, where the tool offers it. The
// conformance harness uses it, since it hosts nothing.
const OutOfBandRedirect = "urn:ietf:wg:oauth:2.0:oob"

// When is when publish makes an asset live (§6): now, at an instant, or
// announced (a scheduled notice meant to be seen before it starts).
type When struct {
	Mode string     `json:"mode"`
	At   *time.Time `json:"at,omitempty"`
}

const (
	WhenNow      = "now"
	WhenAt       = "at"
	WhenAnnounce = "announce"
)

// AssetStatus is status's answer (§6): what was configured, what is in
// effect, and any review the tool inserts between the two.
type AssetStatus struct {
	Configured string `json:"configured"`
	Effective  string `json:"effective"`
	Review     string `json:"review,omitempty"`
}

// Effective states an asset can be in.
const (
	EffectiveNotLive = "not_live"
	EffectiveLive    = "live"
	EffectiveGone    = "gone"
)

var effectiveStates = map[string]bool{EffectiveNotLive: true, EffectiveLive: true, EffectiveGone: true}

// Retract outcomes (§6). Retract is safe to call twice: a second call on an
// asset already retracted answers the same outcome.
const (
	RetractedDeleted  = "deleted"
	RetractedPaused   = "paused"
	RetractedArchived = "archived"
	RetractedResolved = "resolved"
)

// FieldReading is one metric of one asset, as read_metrics answers it:
// a value, or the sentence for why there is none, and its quality flags.
type FieldReading struct {
	Value       *float64 `json:"value,omitempty"`
	Unavailable string   `json:"unavailable,omitempty"`
	Quality     []string `json:"quality,omitempty"`
}

// Item is one search result, in the `article` kind's family fields (§7).
type Item struct {
	URL         string     `json:"url"`
	Title       string     `json:"title"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	Source      string     `json:"source,omitempty"`
	Snippet     string     `json:"snippet,omitempty"`
}

// AuthorizationSupport is how a wrapper connects an account: the authorize
// steps it answers, and the scopes it asks the tool for, so a person is told
// what they are granting before they grant it.
type AuthorizationSupport struct {
	Steps  []string `json:"steps"`
	Scopes []string `json:"scopes"`
	// Expires says whether what complete produces lapses, so Mendel knows to
	// refresh; a sentence, or "never".
	Expires string `json:"expires"`
}

// KindSupport is a manifest's answer for one External Asset Kind: supported,
// partial with its caveat, or declined with its reason, and the shape an
// asset of it takes here -- a refinement of the kind's family schema (§7).
type KindSupport struct {
	Level  VerbLevel       `json:"level"`
	Caveat string          `json:"caveat,omitempty"`
	Reason string          `json:"reason,omitempty"`
	Shape  json.RawMessage `json:"shape,omitempty"`
}

// readsNumbers reports whether the manifest honours a verb that reads a
// metric, and so owes a list of them.
func (m *CapabilityManifest) readsNumbers() bool {
	for _, v := range []Verb{VerbReadSeries, VerbReadTotal, VerbReadMetrics} {
		if m.Verbs[v].Level != VerbDeclined {
			return true
		}
	}
	return false
}

// checkSurface is authorize's, the action surface's and search's part of
// Check.
func (m *CapabilityManifest) checkSurface() string {
	if m.Verbs[VerbAuthorize].Level != VerbDeclined {
		a := m.Authorization
		if a == nil {
			return "authorize is honoured and the manifest does not say how (authorization)"
		}
		steps := map[string]bool{}
		for _, s := range a.Steps {
			switch s {
			case AuthorizeBegin, AuthorizeComplete, AuthorizeRefresh, AuthorizeRevoke:
				steps[s] = true
			default:
				return fmt.Sprintf("authorize step %q is not one the contract names", s)
			}
		}
		if !steps[AuthorizeBegin] || !steps[AuthorizeComplete] {
			return "authorize is honoured without both begin and complete"
		}
		if strings.TrimSpace(a.Expires) == "" {
			return "the manifest does not say whether what authorize produces expires"
		}
	}
	acts := m.Verbs[VerbDraft].Level != VerbDeclined || m.Verbs[VerbPublish].Level != VerbDeclined
	if acts && len(m.Kinds) == 0 {
		return "the wrapper acts on assets and names no External Asset Kind it can act on (kinds)"
	}
	names := make([]string, 0, len(m.Kinds))
	for k := range m.Kinds {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		ks := m.Kinds[k]
		switch ks.Level {
		case VerbSupported, VerbPartial:
			if ks.Level == VerbPartial && ks.Caveat == "" {
				return fmt.Sprintf("kind %s is partial without saying what the caveat is", k)
			}
			if len(ks.Shape) == 0 {
				return fmt.Sprintf("kind %s is offered with no shape for an asset of it", k)
			}
		case VerbDeclined:
			if ks.Reason == "" {
				return fmt.Sprintf("kind %s is declined without a reason", k)
			}
		default:
			return fmt.Sprintf("kind %s has level %q; want supported, partial or declined", k, ks.Level)
		}
	}
	if m.Verbs[VerbSearch].Level != VerbDeclined && strings.TrimSpace(m.SearchHorizon) == "" {
		return "search is honoured without saying how far back it reaches (search_horizon)"
	}
	return ""
}

// Redacted is a response as Mendel may keep or log it: every credential's
// value taken out, its name kept, so what was produced can be listed and
// nothing produced can be read back.
func (r WrapperResponse) Redacted() WrapperResponse {
	out := WrapperResponse{Results: make([]VerbResult, len(r.Results))}
	for i, res := range r.Results {
		if len(res.Credentials) > 0 {
			creds := make(map[string]string, len(res.Credentials))
			for name := range res.Credentials {
				creds[name] = ""
			}
			res.Credentials = creds
		}
		out.Results[i] = res
	}
	return out
}

// redactedCalls is calls with each authorization code and state taken out:
// a code is exchanged once for credentials and is as secret as they are, and
// the state is what a redirect is matched by.
func redactedCalls(calls []VerbCall) []VerbCall {
	out := make([]VerbCall, len(calls))
	for i, c := range calls {
		c.Code, c.State = "", ""
		out[i] = c
	}
	return out
}

// Secrets are the values a request carries that must leave a run nowhere but
// in the request itself: every credential, and an authorize call's code and
// state.
func (r WrapperRequest) Secrets() []string {
	var out []string
	for _, v := range r.Connection.Credentials {
		out = append(out, v)
	}
	for _, c := range r.Calls {
		out = append(out, c.Code, c.State)
	}
	return out
}

// Scrub replaces each secret in text a wrapper wrote -- its stderr, quoted in
// a failure a project's members read -- with [redacted]: as written, and in
// the forms a request commonly carries one (URL-escaped, base64). A value
// shorter than four characters is left alone, since replacing it would
// garble the text and it protects nothing. What a wrapper encodes some other
// way it can still leak; this catches the common mistake of logging a request
// or an error that echoes one.
func Scrub(text string, secrets []string) string {
	var forms []string
	for _, s := range secrets {
		if len(s) < 4 {
			continue
		}
		forms = append(forms, s, url.QueryEscape(s), url.PathEscape(s),
			base64.StdEncoding.EncodeToString([]byte(s)), base64.RawStdEncoding.EncodeToString([]byte(s)),
			base64.URLEncoding.EncodeToString([]byte(s)), base64.RawURLEncoding.EncodeToString([]byte(s)))
	}
	// Longest first, so a secret containing another is replaced whole.
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	for _, f := range forms {
		text = strings.ReplaceAll(text, f, "[redacted]")
	}
	return text
}

// quotable is the end of a wrapper's output for an error message, or a note
// that it is not quoted: output that names credentials may hold them.
func quotable(stdout []byte) string {
	if strings.Contains(string(stdout), `"credentials"`) {
		return "(not quoted: it names credentials, which may be in it)"
	}
	return tail(string(stdout), 512)
}

// Purpose is what a verb is for, in one sentence: what a wrapper that
// honours it must do, and so what a wrapper that cannot should decline
// rather than stretch. A manifest is held to these by the conformance suite
// where a check can reach, and a person reviewing a wrapper reads them
// beside its claims. They are the contract's meaning, as the types are its
// form: a generated wrapper keeps the form more reliably than the meaning
// (SPIKE.md findings 14 and 16), so the meaning is written down here.
func (v Verb) Purpose() string { return verbPurposes[v] }

var verbPurposes = map[Verb]string{
	VerbProbe: "Say what this wrapper can do against this account: every verb, metric and kind, with the venue, " +
		"the entitlements observed and what the tool's terms allow to be kept.",
	VerbAuthorize: "Connect an account through the tool's own authorization: begin answers where to send the " +
		"person, complete turns the code they bring back into credentials, refresh renews them, revoke ends the grant.",
	VerbDraft: "Create an asset that is not live -- seen by no one -- which publish later makes live. A tool with " +
		"no such state declines it.",
	VerbPublish: "Make an asset live for its audience: a draft by its ref, or a payload directly where there is no " +
		"draft; now, at an instant, or announced.",
	VerbStatus: "Say what is configured for an asset and what is in effect -- not live, live, or gone -- and any " +
		"review the tool holds it in.",
	VerbAppendUpdate: "Append an update to a log-shaped asset, such as an incident, keeping every earlier update. " +
		"Not an edit: a kind that is not log-shaped declines it.",
	VerbRetract: "Take an asset back -- paused, deleted, archived or resolved -- safely twice, and never by " +
		"destroying the history the tool keeps.",
	VerbReadBack: "Answer the asset as the tool now holds it, in the kind's family fields, to be compared field " +
		"for field with what was approved.",
	VerbReadMetrics: "Answer an asset's metrics, each a value with its quality flags or the sentence for why there " +
		"is none -- never a zero for a number the tool does not have.",
	VerbSetCap: "Set the most an asset may spend -- daily, total, and an end date -- for a kind that involves " +
		"spend; declared a hard cap or a target the tool may exceed.",
	VerbListOwned: "List the assets whose name starts with the prefix given -- Mendel's own, found by the name it " +
		"gave them -- so nothing is created twice. A tool that keeps no name for a kind declines it.",
	VerbReadSeries: "Read a measure over a window as one point per step of the granularity asked, empty steps " +
		"included; a granularity the tool lacks is refused, never substituted.",
	VerbReadTotal: "Read a measure over a window as one number, as its metric's kind allows.",
	VerbSearch: "Find items the tool holds that match a query, within a window where given, up to a limit, " +
		"honestly: a result it cannot place in the window is left out.",
}
