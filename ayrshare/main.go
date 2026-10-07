// Command ayrshare is Mendel's External Tool Wrapper for Ayrshare
// (https://www.ayrshare.com), contract 2: one JSON request on stdin, one JSON
// response on stdout. See README.md.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

func main() {
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ayrshare: reading the request:", err)
		os.Exit(1)
	}
	var req wp.WrapperRequest
	if err := json.Unmarshal(in, &req); err != nil {
		fmt.Fprintln(os.Stderr, "ayrshare: the request is not readable JSON:", err)
		os.Exit(1)
	}
	resp := run(req, nil)
	if err := json.NewEncoder(os.Stdout).Encode(resp); err != nil {
		fmt.Fprintln(os.Stderr, "ayrshare: writing the response:", err)
		os.Exit(1)
	}
}

// run answers a request's calls in order, stopping at the first that does not
// succeed. hc is the HTTP client to reach Ayrshare with; nil is a default one.
func run(req wp.WrapperRequest, hc *http.Client) wp.WrapperResponse {
	var out wp.WrapperResponse
	if len(req.Calls) == 0 {
		return out
	}
	w := &wrapper{req: req}
	if req.Contract == wp.ContractVersion {
		w.c = newClient(req.Connection.Endpoint, req.Connection.Credentials[credentialName], hc)
	}
	for _, call := range req.Calls {
		var res wp.VerbResult
		if req.Contract != wp.ContractVersion {
			res = wp.VerbResult{Failed: fmt.Sprintf("this wrapper speaks contract %q; the request is for %q", wp.ContractVersion, req.Contract)}
		} else {
			res = w.handle(call)
		}
		res.Verb = call.Verb
		secrets := req.Secrets()
		res.Refused = wp.Scrub(res.Refused, secrets)
		res.Failed = wp.Scrub(res.Failed, secrets)
		out.Results = append(out.Results, res)
		if !res.Succeeded() {
			break
		}
	}
	return out
}
