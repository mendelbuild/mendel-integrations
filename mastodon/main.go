// Command mastodon is the External Tool Wrapper for Mastodon
// (dev/claude_plans/35_tools_outside_the_codebase.md §6, §8), written against
// contract 2-draft: it connects an account through the instance's own OAuth
// (authorize), and publishes, reads back, reads the counts of and retracts a
// social_post. Every other verb is declared absent, with the reason.
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

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// wrapperVersion is this wrapper's own version, recorded as its provenance.
const wrapperVersion = "0.1.1"

// runTimeout bounds one run: a handful of HTTP calls.
const runTimeout = 45 * time.Second

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	if err := run(ctx, os.Stdin, os.Stdout, http.DefaultClient); err != nil {
		// Only a request that cannot be read at all ends here; everything
		// else is answered on stdout. Nothing from the request is printed:
		// it holds credentials.
		fmt.Fprintln(os.Stderr, "mastodon:", err)
		os.Exit(1)
	}
}

// run answers one request: each call in order, stopping at the first that
// does not succeed.
func run(ctx context.Context, in io.Reader, out io.Writer, client *http.Client) error {
	var req wp.WrapperRequest
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return fmt.Errorf("the request is not readable")
	}
	var resp wp.WrapperResponse
	if req.Contract != wp.DraftContractVersion {
		verb := wp.VerbProbe
		if len(req.Calls) > 0 {
			verb = req.Calls[0].Verb
		}
		resp.Results = append(resp.Results, wp.VerbResult{Verb: verb,
			Refused: fmt.Sprintf("this wrapper speaks contract %q and was sent %q", wp.DraftContractVersion, req.Contract)})
		return json.NewEncoder(out).Encode(resp)
	}
	api := newAPI(req.Connection, client)
	for _, call := range req.Calls {
		r := answer(ctx, api, call)
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
	case wp.VerbAuthorize:
		return a.authorize(ctx, c)
	case wp.VerbPublish:
		return a.publish(ctx, c)
	case wp.VerbStatus:
		return a.status(ctx, c)
	case wp.VerbReadBack:
		return a.readBack(ctx, c)
	case wp.VerbReadMetrics:
		return a.readMetrics(ctx, c)
	case wp.VerbRetract:
		return a.retract(ctx, c)
	}
	if s, ok := verbs()[c.Verb]; ok && s.Level == wp.VerbDeclined {
		return wp.VerbResult{Verb: c.Verb, Refused: fmt.Sprintf("this wrapper declares %s absent: %s", c.Verb, s.Reason)}
	}
	return wp.VerbResult{Verb: c.Verb, Refused: fmt.Sprintf("%q is not a verb of contract %s", c.Verb, wp.DraftContractVersion)}
}
