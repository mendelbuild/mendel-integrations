// Command resend is Mendel's External Tool Wrapper for Resend (contract 2):
// one JSON request on stdin, one JSON response on stdout. It sends a short
// one-to-one message (External Asset Kind direct_message) and reads back what
// Resend holds of it. See README.md for what it honours, declines and why.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

func main() {
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reading the request:", err)
		os.Exit(1)
	}
	var req wp.WrapperRequest
	if err := json.Unmarshal(in, &req); err != nil {
		fmt.Fprintln(os.Stderr, "the request is not readable as JSON:", err)
		os.Exit(1)
	}
	out, err := json.Marshal(run(req, &http.Client{Timeout: 30 * time.Second}))
	if err != nil {
		fmt.Fprintln(os.Stderr, "writing the response:", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}

// run answers each call in order and stops at the first that does not succeed.
func run(req wp.WrapperRequest, hc *http.Client) wp.WrapperResponse {
	var resp wp.WrapperResponse
	if req.Contract != wp.ContractVersion {
		verb := wp.VerbProbe
		if len(req.Calls) > 0 {
			verb = req.Calls[0].Verb
		}
		resp.Results = append(resp.Results, wp.VerbResult{Verb: verb,
			Failed: fmt.Sprintf("this wrapper speaks contract %q and the request is for %q; nothing was done", wp.ContractVersion, req.Contract)})
		return resp
	}
	w := &wrapper{conn: req.Connection, c: newClient(req.Connection, hc)}
	for _, call := range req.Calls {
		res := w.handle(call)
		res.Verb = call.Verb
		resp.Results = append(resp.Results, res)
		if !res.Succeeded() {
			break
		}
	}
	return resp
}
