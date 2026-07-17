package service

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"time"
)

// EmailConfig is the resolved SMTP configuration sourced from SystemConfig.
type EmailConfig struct {
	Enabled  bool
	Host     string
	Port     int
	User     string
	Pass     string
	From     string
	Security string // "none" | "starttls" | "ssl"
}

// SendFunc delivers a single email. The production implementation (sendReal)
// uses net/smtp; tests inject a fake to avoid network I/O.
type SendFunc func(cfg EmailConfig, to, subject, htmlBody string) error

// EmailService sends transactional mail (registration verification, admin
// announcements) via SMTP. Configuration lives in SystemConfig so it is
// editable from the admin UI at runtime; the Sender is injectable for tests.
type EmailService struct {
	sysCfg *SystemConfigService
	send   SendFunc
}

func NewEmailService(sysCfg *SystemConfigService) *EmailService {
	return &EmailService{sysCfg: sysCfg, send: sendReal}
}

// SetSender overrides the delivery function (used by tests).
func (s *EmailService) SetSender(f SendFunc) {
	s.send = f
}

// GetConfig resolves the SMTP settings from SystemConfig.
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
		Enabled:  m[CfgKeyEmailEnabled] == "true",
		Host:     m[CfgKeyEmailSMTPHost],
		Port:     port,
		User:     m[CfgKeyEmailSMTPUser],
		Pass:     m[CfgKeyEmailSMTPPass],
		From:     m[CfgKeyEmailSMTPFrom],
		Security: m[CfgKeyEmailSMTPSecurity],
	}, nil
}

// Send delivers an HTML email to a single recipient. It is a no-op error when
// email is disabled or not fully configured.
func (s *EmailService) Send(to, subject, htmlBody string) error {
	if to == "" {
		return errors.New("email: empty recipient")
	}
	cfg, err := s.GetConfig()
	if err != nil {
		return err
	}
	if !cfg.Enabled || cfg.Host == "" || cfg.From == "" {
		return errors.New("email is not configured (set email.enabled=true and SMTP host/from)")
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

// buildMessage assembles an RFC 5322 message with a text/html body.
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
	msg := buildMessage(cfg.From, to, subject, htmlBody)
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
