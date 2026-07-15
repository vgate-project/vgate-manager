package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/util"
)

func newInviteTestDB(t *testing.T) (*gorm.DB, *SystemConfigService) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemConfig{}, &model.User{}, &model.InviteCode{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sysCfg := NewSystemConfigService(db)
	if err := sysCfg.SetAll(map[string]string{
		CfgKeyInviteDefaultUserQuota: "5",
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return db, sysCfg
}

func mustUser(t *testing.T, db *gorm.DB) string {
	t.Helper()
	u := &model.User{
		ID:         util.NewUserID(),
		Email:      "u" + util.RandomToken(4) + "@example.com",
		Username:   new("user_" + util.RandomToken(4)),
		SubToken:   util.RandomToken(16),
		Credential: util.NewCredential(),
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u.ID
}

func TestInviteQuota(t *testing.T) {
	db, sysCfg := newInviteTestDB(t)
	svc := NewInviteService(db, sysCfg)
	userID := mustUser(t, db)

	// Within quota: 3 uses ok.
	c, err := svc.CreateForUser(userID, 3, nil, "")
	if err != nil {
		t.Fatalf("CreateForUser(3): %v", err)
	}
	if c.MaxUses != 3 {
		t.Errorf("expected max_uses 3, got %d", c.MaxUses)
	}

	// Exceeds quota: 3 + 3 = 6 > 5.
	if _, err := svc.CreateForUser(userID, 3, nil, ""); err == nil {
		t.Fatal("expected quota-exceeded error")
	}

	// Exactly at limit: 2 more → 5 total is allowed.
	if _, err := svc.CreateForUser(userID, 2, nil, ""); err != nil {
		t.Fatalf("CreateForUser(2): %v", err)
	}
}

func TestInviteValidateAndConsume(t *testing.T) {
	db, sysCfg := newInviteTestDB(t)
	svc := NewInviteService(db, sysCfg)

	code, err := svc.CreateForAdmin(1, 2, nil, "promo")
	if err != nil {
		t.Fatalf("CreateForAdmin: %v", err)
	}

	c, err := svc.ValidateAndConsume(code.Code)
	if err != nil || c.UsedCount != 1 {
		t.Fatalf("consume 1: %v used=%d", err, c.UsedCount)
	}
	if _, err := svc.ValidateAndConsume(code.Code); err != nil {
		t.Fatalf("consume 2: %v", err)
	}
	if _, err := svc.ValidateAndConsume(code.Code); err == nil {
		t.Fatal("expected exhausted error on 3rd consume")
	}
}

func TestInviteExpired(t *testing.T) {
	db, sysCfg := newInviteTestDB(t)
	svc := NewInviteService(db, sysCfg)
	code, err := svc.CreateForAdmin(1, 1, new(time.Now().Add(-time.Hour)), "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.ValidateAndConsume(code.Code); err == nil {
		t.Fatal("expected expired error")
	}
}
