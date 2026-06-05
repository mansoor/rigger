package notify

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/smtp"
	"strings"

	"github.com/mansoor/rigger/ui/internal/db"
)

// Notification levels — map alert severity (fire) or resolution (success).
const (
	LevelInfo    = "info"
	LevelSuccess = "success"
	LevelWarning = "warning"
	LevelFailure = "failure"
)

// Notification is a single message to deliver across one or more channels.
type Notification struct {
	Title string
	Body  string
	Level string
}

// AppriseBackend delivers a notification to one or more Apprise URLs. Two
// implementations exist: the in-process apprise-go library (EmbeddedApprise,
// the default) and the Apprise API sidecar (AppriseClient, opt-in via
// APPRISE_URL). Both speak the same Apprise URL format, so channels and the UI
// are identical regardless of which backend is active.
type AppriseBackend interface {
	Notify(urls []string, title, body, level string) error
}

// Dispatcher routes notifications to channels: email directly over SMTP,
// everything else through the configured Apprise backend.
type Dispatcher struct {
	db      *db.DB
	apprise AppriseBackend
}

func NewDispatcher(d *db.DB, apprise AppriseBackend) *Dispatcher {
	return &Dispatcher{db: d, apprise: apprise}
}

// Send delivers a notification through a single channel.
func (d *Dispatcher) Send(ch Channel, n Notification) error {
	switch ch.Type {
	case TypeEmail:
		var cfg EmailConfig
		if err := json.Unmarshal(nonNil(ch.Config), &cfg); err != nil {
			return fmt.Errorf("parse email config: %w", err)
		}
		return sendEmail(cfg, n)
	case TypeApprise:
		var cfg AppriseConfig
		if err := json.Unmarshal(nonNil(ch.Config), &cfg); err != nil {
			return fmt.Errorf("parse apprise config: %w", err)
		}
		return d.apprise.Notify(parseAppriseURLs(cfg.URLs), n.Title, n.Body, n.Level)
	default:
		return fmt.Errorf("unknown channel type %q", ch.Type)
	}
}

// DispatchToChannels delivers n to each enabled channel in ids, concurrently.
// Failures are logged, not propagated — a broken channel must not stop the
// evaluator or the other channels. Called from the evaluator on fire/resolve.
func (d *Dispatcher) DispatchToChannels(ids []int64, n Notification) {
	for _, id := range ids {
		ch, err := GetChannel(d.db, id)
		if err != nil || ch == nil || !ch.Enabled {
			continue
		}
		c := *ch
		go func() {
			if err := d.Send(c, n); err != nil {
				log.Printf("notify: channel %q (%s) failed: %v", c.Name, c.Type, err)
			}
		}()
	}
}

// ── SMTP email ───────────────────────────────────────────────────────────────

func sendEmail(cfg EmailConfig, n Notification) error {
	to := splitRecipients(cfg.To)
	if len(to) == 0 {
		return fmt.Errorf("no recipients")
	}
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	msg := buildMessage(cfg.From, to, n.Title, n.Body)

	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}

	// Port 465 is implicit TLS (SMTPS): establish the TLS connection first.
	if cfg.Port == 465 {
		return sendImplicitTLS(addr, cfg.Host, auth, cfg.From, to, msg)
	}

	// Otherwise smtp.SendMail negotiates STARTTLS automatically when the server
	// advertises it (typical for 587/25).
	if err := smtp.SendMail(addr, auth, cfg.From, to, msg); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}

// sendImplicitTLS handles port-465 SMTPS where TLS wraps the whole session.
func sendImplicitTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp rcpt %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// buildMessage assembles a minimal RFC 5322 plain-text message.
func buildMessage(from string, to []string, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	b.WriteString("\r\n")
	return []byte(b.String())
}
