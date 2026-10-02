// Command mailchimp is the External Tool Wrapper for Mailchimp
// (https://mailchimp.com): it sends scheduled reminder emails ("regular"
// campaigns, the contract's email_broadcast kind) to an audience the
// project owns, through the Mailchimp Marketing API v3.
//
// It speaks the wire protocol in wrapperprotocol: one JSON request on
// stdin, one JSON response on stdout, nothing else. See GUIDE.md and
// mailchimp/README.md for what it was written against.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

func main() {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mailchimp: reading the request:", err)
		os.Exit(1)
	}
	var req wp.WrapperRequest
	if err := json.Unmarshal(data, &req); err != nil {
		fmt.Fprintln(os.Stderr, "mailchimp: the request is not readable as JSON:", err)
		os.Exit(1)
	}
	resp := handle(context.Background(), req)
	out, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mailchimp: could not write the response:", err)
		os.Exit(1)
	}
	os.Stdout.Write(out)
}

// handle answers every call in req, in order, stopping at the first that
// does not succeed, as the protocol requires (GUIDE.md "The protocol").
func handle(ctx context.Context, req wp.WrapperRequest) wp.WrapperResponse {
	if len(req.Calls) == 0 {
		return wp.WrapperResponse{}
	}
	if req.Contract != wp.ContractVersion {
		return wp.WrapperResponse{Results: []wp.VerbResult{{
			Verb: req.Calls[0].Verb,
			Failed: fmt.Sprintf("this wrapper speaks contract %q; the request is for %q",
				wp.ContractVersion, req.Contract),
		}}}
	}

	w, err := newWrapper(req.Connection)
	if err != nil {
		// Every call fails the same way: there is no credential to act with.
		return wp.WrapperResponse{Results: []wp.VerbResult{{Verb: req.Calls[0].Verb, Failed: err.Error()}}}
	}

	var results []wp.VerbResult
	for _, call := range req.Calls {
		res := w.dispatch(ctx, call)
		results = append(results, res)
		if !res.Succeeded() {
			break
		}
	}
	return wp.WrapperResponse{Results: results}
}
