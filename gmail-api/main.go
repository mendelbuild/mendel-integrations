// Command gmail-api is the External Tool Wrapper for Gmail API: it sends a
// personal message to one named person from the project owner's own account,
// the exact text approved. It speaks contract "2": one JSON request on stdin,
// one JSON response on stdout.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	wp "github.com/mendelbuild/mendel-integrations/contract/wrapperprotocol"
)

func main() {
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gmail-api: cannot read the request:", err)
		os.Exit(1)
	}
	var req wp.WrapperRequest
	if err := json.Unmarshal(body, &req); err != nil {
		fmt.Fprintln(os.Stderr, "gmail-api: the request is not JSON:", err)
		os.Exit(1)
	}
	if len(req.Calls) == 0 {
		fmt.Fprintln(os.Stderr, "gmail-api: the request makes no calls")
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(run(context.Background(), req)); err != nil {
		fmt.Fprintln(os.Stderr, "gmail-api: cannot write the response:", err)
		os.Exit(1)
	}
}

func base64URL(b []byte) string { return base64.URLEncoding.EncodeToString(b) }

// run answers the calls in order, stopping at the first that does not succeed.
func run(ctx context.Context, req wp.WrapperRequest) wp.WrapperResponse {
	var resp wp.WrapperResponse
	if req.Contract != wp.ContractVersion {
		resp.Results = []wp.VerbResult{{Verb: req.Calls[0].Verb,
			Failed: fmt.Sprintf("this wrapper speaks contract %q, and the request is for %q; nothing was done", wp.ContractVersion, req.Contract)}}
		return resp
	}
	c := newClient(req.Connection)
	for _, call := range req.Calls {
		res := c.handle(ctx, call)
		res.Verb = call.Verb
		resp.Results = append(resp.Results, res)
		if !res.Succeeded() {
			break
		}
	}
	return resp
}

func refused(format string, a ...any) wp.VerbResult {
	return wp.VerbResult{Refused: fmt.Sprintf(format, a...)}
}
func failed(format string, a ...any) wp.VerbResult {
	return wp.VerbResult{Failed: fmt.Sprintf(format, a...)}
}

func (c *client) handle(ctx context.Context, call wp.VerbCall) wp.VerbResult {
	switch call.Verb {
	case wp.VerbProbe:
		return c.probe(ctx)
	case wp.VerbDraft:
		return c.draft(ctx, call)
	case wp.VerbPublish:
		return c.publish(ctx, call)
	case wp.VerbReadBack:
		return c.readBack(ctx, call)
	}
	if s, ok := verbSupport()[call.Verb]; ok && s.Level == wp.VerbDeclined {
		return refused("%s is declined: %s", call.Verb, s.Reason)
	}
	return refused("%q is not a verb of the contract", call.Verb)
}

func (c *client) probe(ctx context.Context) wp.VerbResult {
	if err := c.getProfile(ctx); err != nil {
		return failed("the credential could not read the account: %v", err)
	}
	return wp.VerbResult{Manifest: manifest()}
}

// --- refs ---

// A ref names where the asset is held: a draft (until it is sent) or a sent
// message, which has a new id.
const (
	draftPrefix   = "draft:"
	messagePrefix = "message:"
)

// messageFrom reads a payload as a direct_message, or says why it cannot be.
func messageFrom(kind string, payload json.RawMessage) (directMessage, string) {
	if kind != kindDirectMessage {
		return directMessage{}, fmt.Sprintf("this wrapper sends %s only; %q is not a kind it acts on", kindDirectMessage, kind)
	}
	var fields map[string]json.RawMessage
	if len(payload) == 0 || json.Unmarshal(payload, &fields) != nil || fields == nil {
		return directMessage{}, "the payload is not a JSON object of recipient, subject and body"
	}
	var m directMessage
	for _, f := range []struct {
		name string
		to   *string
	}{{"recipient", &m.Recipient}, {"subject", &m.Subject}, {"body", &m.Body}} {
		raw, ok := fields[f.name]
		if !ok {
			return directMessage{}, fmt.Sprintf("the payload has no %s, which a direct message requires", f.name)
		}
		if json.Unmarshal(raw, f.to) != nil {
			return directMessage{}, fmt.Sprintf("the %s is not text", f.name)
		}
		delete(fields, f.name)
	}
	for name := range fields {
		return directMessage{}, fmt.Sprintf("the payload has a field %q, which a direct message does not; it would not be sent", name)
	}
	if why := checkMessage(m); why != "" {
		return directMessage{}, why
	}
	return m, ""
}

