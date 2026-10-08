// Command mailgun is the External Tool Wrapper for Mailgun (contract 2): it
// sends one short message to one recipient through Mailgun's "Send an email"
// API and declines every other verb, with its reason. See README.md.
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

func main() {
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mailgun: cannot read the request:", err)
		os.Exit(1)
	}
	var req wp.WrapperRequest
	if err := json.Unmarshal(in, &req); err != nil {
		fmt.Fprintln(os.Stderr, "mailgun: the request is not JSON:", err)
		os.Exit(1)
	}
	resp := handle(context.Background(), req, &http.Client{Timeout: 30 * time.Second}, time.Now)
	if err := json.NewEncoder(os.Stdout).Encode(resp); err != nil {
		fmt.Fprintln(os.Stderr, "mailgun: cannot write the answer:", err)
		os.Exit(1)
	}
}
