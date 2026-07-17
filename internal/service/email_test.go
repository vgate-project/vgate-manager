package service

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

func newEmailTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

type fakeSent struct {
	cfg     EmailConfig
	to      string
	subject string
	html    string
}

func TestEmailServiceSend(t *testing.T) {
	db := newEmailTestDB(t)
	sysCfg := NewSystemConfigService(db)
	svc := NewEmailService(sysCfg)

	var sent []fakeSent
	svc.SetSender(func(cfg EmailConfig, to, subject, html string) error {
		sent = append(sent, fakeSent{cfg, to, subject, html})
		return nil
	})

	// Not configured → error.
	if err := svc.Send("a@b.com", "subj", "<p>hi</p>"); err == nil {
		t.Fatal("expected error when email disabled")
	}

	// Enable + configure.
	if err := sysCfg.SetAll(map[string]string{
		CfgKeyEmailEnabled:      "true",
		CfgKeyEmailSMTPHost:     "smtp.example.com",
		CfgKeyEmailSMTPPort:     "587",
		CfgKeyEmailSMTPUser:     "user",
		CfgKeyEmailSMTPPass:     "pass",
		CfgKeyEmailFrom:         "noreply@vgate.io",
		CfgKeyEmailFromName:     "VGate",
		CfgKeyEmailSMTPSecurity: "starttls",
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// SendVerification.
	link := "https://app.vgate.io/verify-email?token=abc123"
	if err := svc.SendVerification("user@example.com", link, "abc123"); err != nil {
		t.Fatalf("SendVerification: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent email, got %d", len(sent))
	}
	if sent[0].to != "user@example.com" {
		t.Errorf("unexpected recipient: %s", sent[0].to)
	}
	if !strings.Contains(sent[0].subject, "Verify") {
		t.Errorf("unexpected subject: %s", sent[0].subject)
	}
	if !strings.Contains(sent[0].html, link) {
		t.Errorf("verification link missing from body")
	}
	if sent[0].cfg.Security != "starttls" || sent[0].cfg.Port != 587 {
		t.Errorf("config not resolved: %+v", sent[0].cfg)
	}
	if sent[0].cfg.FromName != "VGate" {
		t.Errorf("expected FromName VGate, got %q", sent[0].cfg.FromName)
	}

	// SendAnnouncement.
	if err := svc.SendAnnouncement("user@example.com", "Maintenance", "<p>soon</p>"); err != nil {
		t.Fatalf("SendAnnouncement: %v", err)
	}
	if len(sent) != 2 {
		t.Fatalf("expected 2 sent emails, got %d", len(sent))
	}
	if !strings.Contains(sent[1].subject, "Maintenance") {
		t.Errorf("unexpected subject: %s", sent[1].subject)
	}
}

func TestEmailServiceConfigDefaults(t *testing.T) {
	db := newEmailTestDB(t)
	sysCfg := NewSystemConfigService(db)
	cfg, err := NewEmailService(sysCfg).GetConfig()
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	// Empty config should parse to safe defaults (disabled, port 587).
	if cfg.Enabled {
		t.Error("expected disabled by default")
	}
	if cfg.Port != 587 {
		t.Errorf("expected default port 587, got %d", cfg.Port)
	}
}
