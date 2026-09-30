package emailserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tripvault/backend/internal/config"
)

// smtpSink is a throwaway SMTP server that captures one message and lets the
// test assert on the bytes that would reach a real mailbox.
type smtpSink struct {
	ln       net.Listener
	messages chan string
	// conns tracks accepted connections so a test can kill them and simulate a
	// peer that went away. Closing the listener alone would not, because already
	// accepted connections stay open.
	conns   []net.Conn
	connsMu sync.Mutex
}

// killAll closes every live connection, leaving the listener closed too.
func (s *smtpSink) killAll() {
	_ = s.ln.Close()
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.conns = nil
}

func newSMTPSink(t *testing.T) *smtpSink {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &smtpSink{ln: ln, messages: make(chan string, 8)}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.connsMu.Lock()
			s.conns = append(s.conns, conn)
			s.connsMu.Unlock()
			go s.handle(conn)
		}
	}()
	return s
}

func (s *smtpSink) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Minimal but valid SMTP dialogue.
	_, _ = conn.Write([]byte("220 sink ESMTP\r\n"))
	scanner := newLineScanner(conn)
	var inData bool
	var body strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		if inData {
			// A lone dot terminates the DATA section.
			if line == "." {
				inData = false
				select {
				case s.messages <- body.String():
				default:
				}
				_, _ = conn.Write([]byte("250 OK queued\r\n"))
				continue
			}
			// Undo the transparency dot.
			body.WriteString(strings.TrimPrefix(line, "."))
			body.WriteString("\n")
			continue
		}

		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			_, _ = conn.Write([]byte("250-sink\r\n250 AUTH PLAIN LOGIN\r\n"))
		case strings.HasPrefix(line, "AUTH"):
			_, _ = conn.Write([]byte("235 authenticated\r\n"))
		case strings.HasPrefix(line, "MAIL FROM"):
			_, _ = conn.Write([]byte("250 OK\r\n"))
		case strings.HasPrefix(line, "RCPT TO"):
			_, _ = conn.Write([]byte("250 OK\r\n"))
		case strings.HasPrefix(line, "DATA"):
			inData = true
			_, _ = conn.Write([]byte("354 send data\r\n"))
		case strings.HasPrefix(line, "QUIT"):
			_, _ = conn.Write([]byte("221 bye\r\n"))
			return
		default:
			_, _ = conn.Write([]byte("250 OK\r\n"))
		}
	}
}

func (s *smtpSink) addr() string { return s.ln.Addr().String() }

// next waits for the next captured message.
func (s *smtpSink) next(t *testing.T) string {
	t.Helper()
	select {
	case msg := <-s.messages:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an email")
		return ""
	}
}

type lineScanner struct {
	conn net.Conn
	buf  []byte
}

func newLineScanner(conn net.Conn) *lineScanner { return &lineScanner{conn: conn} }

func (s *lineScanner) Scan() bool {
	line, err := s.readLine()
	if err != nil {
		return false
	}
	s.buf = line
	return true
}

func (s *lineScanner) Text() string { return string(s.buf) }

func (s *lineScanner) readLine() ([]byte, error) {
	var line []byte
	one := make([]byte, 1)
	for {
		n, err := s.conn.Read(one)
		if n == 0 || err != nil {
			return nil, err
		}
		if one[0] == '\n' {
			return line, nil
		}
		if one[0] != '\r' {
			line = append(line, one[0])
		}
	}
}

func newTestNotifier(t *testing.T, sink *smtpSink, app config.App) *Notifier {
	t.Helper()

	host, port, _ := net.SplitHostPort(sink.addr())
	cfg := config.SMTP{
		Host:     host,
		Port:     mustAtoi(t, port),
		From:     "no-reply@tripvault.test",
		Username: "tripvault",
		Password: "secret",
		Enabled:  true,
		TLS:      false,
	}

	mailer := New(cfg, slogQuiet())
	t.Cleanup(mailer.Close)

	fixed := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	return NewNotifier(mailer, app, slogQuiet()).WithClock(func() time.Time { return fixed })
}

func slogQuiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNotifier_VerificationEmailCarriesAUsableDeepLink(t *testing.T) {
	sink := newSMTPSink(t)
	notifier := newTestNotifier(t, sink, testApp())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := notifier.SendEmailVerification(ctx, "saurabh@example.com", "Saurabh", "verify-token-123"); err != nil {
		t.Fatalf("SendEmailVerification() error = %v", err)
	}

	msg := sink.next(t)

	if !strings.Contains(msg, "To: saurabh@example.com") {
		t.Errorf("message is not addressed to the user:\n%s", msg)
	}
	if !strings.Contains(msg, "Subject: Verify your TripVault email address") {
		t.Errorf("unexpected subject:\n%s", msg)
	}
	if !strings.Contains(msg, "From: TripVault <no-reply@tripvault.test>") {
		t.Errorf("From header is not quoted as expected:\n%s", msg)
	}

	// The link must open the app. A link back to the API would land the user on
	// a JSON response instead.
	link := extractFirstLink(t, msg)
	assertDeepLink(t, link, "tripvault", "verify-email", "verify-token-123")

	// The address is confirmed by tapping the link, so say so and give a fallback.
	if !strings.Contains(msg, "expires") {
		t.Error("body does not mention expiry, so the user cannot judge a stale link")
	}
	if !strings.Contains(msg, "ignore this message") {
		t.Error("body does not tell someone who did not sign up that they can ignore it")
	}
}

func TestNotifier_ResetEmailCarriesAUsableDeepLink(t *testing.T) {
	sink := newSMTPSink(t)
	notifier := newTestNotifier(t, sink, testApp())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := notifier.SendPasswordReset(ctx, "saurabh@example.com", "Saurabh", "reset-token-abc"); err != nil {
		t.Fatalf("SendPasswordReset() error = %v", err)
	}

	msg := sink.next(t)

	if !strings.Contains(msg, "Subject: Reset your TripVault password") {
		t.Errorf("unexpected subject:\n%s", msg)
	}

	link := extractFirstLink(t, msg)
	assertDeepLink(t, link, "tripvault", "reset-password", "reset-token-abc")

	// A reset mail that does not say the current password still works invites
	// people to reset because they were phished.
	if !strings.Contains(msg, "current password still works") {
		t.Errorf("body does not reassure the user:\n%s", msg)
	}
	if !strings.Contains(msg, "used once") {
		t.Error("body does not state that the link is single use")
	}
}

// TestNotifier_LinkSurvivesUnusualTokens covers the escaping requirement: a
// token that is not URL-safe must not be able to break out of the query string.
func TestNotifier_LinkSurvivesUnusualTokens(t *testing.T) {
	sink := newSMTPSink(t)
	notifier := newTestNotifier(t, sink, testApp())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const token = "a+b/c=d&e=f g"
	if err := notifier.SendPasswordReset(ctx, "saurabh@example.com", "Saurabh", token); err != nil {
		t.Fatalf("SendPasswordReset() error = %v", err)
	}

	link := extractFirstLink(t, sink.next(t))

	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("the emailed link does not parse: %v (%q)", err, link)
	}
	// A token containing & or = would otherwise truncate or inject parameters.
	if got := parsed.Query().Get("token"); got != token {
		t.Errorf("token round trip = %q, want %q (link %q)", got, token, link)
	}
	if len(parsed.Query()) != 1 {
		t.Errorf("link carries unexpected parameters: %v", parsed.Query())
	}
}

func TestNotifier_CustomSchemeIsUsed(t *testing.T) {
	sink := newSMTPSink(t)
	app := testApp()
	app.LinkScheme = "my-trips"
	notifier := newTestNotifier(t, sink, app)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := notifier.SendPasswordReset(ctx, "saurabh@example.com", "Saurabh", "tok"); err != nil {
		t.Fatalf("SendPasswordReset() error = %v", err)
	}

	assertDeepLink(t, extractFirstLink(t, sink.next(t)), "my-trips", "reset-password", "tok")
}

