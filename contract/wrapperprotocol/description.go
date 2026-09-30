package wrapperprotocol

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"regexp"
)

// How a wrapper describes itself: its wrapper.json.
//
// A wrapper lives outside this repository -- in the mendel-integrations
// repository, or wherever its author keeps it -- as a container of its own,
// with a wrapper.json beside its code: the tool it wraps, its version, the
// tag its Dockerfile builds, the command the image runs, the connection a
// project must supply, and the verbs it claims. A Mendel admin registers
// that file with the image pinned by digest (mendel-tool tools register, or
// the External Tools tab), and the server reads the registry and never names
// a tool. Nothing about any tool is in Mendel's repository or its binaries;
// a test that needs a wrapper builds one.
//
// Claims are shortlisting evidence and nothing more (doc 35 §9): what a
// wrapper can do against a project's account is what its probe demonstrates,
// and a wrapper's own tests should hold its probe to claiming no less than
// this file.

// Description is one wrapper.json.
type Description struct {
	Tool     Tool   `json:"tool"`
	Version  string `json:"version"`
	Contract string `json:"contract"`
	// Image is the tag the wrapper's Dockerfile builds: a label for the
	// admin's docker, never what runs. What runs is the digest it is
	// registered with.
	Image string `json:"image"`
	// Command is what the image runs: its Dockerfile's ENTRYPOINT, as a
	// list. A Job in a project's cluster runs it through Mendel's shim,
	// which replaces the entrypoint and so has to be told what it was
	// (doc 35 §19). A wrapper's own test should hold the two to agree.
	Command    []string          `json:"command"`
	SpecSource string            `json:"spec_source"`
	Connection ConnectionSpec    `json:"connection"`
	Claims     map[string]string `json:"claims"`
	// Prices are what the tool charges for a call that succeeds, verb by
	// verb, where it charges per call (a search tool's credits). Declared
	// here, versioned with the wrapper, so Mendel can put a price on a call
	// without naming any tool in its own code (doc 35 §20 stream F).
	Prices []CallPrice `json:"prices,omitempty"`
}

// CallPrice is the list price of one call of a verb that succeeds.
type CallPrice struct {
	Verb     Verb    `json:"verb"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	// Plan is the plan the list price is for, in the tool's own words.
	Plan string `json:"plan"`
	// Source is where the tool publishes the price.
	Source string `json:"source"`
	// PlanSetting names a connection.config setting in which a project
	// states its own price per call on its plan, overriding Amount when
	// set. A value Mendel cannot read as a non-negative decimal makes the
	// spend unknown, never zero.
	PlanSetting string `json:"plan_setting,omitempty"`
}

// Tool is the External Tool a wrapper is for.
type Tool struct {
	Slug       string   `json:"slug"`
	Name       string   `json:"name"`
	Homepage   string   `json:"homepage"`
	Categories []string `json:"categories"`
}

// ConnectionSpec is what a project supplies to connect: the tool's own handle
// on the account, the credentials Mendel holds encrypted for it, and an API
// base URL where the project's instance is not the tool's hosted one. It is
// what the connect form is built from, and what a run injects.
type ConnectionSpec struct {
	Account     *Field  `json:"account,omitempty"`
	Credentials []Field `json:"credentials"`
	Endpoint    *Field  `json:"endpoint,omitempty"`
	// Authorize says the credentials are produced by the authorize verb
	// (contract 2) rather than typed by a person: the connect form offers a
	// link to the tool instead of fields, and the names above are what
	// authorize is expected to answer.
	Authorize bool `json:"authorize,omitempty"`
	// Config is every setting the wrapper reads from a run's
	// Connection.Config: the project's own choices for the tool (doc 35 §6:
	// "a setting is config"), such as who sees a post. Declared so Mendel can
	// ask for them, show them where a person approves what they govern, and
	// refuse a value the wrapper does not take; a setting not declared here
	// is never sent (SPIKE.md finding 9).
	Config []Setting `json:"config,omitempty"`
}

// Setting is one setting of a connection's config.
type Setting struct {
	// Name is the key the wrapper reads it by in Connection.Config.
	Name  string `json:"name"`
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
	// Values are the only values the wrapper accepts, when it accepts only
	// some; empty means any text.
	Values []string `json:"values,omitempty"`
	// Default is what the wrapper does when the setting is not set, said so
	// a person knows what they get by leaving it; one of Values when those
	// are given.
	Default string `json:"default,omitempty"`
}

// CheckConfig says what is wrong with a project's config for this
// connection, or "": a setting the wrapper does not declare, or a value it
// does not take.
func (c ConnectionSpec) CheckConfig(config map[string]any) string {
	declared := map[string]Setting{}
	for _, s := range c.Config {
		declared[s.Name] = s
	}
	names := make([]string, 0, len(config))
	for name := range config {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s, ok := declared[name]
		if !ok {
			return fmt.Sprintf("the wrapper takes no setting %q", name)
		}
		v, isText := config[name].(string)
		if !isText {
			return fmt.Sprintf("setting %s is not text", name)
		}
		if len(s.Values) > 0 && !contains(s.Values, v) {
			return fmt.Sprintf("setting %s is %q; the wrapper takes %s", name, v, strings.Join(s.Values, ", "))
		}
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Field is one value a person enters to connect.
type Field struct {
	// Name is set for a credential only: the project_env_vars row it is held
	// in, encrypted, and the key it arrives under in a run's connection.
	Name        string `json:"name,omitempty"`
	Label       string `json:"label"`
	Help        string `json:"help,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	// Public marks a credential that is an identifier rather than a secret:
	// one the protocol itself puts where people can see it, as OAuth puts a
	// client id in the URL a person opens. Everything else is secret, which
	// is the default. Mendel holds a public credential with the rest; what
	// changes is that finding it in a wrapper's output is not a leak.
	Public bool `json:"public,omitempty"`
}

