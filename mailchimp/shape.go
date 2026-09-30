package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// kindEmailBroadcast is the one External Asset Kind this wrapper acts on.
const kindEmailBroadcast = "email_broadcast"

// Bounds this wrapper refuses past, declared in the kind's shape
// (manifest.go) so a project reads them before it tries.
const (
	subjectMaxLength   = 150
	preheaderMaxLength = 130
)

// emailBroadcastAsset is the kind's family fields, as this wrapper refines
// them (manifest.go's shape): a subject line, a preheader, and the body in
// both the html and text Mailchimp campaign content needs.
type emailBroadcastAsset struct {
	Subject   string             `json:"subject"`
	Preheader string             `json:"preheader"`
	Body      emailBroadcastBody `json:"body"`
}

type emailBroadcastBody struct {
	HTML string `json:"html"`
	Text string `json:"text"`
}

// parseEmailBroadcast reads a draft or publish payload against the shape,
// or says what is wrong with it: a required field missing, or a subject
// past its declared maxLength. It never makes a request: the suite's
// boundary cases (an incomplete payload, one a field too long) must be
// refused without publishing anything.
func parseEmailBroadcast(payload json.RawMessage) (*emailBroadcastAsset, string) {
	if len(payload) == 0 {
		return nil, "the payload is empty"
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, "the payload is not a JSON object"
	}
	var a emailBroadcastAsset
	if err := json.Unmarshal(payload, &a); err != nil {
		return nil, fmt.Sprintf("the payload does not match email_broadcast's shape: %v", err)
	}
	switch {
	case strings.TrimSpace(a.Subject) == "":
		return nil, "the payload has no subject"
	case len([]rune(a.Subject)) > subjectMaxLength:
		return nil, fmt.Sprintf("subject is %d characters, past its declared maxLength (%d)", len([]rune(a.Subject)), subjectMaxLength)
	case strings.TrimSpace(a.Preheader) == "":
		return nil, "the payload has no preheader"
	case len([]rune(a.Preheader)) > preheaderMaxLength:
		return nil, fmt.Sprintf("preheader is %d characters, past its declared maxLength (%d)", len([]rune(a.Preheader)), preheaderMaxLength)
	}
	bodyRaw, ok := raw["body"]
	if !ok || string(bodyRaw) == "null" {
		return nil, "the payload has no body"
	}
	if strings.TrimSpace(a.Body.HTML) == "" || strings.TrimSpace(a.Body.Text) == "" {
		return nil, "the payload's body needs both html and text"
	}
	return &a, ""
}
