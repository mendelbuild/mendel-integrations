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
// A wrapper whose credentials come from the tool's own authorization
// (connection.authorize in its wrapper.json) is connected first, once, by a
// person at a terminal:
//
//	conformance authorize -file mastodon/wrapper.json -cmd /tmp/mastodon -account mastodon.social
//
// which prints a URL to open, reads back the code the tool shows, and keeps
// what the wrapper produced in a file only this user can read, outside the
// repository (see credentialsFile). Later runs read it from there.
//
// Typed credentials are read from this shell's environment under the names
// the wrapper.json gives them, as `mendel-tool tools verify` reads them. No
// credential is ever printed, and the report carries none. The venue is an
// account of the tester's own, never a project's (doc 35 §8).
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mendelbuild/mendel-integrations/conformance"
	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

// options are the flags both forms share.
type options struct {
	file, image, command, account, endpoint string
	config                                  map[string]any
}

func (o *options) register(fs *flag.FlagSet) {
	fs.StringVar(&o.file, "file", "", "the wrapper's wrapper.json")
	fs.StringVar(&o.image, "image", "", "the image to run with `docker run -i --rm`")
	fs.StringVar(&o.command, "cmd", "", "a local executable to run instead of an image")
	fs.StringVar(&o.account, "account", "", "the venue account: the tool's handle on an account of your own")
	fs.StringVar(&o.endpoint, "endpoint", "", "the API base URL, for a self-hosted instance")
	o.config = map[string]any{}
	fs.Func("config", "a setting of the connection's config, key=value (repeatable)", func(kv string) error {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return errors.New("want key=value")
		}
		o.config[k] = v
		return nil
	})
}

func (o *options) check() {
	if o.file == "" || (o.image == "") == (o.command == "") {
		exitOn(errors.New("give -file wrapper.json and exactly one of -image <image> or -cmd <executable>"))
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "authorize" {
		authorize(os.Args[2:])
		return
	}
	var o options
	fs := flag.NewFlagSet("conformance", flag.ExitOnError)
	o.register(fs)
	jsonOut := fs.String("json", "", "also write the report as JSON to this file")
	at := fs.String("at", "", "the instant the suite's windows are read back from (RFC 3339); now when empty. "+
		"Reads cover the last complete day before it, so a venue with traffic only today is read with -at tomorrow")
	revoke := fs.Bool("revoke", false, "run authorize's revoke at the end, which ends the venue's grant")
	fs.Parse(os.Args[1:])
	o.check()

	d := description(o.file)
	conn := connection(d, o)
	when := time.Now()
	if *at != "" {
		var err error
		when, err = time.Parse(time.RFC3339, *at)
		exitOn(err)
	}
	r := conformance.Run(context.Background(), conformance.Target{Description: d, Connection: conn,
		Run: runner(argv(o)), Revoke: *revoke}, when)
	print(r)
	if *jsonOut != "" {
		body, _ := json.MarshalIndent(r, "", "  ")
		exitOn(os.WriteFile(*jsonOut, body, 0o644))
	}
	if *revoke {
		// The grant is gone at the tool; what was kept of it is no use.
		_ = os.Remove(credentialsFile(d, o.account))
	}
	if !r.Passed() {
		os.Exit(1)
	}
}

func description(file string) wp.Description {
	raw, err := os.ReadFile(file)
	exitOn(err)
	var d wp.Description
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	exitOn(dec.Decode(&d))
	if why := d.Check(); why != "" {
		exitOn(fmt.Errorf("%s: %s", file, why))
	}
	return d
}

// credentialsFile is where what authorize produced for one tool and account
// is kept: under the user's own config directory, never the repository, and
// readable by the user alone.
func credentialsFile(d wp.Description, account string) string {
	dir, err := os.UserConfigDir()
	exitOn(err)
	name := strings.NewReplacer("/", "_", ":", "_", "@", "_").Replace(d.Tool.Slug + "@" + account)
	return filepath.Join(dir, "mendel-conformance", name+".json")
}

func readCredentials(path string) map[string]string {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}
	}
	exitOn(err)
	out := map[string]string{}
	exitOn(json.Unmarshal(raw, &out))
	return out
}

func writeCredentials(path string, creds map[string]string) {
	exitOn(os.MkdirAll(filepath.Dir(path), 0o700))
	body, _ := json.Marshal(creds)
	tmp := path + ".tmp"
	exitOn(os.WriteFile(tmp, body, 0o600))
	exitOn(os.Rename(tmp, path))
}

