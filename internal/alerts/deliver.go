// Package alerts delivers operational alerts — budgets crossing their alert
// threshold or running out, and guardrails blocking traffic — to each
// organization's notification channels: Slack, Microsoft Teams, signed
// webhooks and email.
package alerts

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Event types. A channel subscribes to a subset; no subscription means all.
const (
	BudgetThreshold  = "budget.threshold"
	BudgetExhausted  = "budget.exhausted"
	GuardrailBlocked = "guardrail.blocked"
	Test             = "test"
)

// EventTypes lists the types a channel can subscribe to.
var EventTypes = []string{BudgetThreshold, BudgetExhausted, GuardrailBlocked}

// Event is one alert. It never contains prompt or response content.
type Event struct {
	Type   string         `json:"event"`
	OrgID  string         `json:"org_id"`
	Title  string         `json:"title"`
	Text   string         `json:"text"`
	Link   string         `json:"link,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
	TS     time.Time      `json:"ts"`
}

// Target is a decrypted channel ready for delivery.
type Target struct {
	Type   string // slack | teams | webhook | email
	URL    string // slack, teams, webhook
	To     []string
	Secret string // webhook HMAC secret (optional)
}

// Deliverer sends events. HTTP is the client used for Slack, Teams and
// webhooks (SSRF-guarded in production); SMTP is the parsed THEOPHYLAX_SMTP_URL.
type Deliverer struct {
	HTTP *http.Client
	SMTP *SMTPConfig
}

func (d *Deliverer) Send(ctx context.Context, t Target, e Event) error {
	switch t.Type {
	case "slack":
		text := "*" + e.Title + "*\n" + e.Text
		if e.Link != "" {
			text += "\n<" + e.Link + "|Open in Theophylax>"
		}
		return d.post(ctx, t.URL, map[string]any{"text": text}, nil)
	case "teams":
		body := []any{
			map[string]any{"type": "TextBlock", "text": e.Title, "weight": "Bolder", "size": "Medium", "wrap": true},
			map[string]any{"type": "TextBlock", "text": e.Text, "wrap": true},
		}
		card := map[string]any{"type": "AdaptiveCard", "version": "1.4", "body": body,
			"$schema": "http://adaptivecards.io/schemas/adaptive-card.json"}
		if e.Link != "" {
			card["actions"] = []any{map[string]any{"type": "Action.OpenUrl", "title": "Open in Theophylax", "url": e.Link}}
		}
		return d.post(ctx, t.URL, map[string]any{"type": "message", "attachments": []any{
			map[string]any{"contentType": "application/vnd.microsoft.card.adaptive", "content": card}}}, nil)
	case "webhook":
		b, _ := json.Marshal(e)
		h := http.Header{"X-Theophylax-Event": {e.Type}}
		if t.Secret != "" {
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			m := hmac.New(sha256.New, []byte(t.Secret))
			m.Write([]byte(ts + "."))
			m.Write(b)
			h.Set("X-Theophylax-Timestamp", ts)
			h.Set("X-Theophylax-Signature", "sha256="+hex.EncodeToString(m.Sum(nil)))
		}
		return d.postRaw(ctx, t.URL, b, h)
	case "email":
		if d.SMTP == nil {
			return fmt.Errorf("email channel needs THEOPHYLAX_SMTP_URL")
		}
		body := e.Text
		if e.Link != "" {
			body += "\n\n" + e.Link
		}
		return d.SMTP.Send(t.To, "[Theophylax] "+e.Title, body)
	}
	return fmt.Errorf("unknown channel type %q", t.Type)
}

func (d *Deliverer) post(ctx context.Context, u string, v any, h http.Header) error {
	b, _ := json.Marshal(v)
	return d.postRaw(ctx, u, b, h)
}

func (d *Deliverer) postRaw(ctx context.Context, u string, b []byte, h http.Header) error {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, err := http.NewRequestWithContext(cctx, http.MethodPost, u, bytes.NewReader(b))
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "theophylax-alerts")
		for k, vs := range h {
			req.Header[k] = vs
		}
		resp, err := d.HTTP.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 300 {
				cancel()
				return nil
			}
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		cancel()
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return last
}

// SMTPConfig is parsed from THEOPHYLAX_SMTP_URL:
// smtp://user:pass@host:587?from=alerts@example.com (STARTTLS), or smtps://…:465
// for implicit TLS.
type SMTPConfig struct {
	Addr, Host, User, Pass, From string
	Implicit                     bool
}

func ParseSMTPURL(raw string) (*SMTPConfig, error) {
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "smtp" && u.Scheme != "smtps" {
		return nil, fmt.Errorf("THEOPHYLAX_SMTP_URL must start with smtp:// or smtps://")
	}
	c := &SMTPConfig{Host: u.Hostname(), From: u.Query().Get("from"), Implicit: u.Scheme == "smtps"}
	port := u.Port()
	if port == "" {
		port = map[bool]string{true: "465", false: "587"}[c.Implicit]
	}
	c.Addr = net.JoinHostPort(c.Host, port)
	if u.User != nil {
		c.User = u.User.Username()
		c.Pass, _ = u.User.Password()
	}
	if c.From == "" {
		return nil, fmt.Errorf("THEOPHYLAX_SMTP_URL needs ?from=<address>")
	}
	return c, nil
}

func (c *SMTPConfig) Send(to []string, subject, body string) error {
	for _, a := range append([]string{c.From}, to...) {
		if strings.ContainsAny(a, "\r\n") {
			return fmt.Errorf("invalid address")
		}
	}
	subject = strings.NewReplacer("\r", " ", "\n", " ").Replace(subject)
	msg := "From: " + c.From + "\r\nTo: " + strings.Join(to, ", ") + "\r\nSubject: " + subject +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + strings.ReplaceAll(body, "\n", "\r\n")
	var conn net.Conn
	var err error
	if c.Implicit {
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", c.Addr, &tls.Config{ServerName: c.Host})
	} else {
		conn, err = net.DialTimeout("tcp", c.Addr, 10*time.Second)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer cl.Close()
	if !c.Implicit {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return fmt.Errorf("SMTP server does not offer STARTTLS")
		}
		if err := cl.StartTLS(&tls.Config{ServerName: c.Host}); err != nil {
			return err
		}
	}
	if c.User != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.User, c.Pass, c.Host)); err != nil {
			return err
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return err
	}
	for _, a := range to {
		if err := cl.Rcpt(a); err != nil {
			return err
		}
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}
