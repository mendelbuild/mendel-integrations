package wrapperprotocol

import (
	"fmt"
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
	// (contract 2-draft) rather than typed by a person: the connect form
	// offers a link to the tool instead of fields, and the names above are
	// what authorize is expected to answer.
	Authorize bool `json:"authorize,omitempty"`
}

// Field is one value a person enters to connect.
type Field struct {
	// Name is set for a credential only: the project_env_vars row it is held
	// in, encrypted, and the key it arrives under in a run's connection.
	Name        string `json:"name,omitempty"`
	Label       string `json:"label"`
	Help        string `json:"help,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

var (
	slugPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	credentialPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
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
	for verb, level := range d.Claims {
		if !claimLevels[level] {
			return fmt.Sprintf("claim %q for %s is not supported, partial or declined", level, verb)
		}
	}
	return ""
}
