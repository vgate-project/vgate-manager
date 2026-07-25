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
	pkg := model.TrafficPackage{ID: "tp1", QuotaBytes: 500}

	if err := db.Transaction(func(tx *gorm.DB) error {
		var u model.User
		if err := tx.Where("id = ?", "u1").First(&u).Error; err != nil {
			return err
		}
		return applyTrafficEffect(tx, &u, &pkg)
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
	// A traffic-package grant is a permanent tracked bonus on top of the plan's
	// quota; the FIFO consumption order is by GrantedAt.
	if grants[0].QuotaBytes != 500 {
		t.Errorf("grant QuotaBytes = %d, want 500", grants[0].QuotaBytes)
	}
}
