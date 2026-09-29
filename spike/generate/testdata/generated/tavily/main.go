// Command tavily is Mendel's External Tool Wrapper for Tavily
// (https://tavily.com), a hosted web search API. It speaks the wrapper
// protocol's draft contract "2-draft" (wrapperprotocol.DraftContractVersion):
// one JSON WrapperRequest on stdin, one JSON WrapperResponse on stdout. It
// honours two verbs against a real account, probe and search, and declines
// every other verb the contract names, since Tavily has nothing for a
// wrapper to draft, publish, retract, or read a metric series from.
//
// See README.md for what was read to write this, with the date, and for the
// spec_hash probe's manifest carries.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tavily: reading the request from stdin:", err)
		os.Exit(1)
	}

	var req WrapperRequest
	if err := json.Unmarshal(in, &req); err != nil {
		fmt.Fprintln(os.Stderr, "tavily: the request on stdin is not readable JSON:", err)
		os.Exit(1)
	}

	w := &Wrapper{HTTP: &http.Client{Timeout: 30 * time.Second}}
	resp := w.Handle(req)

	out, err := json.Marshal(resp)
	if err != nil {
		// Nothing built here should be unmarshalable; if it somehow is, say
		// so on stderr rather than print a truncated response.
		fmt.Fprintln(os.Stderr, "tavily: encoding the response:", err)
		os.Exit(1)
	}
	os.Stdout.Write(out)
}