// --- draft ---

func (c *client) draft(ctx context.Context, call wp.VerbCall) wp.VerbResult {
	m, why := messageFrom(call.AssetKind, call.Payload)
	if why != "" {
		return refused("%s", why)
	}
	d, err := c.createDraft(ctx, composeMIME(c.from, m))
	if err != nil {
		return failed("creating the draft: %v", err)
	}
	if d.ID == "" {
		return failed("Gmail API created a draft and answered no id")
	}
	return wp.VerbResult{Ref: draftPrefix + d.ID}
}

// --- publish ---

func (c *client) publish(ctx context.Context, call wp.VerbCall) wp.VerbResult {
	if call.When != nil && call.When.Mode != wp.WhenNow && call.When.Mode != "" {
		return refused("a message is sent now or not at all: the documentation found shows no scheduled send, so when %q is not served",
			call.When.Mode)
	}
	if call.Ref != "" {
		id, ok := strings.CutPrefix(call.Ref, draftPrefix)
		switch {
		case strings.HasPrefix(call.Ref, messagePrefix):
			return refused("%s is a message already sent; it cannot be sent again", call.Ref)
		case !ok || id == "":
			return refused("%q is not a draft this wrapper made", call.Ref)
		}
		sent, err := c.sendDraft(ctx, id)
		if e, isAPI := err.(*apiError); isAPI && e.notFound() {
			return refused("the draft %s is not held: it was sent or deleted", call.Ref)
		}
		if err != nil {
			return failed("sending the draft: %v", err)
		}
		return sentResult(sent)
	}
	m, why := messageFrom(call.AssetKind, call.Payload)
	if why != "" {
		return refused("%s", why)
	}
	sent, err := c.sendMessage(ctx, composeMIME(c.from, m))
	if err != nil {
		return failed("sending the message: %v", err)
	}
	return sentResult(sent)
}

func sentResult(m messageResource) wp.VerbResult {
	if m.ID == "" {
		return failed("Gmail API sent the message and answered no id, so it could not be read back")
	}
	return wp.VerbResult{Ref: messagePrefix + m.ID}
}

// --- read_back ---

func (c *client) readBack(ctx context.Context, call wp.VerbCall) wp.VerbResult {
	var raw string
	switch {
	case strings.HasPrefix(call.Ref, draftPrefix) && len(call.Ref) > len(draftPrefix):
		d, err := c.getDraftRaw(ctx, strings.TrimPrefix(call.Ref, draftPrefix))
		if e, ok := err.(*apiError); ok && e.notFound() {
			return refused("the draft %s is not held: it was sent or deleted", call.Ref)
		}
		if err != nil {
			return failed("reading the draft: %v", err)
		}
		raw = d.Message.Raw
	case strings.HasPrefix(call.Ref, messagePrefix) && len(call.Ref) > len(messagePrefix):
		m, err := c.getMessageRaw(ctx, strings.TrimPrefix(call.Ref, messagePrefix))
		if e, ok := err.(*apiError); ok && e.notFound() {
			return refused("the message %s is not held: it was deleted", call.Ref)
		}
		if err != nil {
			return failed("reading the message: %v", err)
		}
		raw = m.Raw
	default:
		return refused("%q is not a ref this wrapper made", call.Ref)
	}
	if raw == "" {
		return failed("Gmail API answered the message without its raw form")
	}
	bytes, err := decodeBase64URL(raw)
	if err != nil {
		return failed("the message's raw form is not base64url: %v", err)
	}
	m, err := parseMIME(bytes)
	if err != nil {
		return failed("%v", err)
	}
	asset, _ := json.Marshal(m)
	return wp.VerbResult{Asset: asset}
}

// decodeBase64URL reads base64url with or without padding.
func decodeBase64URL(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	s = strings.NewReplacer("+", "-", "/", "_").Replace(s)
	return base64.RawURLEncoding.DecodeString(s)
}
