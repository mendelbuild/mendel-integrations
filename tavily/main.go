// Command tavily is the External Tool Wrapper for Tavily
// (dev/claude_plans/35_tools_outside_the_codebase.md §6, §8), written against
// contract 2-draft: a search tool, honouring probe and search and declaring
// every other verb absent, with the reason.
//
// It reads one request from stdin and writes one response to stdout, and
// knows nothing about Mendel beyond that protocol. The API it calls is the one
// cited in README.md beside it, fetched when this was written rather than
// recalled. Standard library and the wire types only.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

// wrapperVersion is this wrapper's own version, recorded as its provenance.
const wrapperVersion = "0.2.0"

// runTimeout bounds one run: a search takes a second or two.
const runTimeout = 45 * time.Second

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	if err := run(ctx, os.Stdin, os.Stdout, http.DefaultClient); err != nil {
		// Nothing from the request is printed: it holds the key.
		fmt.Fprintln(os.Stderr, "tavily:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, in io.Reader, out io.Writer, client *http.Client) error {
	var req wp.WrapperRequest
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return fmt.Errorf("the request is not readable")
	}
	var resp wp.WrapperResponse
	if req.Contract != wp.ContractVersion {
		verb := wp.VerbProbe
		if len(req.Calls) > 0 {
			verb = req.Calls[0].Verb
		}
		resp.Results = append(resp.Results, wp.VerbResult{Verb: verb,
			Refused: fmt.Sprintf("this wrapper speaks contract %q and was sent %q", wp.ContractVersion, req.Contract)})
		return json.NewEncoder(out).Encode(resp)
	}
	a := newAPI(req.Connection, client)
	for _, call := range req.Calls {
		r := answer(ctx, a, call)
		resp.Results = append(resp.Results, r)
		if !r.Succeeded() {
			break
		}
	}
	return json.NewEncoder(out).Encode(resp)
}

func answer(ctx context.Context, a *api, c wp.VerbCall) wp.VerbResult {
	switch c.Verb {
	case wp.VerbProbe:
		return a.probe(ctx)
	case wp.VerbSearch:
		return a.search(ctx, c)
	}
	if s, ok := verbs()[c.Verb]; ok && s.Level == wp.VerbDeclined {
		return wp.VerbResult{Verb: c.Verb, Refused: fmt.Sprintf("this wrapper declares %s absent: %s", c.Verb, s.Reason)}
	}
	return wp.VerbResult{Verb: c.Verb, Refused: fmt.Sprintf("%q is not a verb of contract %s", c.Verb, wp.ContractVersion)}
}
