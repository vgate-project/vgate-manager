package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

func authTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.SystemConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestRegisterUserGrantsTrial verifies that, when the trial is enabled, a newly
// registered user receives the configured free quota, has quota reset disabled,
// and gets an ExpireAt ~= the configured duration out.
func TestRegisterUserGrantsTrial(t *testing.T) {
	db := authTestDB(t)
	sys := NewSystemConfigService(db)
	if err := sys.SetAll(map[string]string{
		CfgKeyRegisterEnabled:   "true",
		CfgKeyTrialEnabled:      "true",
		CfgKeyTrialQuotaBytes:   "1073741824",
		CfgKeyTrialDurationDays: "7",
	}); err != nil {
		t.Fatalf("SetAll: %v", err)
	}
	auth := NewAuthService(db, "secret", time.Hour, 24*time.Hour)
	auth.SetConfigService(sys)

	u, _, _, _, err := auth.RegisterUser("trialuser", "trial@example.com", "password123", "")
	if err != nil {
		t.Fatalf("RegisterUser: %v", err)
	}
	if u.QuotaBytes != 1073741824 {
		t.Errorf("QuotaBytes = %d, want 1073741824", u.QuotaBytes)
	}
	if u.QuotaResetEnabled {
		t.Errorf("QuotaResetEnabled = true, want false")
	}
	if u.ExpireAt == nil {
		t.Fatalf("ExpireAt = nil, want set")
	}
	remaining := time.Until(*u.ExpireAt)
	if remaining < 6*24*time.Hour || remaining > 8*24*time.Hour {
		t.Errorf("ExpireAt %v not ~7 days out (remaining %v)", *u.ExpireAt, remaining)
	}
}

// TestRegisterUserNoTrialWhenDisabled verifies that, with the trial disabled, a
// new user gets no quota and no expiry (default blocked state).
func TestRegisterUserNoTrialWhenDisabled(t *testing.T) {
	db := authTestDB(t)
	sys := NewSystemConfigService(db)
	if err := sys.SetAll(map[string]string{
		CfgKeyRegisterEnabled: "true",
		CfgKeyTrialEnabled:    "false",
	}); err != nil {
		t.Fatalf("SetAll: %v", err)
	}
	auth := NewAuthService(db, "secret", time.Hour, 24*time.Hour)
	auth.SetConfigService(sys)

	u, _, _, _, err := auth.RegisterUser("nouser", "no@example.com", "password123", "")
	if err != nil {
		t.Fatalf("RegisterUser: %v", err)
	}
	if u.QuotaBytes != 0 {
		t.Errorf("QuotaBytes = %d, want 0 (trial disabled)", u.QuotaBytes)
	}
	if u.ExpireAt != nil {
		t.Errorf("ExpireAt = %v, want nil", *u.ExpireAt)
	}
}
