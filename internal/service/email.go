package service

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"time"

	"github.com/resend/resend-go/v3"
)

// EmailConfig is the resolved mail configuration sourced from SystemConfig.
type EmailConfig struct {
	Enabled  bool
	Provider string // "smtp" | "resend" (empty ⇒ "smtp")
	Host     string
	Port     int
	User     string
	Pass     string
	From     string
	FromName string // optional display name, e.g. "VGate" → "VGate" <from>
	Security string // "none" | "starttls" | "ssl"
	// Resend-specific settings, used only when Provider == "resend".
	ResendAPIKey string
	ResendFrom   string
}

// SendFunc delivers a single email via SMTP. The production implementation
// (sendReal) uses net/smtp; tests inject a fake to avoid network I/O.
type SendFunc func(cfg EmailConfig, to, subject, htmlBody string) error

// ResendSendFunc delivers a single email via the Resend API. The production
// implementation (sendResend) calls resend-go; tests inject a fake.
type ResendSendFunc func(from, to, subject, htmlBody, apiKey string) error

// EmailService sends transactional mail (registration verification, admin
// announcements) via the configured backend (SMTP or Resend). Configuration
// lives in SystemConfig so it is editable from the admin UI at runtime; both
// senders are injectable for tests.
type EmailService struct {
	sysCfg     *SystemConfigService
	send       SendFunc
	resendSend ResendSendFunc
}

func NewEmailService(sysCfg *SystemConfigService) *EmailService {
	return &EmailService{sysCfg: sysCfg, send: sendReal, resendSend: sendResend}
}

// SetSender overrides the SMTP delivery function (used by tests).
func (s *EmailService) SetSender(f SendFunc) {
	s.send = f
}

// SetResendSender overrides the Resend delivery function (used by tests).
func (s *EmailService) SetResendSender(f ResendSendFunc) {
	s.resendSend = f
}

// GetConfig resolves the mail settings from SystemConfig.
func (s *EmailService) GetConfig() (EmailConfig, error) {
	m, err := s.sysCfg.GetAll()
	if err != nil {
		return EmailConfig{}, err
	}
	port, _ := strconv.Atoi(m[CfgKeyEmailSMTPPort])
	if port == 0 {
		port = 587
	}
	return EmailConfig{
		Enabled:      m[CfgKeyEmailEnabled] == "true",
		Provider:     m[CfgKeyEmailProvider],
		Host:         m[CfgKeyEmailSMTPHost],
		Port:         port,
		User:         m[CfgKeyEmailSMTPUser],
		Pass:         m[CfgKeyEmailSMTPPass],
		From:         m[CfgKeyEmailFrom],
		FromName:     m[CfgKeyEmailFromName],
		Security:     m[CfgKeyEmailSMTPSecurity],
		ResendAPIKey: m[CfgKeyEmailResendAPIKey],
		ResendFrom:   m[CfgKeyEmailFrom],
	}, nil
}

// Send delivers an HTML email to a single recipient, using the backend
// selected by email.provider. It is a no-op error when email is disabled or
// the chosen backend is not fully configured.
func (s *EmailService) Send(to, subject, htmlBody string) error {
	if to == "" {
		return errors.New("email: empty recipient")
	}
	cfg, err := s.GetConfig()
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return errors.New("email is not configured (set email.enabled=true)")
	}
	if cfg.Provider == "resend" {
		if cfg.ResendAPIKey == "" || cfg.ResendFrom == "" {
			return errors.New("resend email is not configured (set email.resend_api_key and email.from)")
		}
		return s.resendSend(formatFrom(cfg.FromName, cfg.ResendFrom), to, subject, htmlBody, cfg.ResendAPIKey)
	}
	// Default backend: SMTP.
	if cfg.Host == "" || cfg.From == "" {
		return errors.New("SMTP email is not configured (set email.smtp_host and email.from)")
	}
	return s.send(cfg, to, subject, htmlBody)
}

// SendVerification emails the registration verification link to the user. Both a
// clickable link and the raw token are included so verification still works when
// site.base_url is not configured (link is then empty).
func (s *EmailService) SendVerification(to, link, token string) error {
	subject := "Verify your vgate account"
	linkHTML := ""
	if link != "" {
		linkHTML = fmt.Sprintf(`<p><a href="%s">Verify my email</a></p><p>Or paste this link into your browser: %s</p>`, link, link)
	}
	tokenHTML := ""
	if token != "" {
		tokenHTML = fmt.Sprintf(`<p>Your verification token: <code>%s</code></p>`, token)
	}
	html := fmt.Sprintf(
		`<p>Welcome to vgate!</p><p>Please confirm your email address. Your account will be activated once verification completes.</p>%s%s`,
		linkHTML, tokenHTML)
	return s.Send(to, subject, html)
}

// SendAnnouncement emails an announcement (typically to all/active users).
func (s *EmailService) SendAnnouncement(to, title, content string) error {
	subject := "Announcement: " + title
	html := fmt.Sprintf(`<h3>%s</h3><div>%s</div>`, title, content)
	return s.Send(to, subject, html)
}

// formatFrom renders an RFC 5322 display sender: "Name" <addr> when a name is
// set, otherwise the bare address. It is applied only to the From: header and
// the Resend From field; the SMTP MAIL FROM command still uses the bare
// address (see sendReal).
func formatFrom(name, addr string) string {
	if addr == "" {
		return name
	}
	if name == "" {
		return addr
	}
	return fmt.Sprintf("%s <%s>", name, addr)
}

// buildMessage assembles an RFC 5322 message with a text/html body.
// from is the fully formatted display sender (e.g. "VGate" <noreply@vgate.io>).
func buildMessage(from, to, subject, htmlBody string) []byte {
	now := time.Now().Format(time.RFC1123Z)
	msg := "From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: " + now + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=\"UTF-8\"\r\n" +
		"\r\n" +
		htmlBody
	return []byte(msg)
}

// sendReal delivers mail via net/smtp, supporting none/starttls/ssl transports.
func sendReal(cfg EmailConfig, to, subject, htmlBody string) error {
	msg := buildMessage(formatFrom(cfg.FromName, cfg.From), to, subject, htmlBody)
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	var c *smtp.Client
	var err error
	switch cfg.Security {
	case "ssl":
		conn, derr := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host})
		if derr != nil {
			return fmt.Errorf("smtp tls dial: %w", derr)
		}
		c, err = smtp.NewClient(conn, cfg.Host)
	default:
		c, err = smtp.Dial(addr)
	}
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer c.Quit()

	if cfg.Security == "starttls" {
		if err := c.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}

	if cfg.User != "" {
		auth := smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := c.Mail(cfg.From); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	return w.Close()
}

// sendResend delivers mail via the Resend REST API (resend-go/v3).
func sendResend(from, to, subject, htmlBody, apiKey string) error {
	client := resend.NewClient(apiKey)
	params := &resend.SendEmailRequest{
		From:    from,
		To:      []string{to},
		Subject: subject,
		Html:    htmlBody,
	}
	if _, err := client.Emails.Send(params); err != nil {
		return fmt.Errorf("resend send: %w", err)
	}
	return nil
}