var (
	slugPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	credentialPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	settingPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	// currencyPattern is an ISO 4217 code's shape: three upper-case letters.
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	claimLevels       = map[string]bool{"supported": true, "partial": true, "declined": true}
)

// Check says what is wrong with a wrapper.json, or "".
func (d Description) Check() string {
	switch {
	case !slugPattern.MatchString(d.Tool.Slug):
		return fmt.Sprintf("tool.slug %q is not a lower-case slug", d.Tool.Slug)
	case d.Tool.Name == "":
		return "tool.name is empty"
	case d.Version == "":
		return "version is empty"
	case d.Contract == "":
		return "contract is empty"
	case d.Image == "":
		return "image is empty"
	case len(d.Command) == 0:
		return "command is empty; say what the image runs (its Dockerfile's ENTRYPOINT), since a Job in a " +
			"project's cluster runs it through Mendel's shim rather than by its entrypoint"
	case len(d.Connection.Credentials) == 0 && d.Connection.Account == nil:
		return "connection asks for nothing; a project could not say which account is its own"
	case len(d.Claims) == 0:
		return "claims is empty; a wrapper that claims nothing cannot be offered for anything"
	case d.Connection.Authorize && d.Claims[string(VerbAuthorize)] == "":
		return "connection.authorize is set and authorize is not claimed; say how the credentials are produced"
	}
	for i, part := range d.Command {
		if part == "" {
			return fmt.Sprintf("command's part %d is empty", i)
		}
	}
	seen := map[string]bool{}
	for _, c := range d.Connection.Credentials {
		if !credentialPattern.MatchString(c.Name) {
			return fmt.Sprintf("credential name %q is not an upper-case environment variable name", c.Name)
		}
		if seen[c.Name] {
			return fmt.Sprintf("credential %q is asked for twice", c.Name)
		}
		seen[c.Name] = true
		if c.Label == "" {
			return fmt.Sprintf("credential %q has no label to show beside its field", c.Name)
		}
	}
	settings := map[string]bool{}
	for _, s := range d.Connection.Config {
		switch {
		case !settingPattern.MatchString(s.Name):
			return fmt.Sprintf("config setting %q is not a lower-case name", s.Name)
		case settings[s.Name]:
			return fmt.Sprintf("config setting %q is declared twice", s.Name)
		case s.Label == "":
			return fmt.Sprintf("config setting %q has no label to show beside it", s.Name)
		case len(s.Values) > 0 && s.Default != "" && !contains(s.Values, s.Default):
			return fmt.Sprintf("config setting %q defaults to %q, which is not one of its values", s.Name, s.Default)
		}
		settings[s.Name] = true
		seen := map[string]bool{}
		for _, v := range s.Values {
			if v == "" || seen[v] {
				return fmt.Sprintf("config setting %q lists an empty or repeated value", s.Name)
			}
			seen[v] = true
		}
	}
	for verb, level := range d.Claims {
		if !claimLevels[level] {
			return fmt.Sprintf("claim %q for %s is not supported, partial or declined", level, verb)
		}
	}
	priced := map[Verb]bool{}
	for _, p := range d.Prices {
		switch {
		case !isContractVerb(p.Verb):
			return fmt.Sprintf("a price is declared for %q, which is not a verb of the contract", p.Verb)
		case priced[p.Verb]:
			return fmt.Sprintf("%s is priced twice", p.Verb)
		case d.Claims[string(p.Verb)] != "supported" && d.Claims[string(p.Verb)] != "partial":
			return fmt.Sprintf("%s is priced and not claimed supported or partial", p.Verb)
		case !(p.Amount >= 0) || math.IsInf(p.Amount, 0):
			return fmt.Sprintf("the price of %s is %v; a price is a non-negative number", p.Verb, p.Amount)
		case !currencyPattern.MatchString(p.Currency):
			return fmt.Sprintf("the price of %s is in %q; name the currency by its ISO 4217 code, e.g. USD", p.Verb, p.Currency)
		case strings.TrimSpace(p.Plan) == "":
			return fmt.Sprintf("the price of %s does not say which plan it is for", p.Verb)
		case strings.TrimSpace(p.Source) == "":
			return fmt.Sprintf("the price of %s does not say where the tool publishes it", p.Verb)
		case p.PlanSetting != "" && !settings[p.PlanSetting]:
			return fmt.Sprintf("the price of %s is overridden by setting %q, which connection.config does not declare",
				p.Verb, p.PlanSetting)
		}
		priced[p.Verb] = true
	}
	return ""
}
