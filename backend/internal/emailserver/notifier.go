package emailserver

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/tripvault/backend/internal/auth"
	"github.com/tripvault/backend/internal/config"
)

// Notifier renders and sends the transactional messages TripVault sends.
//
// It implements auth.Notifier, so the auth service depends on that interface
// rather than on SMTP.
type Notifier struct {
	mailer *Mailer
	// scheme is the app's custom URL scheme, used for the links in email bodies.
	scheme  string
	appName string
	log     *slog.Logger
	// now is injectable so rendered timestamps are stable in tests.
	now func() time.Time
}

// NewNotifier builds a Notifier over mailer.
func NewNotifier(mailer *Mailer, cfg config.App, log *slog.Logger) *Notifier {
	if log == nil {
		log = slog.Default()
	}
	scheme := strings.ToLower(strings.TrimSpace(cfg.LinkScheme))
	if scheme == "" {
		scheme = "tripvault"
	}
	return &Notifier{
		mailer:  mailer,
		scheme:  scheme,
		appName: cfg.Name,
		log:     log,
		now:     time.Now,
	}
}

// WithClock replaces the time source, for tests only.
func (n *Notifier) WithClock(now func() time.Time) *Notifier {
	n.now = now
	return n
}

// from builds the From header.
//
// Configuration carries a bare address, so quoting and escaping happen here
// rather than in .env. A value that already carries a display name is passed
// through untouched.
func (n *Notifier) from() string {
	configured := strings.TrimSpace(n.mailer.cfg.From)
	if configured == "" {
		configured = "no-reply@" + strings.ToLower(n.appName)
	}
	if strings.Contains(configured, "<") {
		return configured
	}
	return fmt.Sprintf("%s <%s>", n.appName, configured)
}

// SendEmailVerification emails a verification link.
func (n *Notifier) SendEmailVerification(ctx context.Context, to, name, token string) error {
	link := n.deepLink("verify-email", token)

	subject := fmt.Sprintf("Verify your %s email address", n.appName)
	body := fmt.Sprintf(`Hi %s,

Welcome to %s. Confirm this address to activate your account:

%s

This link expires in %s. If you did not create a %s account you can
safely ignore this message.

- The %s team
`, displayName(name), n.appName, link, auth.EmailVerificationTTL, n.appName, n.appName)

	return n.mailer.Send(ctx, n.from(), to, subject, body)
}

// SendPasswordReset emails a reset link.
func (n *Notifier) SendPasswordReset(ctx context.Context, to, name, token string) error {
	link := n.deepLink("reset-password", token)

	subject := fmt.Sprintf("Reset your %s password", n.appName)
	body := fmt.Sprintf(`Hi %s,

We received a request to reset the password on your %s account.

Choose a new password here:

%s

This link expires in %s and can be used once. If you did not request a
reset, no action is needed - your current password still works.

- The %s team
`, displayName(name), n.appName, link, auth.PasswordResetTTL, n.appName)

	return n.mailer.Send(ctx, n.from(), to, subject, body)
}

// deepLink builds a link that opens the installed app on the given screen.
//
// The link targets the app's custom URL scheme rather than a web page. The API
// serves no HTML, so an https link here would give the user a dead end, and a
// dead password-reset link is a permanent lockout.
func (n *Notifier) deepLink(screen, token string) string {
	return fmt.Sprintf("%s://%s?token=%s", n.scheme, screen, url.QueryEscape(token))
}

// displayName falls back to something neutral when the profile name is blank.
func displayName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "there"
	}
	return html.EscapeString(trimmed)
}
