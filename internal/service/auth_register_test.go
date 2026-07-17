package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// TestRegisterPendingIssuesSession locks the A1 contract: when email
// verification is required, registration still returns a session token (status
// 202 on the wire) so the client can auto-log-in and surface the in-dashboard
// verify banner, instead of stranding the user on a "waiting for verification"
// page. Unverified users can log in (verification only gates purchases/traffic).
func TestRegisterPendingIssuesSession(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.SystemConfig{}, &model.EmailVerification{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Seed config before constructing the service so its on-init cache warm
	// picks up the rows (the service only re-reads the DB on a cold cache).
	if err := db.Create(&model.SystemConfig{Key: CfgKeyRegisterEnabled, Value: "true"}).Error; err != nil {
		t.Fatalf("seed register_enabled: %v", err)
	}
	if err := db.Create(&model.SystemConfig{Key: CfgKeyRegisterRequireEmailVerify, Value: "true"}).Error; err != nil {
		t.Fatalf("seed require_email_verify: %v", err)
	}

	sysCfg := NewSystemConfigService(db)

	authSvc := NewAuthService(db, "test-secret", time.Hour, time.Hour)
	authSvc.SetConfigService(sysCfg)

	user, token, _, pending, err := authSvc.RegisterUser("alice", "alice@example.com", "secret-pass-123", "")
	if err != nil {
		t.Fatalf("RegisterUser: %v", err)
	}
	if !pending {
		t.Fatal("expected pending=true when email verification is required")
	}
	if token == "" {
		t.Error("expected a session token for a pending (unverified) registration")
	}
	if user.EmailVerified {
		t.Error("a pending registration must not mark the user email-verified")
	}

	// The issued session must actually log the (still unverified) user in,
	// confirming the 202 token is a usable credential under Option A.
	if _, _, _, lerr := authSvc.UserLogin("alice@example.com", "secret-pass-123"); lerr != nil {
		t.Errorf("issued session could not log the unverified user in: %v", lerr)
	}
}
