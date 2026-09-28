// Command conformance runs one wrapper through the conformance suite against
// a venue account, and prints what each check found.
//
//	conformance -file plausible/wrapper.json -image ghcr.io/mendelbuild/plausible@sha256:... \
//	    -account example.com [-endpoint http://localhost:8000] [-json report.json]
//
// or, while writing a wrapper, against its binary with no image at all:
//
//	go build -o /tmp/plausible ./plausible && conformance -file plausible/wrapper.json -cmd /tmp/plausible ...
//
// Credentials are read from this shell's environment under the names the
// wrapper.json gives them, as `mendel-tool tools verify` reads them, and are
// never written anywhere: the report carries none. The venue is an account of
// the tester's own, never a project's (doc 35 §8).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mendelbuild/mendel-integrations/conformance"
	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

func main() {
	file := flag.String("file", "", "the wrapper's wrapper.json")
	image := flag.String("image", "", "the image to run with `docker run -i --rm`")
	command := flag.String("cmd", "", "a local executable to run instead of an image")
	account := flag.String("account", "", "the venue account: the tool's handle on an account of your own")
	endpoint := flag.String("endpoint", "", "the API base URL, for a self-hosted instance")
	jsonOut := flag.String("json", "", "also write the report as JSON to this file")
	at := flag.String("at", "", "the instant the suite's windows are read back from (RFC 3339); now when empty. "+
		"Reads cover the last complete day before it, so a venue with traffic only today is read with -at tomorrow")
	flag.Parse()

	if *file == "" || (*image == "") == (*command == "") {
		fmt.Fprintln(os.Stderr, "usage: conformance -file wrapper.json (-image <image> | -cmd <executable>) -account <account> [-endpoint URL] [-json out.json]")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*file)
	exitOn(err)
	var d wp.Description
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	exitOn(dec.Decode(&d))
	if why := d.Check(); why != "" {
		exitOn(fmt.Errorf("%s: %s", *file, why))
	}

	conn := wp.Connection{AccountID: *account, Endpoint: *endpoint, Credentials: map[string]string{}}
	var missing []string
	for _, c := range d.Connection.Credentials {
		v := os.Getenv(c.Name)
		if v == "" {
			missing = append(missing, c.Name)
		}
		conn.Credentials[c.Name] = v
	}
	if d.Connection.Account != nil && *account == "" {
		missing = append(missing, "-account ("+d.Connection.Account.Label+")")
	}
	if len(missing) > 0 {
		exitOn(fmt.Errorf("set %s first; credentials are read from this shell and never stored", strings.Join(missing, ", ")))
	}

	argv := []string{"docker", "run", "-i", "--rm", *image}
	if *command != "" {
		argv = []string{*command}
	}
	when := time.Now()
	if *at != "" {
		when, err = time.Parse(time.RFC3339, *at)
		exitOn(err)
	}
	r := conformance.Run(context.Background(), conformance.Target{Description: d, Connection: conn, Run: runner(argv)}, when)
	print(r)
	if *jsonOut != "" {
		body, _ := json.MarshalIndent(r, "", "  ")
		exitOn(os.WriteFile(*jsonOut, body, 0o644))
	}
	if !r.Passed() {
		os.Exit(1)
	}
}

// runner runs argv once per request: the request on stdin, the answer from
// stdout, checked against the calls with the protocol's own parser.
func runner(argv []string) conformance.Runner {
	return func(ctx context.Context, req wp.WrapperRequest) (wp.WrapperResponse, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		body, err := json.Marshal(req)
		if err != nil {
			return wp.WrapperResponse{}, err
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdin = bytes.NewReader(body)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return wp.WrapperResponse{}, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
		}
		return wp.ParseWrapperResponse(stdout.Bytes(), len(req.Calls))
	}
}

func print(r conformance.Report) {
	mark := map[conformance.Outcome]string{conformance.Pass: "PASS", conformance.Fail: "FAIL",
		conformance.Warn: "WARN", conformance.Untested: "----"}
	fmt.Printf("%s %s, %s\n\n", r.Tool, r.Version, r.At.Format(time.RFC3339))
	var calls int
	var elapsed time.Duration
	for _, c := range r.Checks {
		verb := string(c.Verb)
		if verb == "" {
			verb = "-"
		}
		fmt.Printf("%s  %-14s %-62s %6dms\n", mark[c.Outcome], verb, c.Name, c.Elapsed.Milliseconds())
		if c.Detail != "" && c.Outcome != conformance.Pass {
			fmt.Printf("      %s\n", c.Detail)
		}
		calls += c.Calls
		elapsed += c.Elapsed
	}
	n := r.Count()
	fmt.Printf("\n%d pass, %d fail, %d warn, %d untested; %d calls in %s\n",
		n[conformance.Pass], n[conformance.Fail], n[conformance.Warn], n[conformance.Untested], calls, elapsed.Round(time.Millisecond))
}

func exitOn(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "conformance:", err)
		os.Exit(2)
	}
}