// connection is the venue account: typed credentials from the environment,
// authorized ones from the credentials file.
func connection(d wp.Description, o options) wp.Connection {
	conn := wp.Connection{AccountID: o.account, Endpoint: o.endpoint, Credentials: map[string]string{}, Config: o.config}
	var missing []string
	if d.Connection.Authorize {
		conn.Credentials = readCredentials(credentialsFile(d, o.account))
		if len(conn.Credentials) == 0 {
			exitOn(fmt.Errorf("%s connects through the tool's own authorization; run `conformance authorize` with the "+
				"same -file and -account first", d.Tool.Slug))
		}
	} else {
		for _, c := range d.Connection.Credentials {
			v := os.Getenv(c.Name)
			if v == "" {
				missing = append(missing, c.Name)
			}
			conn.Credentials[c.Name] = v
		}
	}
	if d.Connection.Account != nil && o.account == "" {
		missing = append(missing, "-account ("+d.Connection.Account.Label+")")
	}
	if len(missing) > 0 {
		exitOn(fmt.Errorf("set %s first; credentials are read from this shell and never stored", strings.Join(missing, ", ")))
	}
	return conn
}

// authorize connects a venue account through the wrapper's authorize verb,
// with a person at this terminal: begin, a URL to open, the code the tool
// shows, complete.
func authorize(args []string) {
	var o options
	fs := flag.NewFlagSet("conformance authorize", flag.ExitOnError)
	o.register(fs)
	fs.Parse(args)
	o.check()
	d := description(o.file)
	if !d.Connection.Authorize {
		exitOn(fmt.Errorf("%s's wrapper.json does not connect through authorize; its credentials are typed", d.Tool.Slug))
	}
	if o.account == "" && d.Connection.Account != nil {
		exitOn(fmt.Errorf("give -account (%s)", d.Connection.Account.Label))
	}
	path := credentialsFile(d, o.account)
	conn := wp.Connection{AccountID: o.account, Endpoint: o.endpoint, Config: o.config,
		Credentials: readCredentials(path)} // a client registered before is reused
	run := runner(argv(o))
	ctx := context.Background()

	b := make([]byte, 16)
	_, _ = rand.Read(b)
	state := hex.EncodeToString(b)
	resp, _, err := run(ctx, wp.WrapperRequest{Contract: d.Contract, Connection: conn, Calls: []wp.VerbCall{{
		Verb: wp.VerbAuthorize, Step: wp.AuthorizeBegin, RedirectURI: wp.OutOfBandRedirect, State: state}}})
	exitOn(err)
	begun := resp.Results[0]
	if !begun.Succeeded() {
		exitOn(fmt.Errorf("begin: %s", begun.Why()))
	}
	for k, v := range begun.Credentials {
		conn.Credentials[k] = v
	}
	// Kept now, so a client the instance registered is not registered again
	// if the person stops here and comes back.
	writeCredentials(path, conn.Credentials)

	fmt.Printf("Open this in a browser, sign in as the venue account, and approve:\n\n  %s\n\n", begun.AuthorizeURL)
	fmt.Print("Then paste the code it shows here: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		exitOn(errors.New("no code was entered"))
	}
	code := strings.TrimSpace(line)

	resp, _, err = run(ctx, wp.WrapperRequest{Contract: d.Contract, Connection: conn, Calls: []wp.VerbCall{{
		Verb: wp.VerbAuthorize, Step: wp.AuthorizeComplete, Code: code}}})
	exitOn(err)
	done := resp.Results[0]
	if !done.Succeeded() {
		exitOn(fmt.Errorf("complete: %s", done.Why()))
	}
	for k, v := range done.Credentials {
		conn.Credentials[k] = v
	}
	writeCredentials(path, conn.Credentials)
	names := make([]string, 0, len(conn.Credentials))
	for k := range conn.Credentials {
		names = append(names, k)
	}
	sort.Strings(names)
	fmt.Printf("\nAuthorized. Kept %s in %s (readable by you alone).\n", strings.Join(names, ", "), path)
}

func argv(o options) []string {
	if o.command != "" {
		return []string{o.command}
	}
	return []string{"docker", "run", "-i", "--rm", o.image}
}

// runner runs argv once per request: the request on stdin, the answer from
// stdout, checked against the calls with the protocol's own parser, and
// stderr kept for the suite's check that no credential reaches it.
func runner(argv []string) conformance.Runner {
	return func(ctx context.Context, req wp.WrapperRequest) (wp.WrapperResponse, string, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		body, err := json.Marshal(req)
		if err != nil {
			return wp.WrapperResponse{}, "", err
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdin = bytes.NewReader(body)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			// Not quoting stderr here: it is the wrapper's, and the suite is
			// what judges whether a credential reached it.
			return wp.WrapperResponse{}, stderr.String(), fmt.Errorf("the wrapper exited: %v", err)
		}
		resp, err := wp.ParseWrapperResponse(stdout.Bytes(), len(req.Calls))
		return resp, stderr.String(), err
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
