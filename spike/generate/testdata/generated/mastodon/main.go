package main

import (
	"context"
	"encoding/json"
	"io"
	"os"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

func main() {
	os.Exit(mainWithIO(os.Stdin, os.Stdout, os.Stderr))
}

// mainWithIO is main's body, taking its streams as arguments so tests can
// drive it without touching the process's own stdin/stdout.
func mainWithIO(stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	body, err := io.ReadAll(stdin)
	if err != nil {
		// A process that cannot read its request at all exits non-zero, and
		// stdout is empty: this is that one case.
		return 1
	}
	var req wp.WrapperRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return 1
	}
	resp := run(context.Background(), req)
	encoded, err := json.Marshal(resp)
	if err != nil {
		return 1
	}
	if _, err := stdout.Write(encoded); err != nil {
		return 1
	}
	return 0
}
