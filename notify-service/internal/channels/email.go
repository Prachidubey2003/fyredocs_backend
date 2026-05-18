package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/smtp"
)

// EmailPayload is the per-event body the dispatcher unmarshals
// from NotifyEvent.Payload before calling Email.Send. Subject is
// required; either Text or HTML (or both) must be present.
type EmailPayload struct {
	Subject string `json:"subject"`
	Text    string `json:"text,omitempty"`
	HTML    string `json:"html,omitempty"`
}

// Email is the SMTP-backed channel implementation. When
// SMTPHost is empty (the dev / preview default), Send logs the
// would-be delivery at Info level and returns nil — every other
// part of the pipeline runs end-to-end without external SMTP
// creds, which makes the smoke-test loop fast.
//
// The production wiring expects a single relay account
// (sendgrid, mailgun, ses-smtp, …) — high-volume per-tenant
// sender pools belong in a separate `mailer` follow-up, not in
// v0.
type Email struct {
	// SMTPHost is `host:port`. Empty disables the transport
	// (logs only).
	SMTPHost string
	// SMTPAuth is the PLAIN auth wrapper. Nil means anonymous
	// relay (rare; mostly internal test fixtures).
	SMTPAuth smtp.Auth
	// FromAddress is the envelope sender. Subscribers may also
	// see this in the `From` header unless the payload overrides
	// it (v0 does not).
	FromAddress string
}

func (e *Email) Send(ctx context.Context, req SendRequest) error {
	if req.Target == "" {
		return fmt.Errorf("email: empty target address")
	}
	var p EmailPayload
	if len(req.Payload) > 0 {
		if err := json.Unmarshal(req.Payload, &p); err != nil {
			return fmt.Errorf("email: parse payload: %w", err)
		}
	}
	if p.Subject == "" {
		return fmt.Errorf("email: subject is required")
	}
	if p.Text == "" && p.HTML == "" {
		return fmt.Errorf("email: text or html is required")
	}

	if e.SMTPHost == "" {
		// Dev fallback: pretend-deliver. The dispatcher still
		// persists the Delivery row as `delivered` so the
		// developer console renders it normally.
		slog.Info("email channel (log-only mode)",
			"target", req.Target, "subject", p.Subject)
		return nil
	}

	msg := buildMIME(e.FromAddress, req.Target, p)
	if err := smtp.SendMail(e.SMTPHost, e.SMTPAuth, e.FromAddress, []string{req.Target}, msg); err != nil {
		return fmt.Errorf("email: smtp send: %w", err)
	}
	_ = ctx // SMTP stdlib doesn't honour context yet; placeholder for future net/smtp upgrade.
	return nil
}

// buildMIME stitches together a minimal RFC-5322 message. For
// HTML+plain dual payloads we'd emit a multipart/alternative;
// v0's `Text` xor `HTML` choice keeps the message shape simple
// (subscribers can decide which one to send per-template).
func buildMIME(from, to string, p EmailPayload) []byte {
	body := p.Text
	contentType := "text/plain; charset=utf-8"
	if p.HTML != "" {
		body = p.HTML
		contentType = "text/html; charset=utf-8"
	}
	msg := "From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + p.Subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: " + contentType + "\r\n" +
		"\r\n" +
		body
	return []byte(msg)
}