func TestDisplayName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Saurabh", "Saurabh"},
		{"  Saurabh  ", "Saurabh"},
		{"", "there"},
		{"   ", "there"},
		// A name is user input rendered into an email, so markup must not survive.
		{"<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
	}

	for _, tt := range tests {
		if got := displayName(tt.in); got != tt.want {
			t.Errorf("displayName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuildMessage(t *testing.T) {
	msg := buildMessage("TripVault <no-reply@x.test>", "a@b.test", "Subject here", "Body line")

	for _, want := range []string{
		"From: TripVault <no-reply@x.test>\r\n",
		"To: a@b.test\r\n",
		"Subject: Subject here\r\n",
		"Date: ",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=UTF-8\r\n",
		"\r\n\r\nBody line",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
	// Headers and body must be separated by a blank line, or the first body line
	// is parsed as a header.
	if !strings.Contains(msg, "\r\n\r\n") {
		t.Error("no header/body separator")
	}
}

func TestExtractAddress(t *testing.T) {
	tests := []struct{ in, want string }{
		{"no-reply@x.test", "no-reply@x.test"},
		{"TripVault <no-reply@x.test>", "no-reply@x.test"},
		{"  spaced@x.test  ", "spaced@x.test"},
		{"no angle brackets here", "no angle brackets here"},
		{"", ""},
	}

	for _, tt := range tests {
		if got := extractAddress(tt.in); got != tt.want {
			t.Errorf("extractAddress(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestMailer_DisabledReportsFailure matters for more than tidiness. If a
// disabled mailer returned nil, a misconfigured deployment would report
// verification and reset emails as delivered while nothing was sent, and the
// user would be locked out with no explanation.
func TestMailer_DisabledReportsFailure(t *testing.T) {
	mailer := New(config.SMTP{Enabled: false}, slogQuiet())
	t.Cleanup(mailer.Close)

	err := mailer.Send(context.Background(), "a@b.test", "c@d.test", "s", "b")
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Send() error = %v, want ErrNotConfigured", err)
	}
	if mailer.Enabled() {
		t.Error("Enabled() = true, want false")
	}
}

func TestMailer_EnabledNeedsAHost(t *testing.T) {
	// Enabled without a host would dial an empty address at the first send.
	mailer := New(config.SMTP{Enabled: true}, slogQuiet())
	t.Cleanup(mailer.Close)

	if mailer.Enabled() {
		t.Error("Enabled() = true, want false when no host is configured")
	}
}

func TestMailer_RejectsAnEmptyRecipient(t *testing.T) {
	sink := newSMTPSink(t)
	host, port, _ := net.SplitHostPort(sink.addr())
	mailer := New(config.SMTP{
		Host: host, Port: mustAtoi(t, port), Enabled: true,
	}, slogQuiet())
	t.Cleanup(mailer.Close)

	if err := mailer.Send(context.Background(), "a@b.test", "", "s", "b"); err == nil {
		t.Error("Send() error = nil, want a failure for an empty recipient")
	}
}

// TestMailer_RecoversFromADeadPeer proves a broken pooled connection does not
// wedge every later send.
func TestMailer_RecoversFromADeadPeer(t *testing.T) {
	sink := newSMTPSink(t)
	host, port, _ := net.SplitHostPort(sink.addr())
	mailer := New(config.SMTP{
		Host: host, Port: mustAtoi(t, port), Enabled: true,
	}, slogQuiet())
	t.Cleanup(mailer.Close)

	ctx := context.Background()
	notifier := NewNotifier(mailer, testApp(), slogQuiet())

	// First send establishes the connection.
	if err := notifier.SendPasswordReset(ctx, "a@b.test", "Saurabh", "tok-1"); err != nil {
		t.Fatalf("first send error = %v", err)
	}
	_ = sink.next(t)

	// Kill the server so the pooled connection is now dead.
	sink.killAll()

	// This send must fail...
	if err := notifier.SendPasswordReset(ctx, "a@b.test", "Saurabh", "tok-2"); err == nil {
		t.Error("send to a dead server error = nil, want a failure")
	}

	// ...and the mailer must not be stuck holding that dead connection.
	if mailer.client != nil {
		t.Error("a dead connection was kept; later sends would keep failing")
	}
}

func testApp() config.App {
	return config.App{
		Name:       "TripVault",
		BaseURL:    "https://api.tripvault.test",
		LinkScheme: "tripvault",
	}
}

func extractFirstLink(t *testing.T, msg string) string {
	t.Helper()
	for _, line := range strings.Split(msg, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "://") && strings.Contains(trimmed, "token=") {
			return trimmed
		}
	}
	t.Fatalf("no link found in message:\n%s", msg)
	return ""
}

func assertDeepLink(t *testing.T, link, wantScheme, wantScreen, wantToken string) {
	t.Helper()

	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("link does not parse: %v (%q)", err, link)
	}
	if parsed.Scheme != wantScheme {
		t.Errorf("link scheme = %q, want %q (link %q)", parsed.Scheme, wantScheme, link)
	}
	if parsed.Host != wantScreen {
		t.Errorf("link target = %q, want %q (link %q)", parsed.Host, wantScreen, link)
	}
	if got := parsed.Query().Get("token"); got != wantToken {
		t.Errorf("link token = %q, want %q", got, wantToken)
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("not a port: %q", s)
	}
	return n
}
