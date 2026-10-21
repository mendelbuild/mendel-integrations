package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The direct_message family (recipient, subject, body) and the RFC 2822
// message Gmail API takes for it: messages.send and drafts.create both take
// the MIME message, base64url encoded, in the message's raw field (the drafts
// guide, "Create drafts").

// maxSubject is the longest subject, in characters, this wrapper accepts. It
// is the wrapper's own bound, well inside RFC 2822's 998-character line limit
// once encoded; the Gmail documentation fetched states none.
const maxSubject = 200

// directMessage is the approved text: the whole of what is sent.
type directMessage struct {
	Recipient string `json:"recipient"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
}

// checkMessage says why a message cannot be sent exactly as written, or "".
func checkMessage(m directMessage) string {
	switch {
	case m.Recipient == "":
		return "the recipient is empty"
	case m.Subject == "":
		return "the subject is empty"
	case m.Body == "":
		return "the body is empty"
	}
	for _, r := range m.Recipient {
		if r > unicode.MaxASCII || r <= ' ' || r == 0x7f {
			return "the recipient must be one plain ASCII address, such as name@example.com"
		}
	}
	if a, err := mail.ParseAddress(m.Recipient); err != nil || a.Name != "" || a.Address != m.Recipient {
		return "the recipient must be one plain address, such as name@example.com, with no display name or list"
	}
	switch {
	case !utf8.ValidString(m.Subject) || !utf8.ValidString(m.Body):
		return "the text is not valid UTF-8"
	case utf8.RuneCountInString(m.Subject) > maxSubject:
		return fmt.Sprintf("the subject is longer than %d characters", maxSubject)
	case strings.ContainsRune(m.Body, 0):
		return "the body holds a NUL character"
	}
	for _, r := range m.Subject {
		if unicode.IsControl(r) {
			return "the subject holds a control character, such as a line break, which a mail header cannot carry"
		}
	}
	first, _ := utf8.DecodeRuneInString(m.Subject)
	last, _ := utf8.DecodeLastRuneInString(m.Subject)
	if unicode.IsSpace(first) || unicode.IsSpace(last) {
		return "the subject begins or ends with a space, which a mail header does not keep, so it could not be read back as approved"
	}
	return ""
}

// encodeSubject writes a subject as a header value: plain where it is plain
// printable ASCII, otherwise as RFC 2047 encoded words of at most 75
// characters, folded.
func encodeSubject(s string) string {
	plain := !strings.Contains(s, "=?")
	for _, r := range s {
		if r < ' ' || r > '~' {
			plain = false
		}
	}
	if plain {
		return s
	}
	const prefix, suffix = "=?utf-8?b?", "?="
	// 45 bytes encode to 60 base64 characters; 60+12 is within 75.
	const chunk = 45
	var words []string
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			words = append(words, prefix+base64.StdEncoding.EncodeToString(cur)+suffix)
			cur = cur[:0]
		}
	}
	for _, r := range s {
		enc := []byte(string(r))
		if len(cur)+len(enc) > chunk {
			flush()
		}
		cur = append(cur, enc...)
	}
	flush()
	return strings.Join(words, "\r\n ")
}

// composeMIME is the message as an RFC 2822 MIME message. The body travels
// base64 encoded, so every character of it, line breaks included, comes back
// as it went. from is left out when empty: Gmail sends as the account.
func composeMIME(from string, m directMessage) []byte {
	var b bytes.Buffer
	if from != "" {
		fmt.Fprintf(&b, "From: %s\r\n", from)
	}
	fmt.Fprintf(&b, "To: %s\r\n", m.Recipient)
	fmt.Fprintf(&b, "Subject: %s\r\n", encodeSubject(m.Subject))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(m.Body))
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	b.WriteString("\r\n")
	return b.Bytes()
}

// parseMIME reads the family fields back out of a message as Gmail holds it.
func parseMIME(raw []byte) (directMessage, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return directMessage{}, fmt.Errorf("the message is not readable as RFC 2822: %v", err)
	}
	var out directMessage
	out.Recipient = msg.Header.Get("To")
	if a, err := mail.ParseAddress(out.Recipient); err == nil {
		out.Recipient = a.Address
	}
	out.Subject = msg.Header.Get("Subject")
	if s, err := new(mime.WordDecoder).DecodeHeader(out.Subject); err == nil {
		out.Subject = s
	}
	body, err := textBody(msg.Header, msg.Body)
	if err != nil {
		return directMessage{}, err
	}
	out.Body = body
	return out, nil
}

type headerGetter interface{ Get(string) string }

// textBody finds the text/plain body of a message or of one of its parts and
// decodes its transfer encoding.
func textBody(h headerGetter, r io.Reader) (string, error) {
	mt, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil || mt == "" {
		mt, params = "text/plain", map[string]string{}
	}
	if strings.HasPrefix(mt, "multipart/") {
		mr := multipart.NewReader(r, params["boundary"])
		for {
			p, err := mr.NextRawPart()
			if err == io.EOF {
				return "", errors.New("the message has no text/plain part")
			}
			if err != nil {
				return "", fmt.Errorf("the message's parts are not readable: %v", err)
			}
			if body, err := textBody(p.Header, p); err == nil {
				return body, nil
			}
		}
	}
	if mt != "text/plain" {
		return "", fmt.Errorf("the message part is %s, not text/plain", mt)
	}
	if cs := strings.ToLower(params["charset"]); cs != "" && cs != "utf-8" && cs != "us-ascii" {
		return "", fmt.Errorf("the message is in charset %s, which this wrapper does not read", cs)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(h.Get("Content-Transfer-Encoding"))) {
	case "base64":
		clean := strings.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, string(data))
		dec, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			return "", fmt.Errorf("the message body is not valid base64: %v", err)
		}
		data = dec
	case "quoted-printable":
		dec, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(data)))
		if err != nil {
			return "", fmt.Errorf("the message body is not valid quoted-printable: %v", err)
		}
		data = dec
	}
	if !utf8.Valid(data) {
		return "", errors.New("the message body is not valid UTF-8")
	}
	return string(data), nil
}
