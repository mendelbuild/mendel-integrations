package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	wp "github.com/mendelbuild/mendelbuild/wrapperprotocol"
)

func TestMainRejectsUnreadableRequest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := mainWithIO(strings.NewReader("not json"), &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected a non-zero exit for a request that cannot be read")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout should be empty when the request cannot be read, got %q", stdout.String())
	}
}

func TestMainRoundTripsAContractMismatch(t *testing.T) {
	req := wp.WrapperRequest{Contract: "0", Calls: []wp.VerbCall{{Verb: wp.VerbProbe}}}
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := mainWithIO(bytes.NewReader(encoded), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("a readable request that simply fails should exit 0, got %d", code)
	}
	var resp wp.WrapperResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("stdout was not a readable response: %v (%s)", err, stdout.String())
	}
	if len(resp.Results) != 1 || resp.Results[0].Failed == "" {
		t.Fatalf("expected one failed result, got %+v", resp)
	}
}
