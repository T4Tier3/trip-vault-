// Package emailserver sends transactional email over SMTP.
//
// TripVault needs only a handful of messages, so this is deliberately a thin
// client rather than a template engine with a dependency tree.
package emailserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/tripvault/backend/internal/config"
)

// ErrNotConfigured is returned when email is required but disabled.
var ErrNotConfigured = errors.New("email: SMTP is not configured")

// Mailer sends email over SMTP.
//
// It reuses a single connection when possible. Verification and reset mails are
// low volume, so a connection per send would be simpler; pooling is kept here
// because a busy trip with many invitations (Phase 4) makes connection setup
// noticeable against a remote SMTP relay.
type Mailer struct {
	cfg    config.SMTP
	log    *slog.Logger
	mu     sync.Mutex
	client *smtp.Client
}

// New builds a Mailer. When SMTP is disabled the returned Mailer is inert and
// reports ErrNotConfigured, which is what lets tests run without a mail server.
func New(cfg config.SMTP, log *slog.Logger) *Mailer {
	if log == nil {
		log = slog.Default()
	}
	return &Mailer{cfg: cfg, log: log}
}

// Enabled reports whether the mailer will attempt delivery.
func (m *Mailer) Enabled() bool { return m.cfg.Enabled && m.cfg.Host != "" }

// Close releases a pooled SMTP connection.
func (m *Mailer) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		_ = m.client.Quit()
		m.client = nil
	}
}

// Send delivers a message. from is the full address including display name.
func (m *Mailer) Send(ctx context.Context, from, to, subject, body string) error {
	if !m.Enabled() {
		return ErrNotConfigured
	}
	if to == "" {
		return errors.New("email: recipient is empty")
	}

	message := buildMessage(from, to, subject, body)

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureClient(); err != nil {
		return err
	}
	if err := m.client.Mail(extractAddress(from)); err != nil {
		// The pooled connection may have gone stale; drop it so the next send
		// reconnects rather than failing forever.
		m.discard()
		return fmt.Errorf("email: MAIL FROM: %w", err)
	}
	if err := m.client.Rcpt(to); err != nil {
		m.discard()
		return fmt.Errorf("email: RCPT TO: %w", err)
	}
	w, err := m.client.Data()
	if err != nil {
		m.discard()
		return fmt.Errorf("email: DATA: %w", err)
	}
	if _, err := w.Write([]byte(message)); err != nil {
		m.discard()
		return fmt.Errorf("email: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		m.discard()
		return fmt.Errorf("email: close body: %w", err)
	}

	m.log.Info("email sent", "to", to, "subject", subject)
	return nil
}

// ensureClient connects on first use.
func (m *Mailer) ensureClient() error {
	if m.client != nil {
		return nil
	}

	var auth smtp.Auth
	if m.cfg.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
	}

	if m.cfg.TLS {
		conn, err := tls.Dial("tcp", m.cfg.Address(), &tls.Config{
			ServerName:         m.cfg.Host,
			InsecureSkipVerify: m.cfg.SkipVerify, //nolint:gosec // opt-in for self-signed dev relays
			MinVersion:         tls.VersionTLS12,
		})
		if err != nil {
			return fmt.Errorf("email: dial %s: %w", m.cfg.Address(), err)
		}
		client, err := smtp.NewClient(conn, m.cfg.Host)
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("email: smtp handshake: %w", err)
		}
		m.client = client
		return nil
	}

	client, err := smtp.Dial(m.cfg.Address())
	if err != nil {
		return fmt.Errorf("email: dial %s: %w", m.cfg.Address(), err)
	}
	if auth != nil {
		if ok, _ := client.Extension("AUTH"); ok {
			if err := client.Auth(auth); err != nil {
				_ = client.Close()
				return fmt.Errorf("email: auth: %w", err)
			}
		}
	}
	m.client = client
	return nil
}

// discard drops the pooled connection after a protocol error.
func (m *Mailer) discard() {
	if m.client != nil {
		_ = m.client.Close()
		m.client = nil
	}
}

// buildMessage assembles an RFC 5322 message with a plain text body.
func buildMessage(from, to, subject, body string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "From: %s\r\n", from)
	fmt.Fprintf(&sb, "To: %s\r\n", to)
	fmt.Fprintf(&sb, "Subject: %s\r\n", subject)
	sb.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	sb.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)
	return sb.String()
}

// extractAddress pulls the bare address out of a "Name <addr>" header.
func extractAddress(from string) string {
	start := strings.LastIndex(from, "<")
	end := strings.LastIndex(from, ">")
	if start >= 0 && end > start {
		return from[start+1 : end]
	}
	return strings.TrimSpace(from)
}
