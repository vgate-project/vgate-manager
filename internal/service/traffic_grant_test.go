package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

func grantTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Plan{}, &model.PlanPrice{},
		&model.TrafficPackage{}, &model.Order{}, &model.TrafficGrant{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestApplyTrafficEffectTracksBonus(t *testing.T) {
	db := grantTestDB(t)
	now := time.Now()
	expire := now.Add(5 * 24 * time.Hour)
	user := model.User{ID: "u1", Credential: "u1", Email: "u1@example.com", SubToken: "sub-u1",
		Level: 1, ExpireAt: &expire, QuotaBytes: 1000}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	pkg := model.TrafficPackage{ID: "tp1", QuotaBytes: 500, ValidityDays: 7}

	if err := db.Transaction(func(tx *gorm.DB) error {
		var u model.User
		if err := tx.Where("id = ?", "u1").First(&u).Error; err != nil {
			return err
		}
		return applyTrafficEffect(tx, &u, &pkg, 7)
	}); err != nil {
		t.Fatal(err)
	}

	var got model.User
	db.Where("id = ?", "u1").First(&got)
	if got.QuotaBytes != 1000 {
		t.Errorf("base QuotaBytes = %d, want 1000 (must be untouched by traffic purchase)", got.QuotaBytes)
	}
	if got.TrafficQuotaBytes != 500 {
		t.Errorf("TrafficQuotaBytes = %d, want 500", got.TrafficQuotaBytes)
	}

	var grants []model.TrafficGrant
	db.Where("user_id = ?", "u1").Find(&grants)
	if len(grants) != 1 {
		t.Fatalf("expected 1 grant, got %d", len(grants))
	}
	if grants[0].ExpireAt == nil {
		t.Fatal("expected grant ExpireAt to be set for validity_days > 0")
	}
	if !grants[0].ExpireAt.After(now) {
		t.Errorf("grant ExpireAt should be in the future, got %v", grants[0].ExpireAt)
	}
	if grants[0].Reclaimed {
		t.Error("grant should not be reclaimed yet")
	}
}

func TestReclaimExpiredTrafficGrants(t *testing.T) {
	db := grantTestDB(t)
	now := time.Now()
	expire := now.Add(5 * 24 * time.Hour)
	user := model.User{ID: "u1", Credential: "u1", Email: "u1@example.com", SubToken: "sub-u1",
		Level: 1, ExpireAt: &expire, QuotaBytes: 1000}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	pkg := model.TrafficPackage{ID: "tp1", QuotaBytes: 500, ValidityDays: 1}

	if err := db.Transaction(func(tx *gorm.DB) error {
		var u model.User
		if err := tx.Where("id = ?", "u1").First(&u).Error; err != nil {
			return err
		}
		return applyTrafficEffect(tx, &u, &pkg, 1)
	}); err != nil {
		t.Fatal(err)
	}

	// Force the grant's expiry into the past so it is reclaimable.
	past := now.Add(-time.Hour)
	if err := db.Model(&model.TrafficGrant{}).Where("user_id = ?", "u1").
		Update("expire_at", &past).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewUserService(db, nil)
	n, err := svc.ReclaimExpiredTrafficGrants()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("reclaimed = %d, want 1", n)
	}

	var got model.User
	db.Where("id = ?", "u1").First(&got)
	if got.TrafficQuotaBytes != 0 {
		t.Errorf("TrafficQuotaBytes = %d, want 0 after reclaim", got.TrafficQuotaBytes)
	}
	if got.QuotaBytes != 1000 {
		t.Errorf("base QuotaBytes = %d, want 1000 (untouched)", got.QuotaBytes)
	}

	var g model.TrafficGrant
	db.Where("user_id = ?", "u1").First(&g)
	if !g.Reclaimed {
		t.Error("grant should be marked reclaimed")
	}

	// A second reclaim must be a no-op (already reclaimed).
	n2, err := svc.ReclaimExpiredTrafficGrants()
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("second reclaim = %d, want 0", n2)
	}
}

func TestReclaimSkipsPermanentGrants(t *testing.T) {
	db := grantTestDB(t)
	user := model.User{ID: "u1", Credential: "u1", Email: "u1@example.com", SubToken: "sub-u1",
		Level: 1, QuotaBytes: 1000}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	// validity_days = 0 → permanent grant (nil ExpireAt), never reclaimed.
	pkg := model.TrafficPackage{ID: "tp0", QuotaBytes: 500, ValidityDays: 0}
	if err := db.Transaction(func(tx *gorm.DB) error {
		var u model.User
		if err := tx.Where("id = ?", "u1").First(&u).Error; err != nil {
			return err
		}
		return applyTrafficEffect(tx, &u, &pkg, 0)
	}); err != nil {
		t.Fatal(err)
	}

	svc := NewUserService(db, nil)
	n, err := svc.ReclaimExpiredTrafficGrants()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("reclaimed = %d, want 0 (permanent grant must not be reclaimed)", n)
	}

	var got model.User
	db.Where("id = ?", "u1").First(&got)
	if got.TrafficQuotaBytes != 500 {
		t.Errorf("TrafficQuotaBytes = %d, want 500 (permanent grant kept)", got.TrafficQuotaBytes)
	}
}
