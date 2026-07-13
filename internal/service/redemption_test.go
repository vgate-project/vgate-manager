package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/util"
)

func newRedemptionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// Force a single connection so the in-memory sqlite DB (and its migrated
	// tables, including plans) is shared across all queries.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&model.User{},
		&model.Plan{},
		&model.PlanPrice{},
		&model.RedemptionCode{},
		&model.RedemptionRecord{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func mustRedeemUser(t *testing.T, db *gorm.DB) string {
	t.Helper()
	u := &model.User{
		ID:         util.NewUserID(),
		Email:      "u" + util.RandomToken(4) + "@example.com",
		SubToken:   util.RandomToken(16),
		Credential: util.NewCredential(),
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u.ID
}

func TestRedemptionBatchGenerate(t *testing.T) {
	db := newRedemptionTestDB(t)
	svc := NewRedemptionService(db)

	codes, err := svc.BatchGenerate(1, dto.AdminGenerateRedemptionRequest{Type: model.RedeemTypeTraffic, QuotaBytes: 1 << 30, MaxUses: 1, Count: 3, Note: "promo"})
	if err != nil {
		t.Fatalf("BatchGenerate: %v", err)
	}
	if len(codes) != 3 {
		t.Fatalf("expected 3 codes, got %d", len(codes))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if seen[c.Code] {
			t.Fatal("duplicate code string generated")
		}
		seen[c.Code] = true
	}

	// Validation: traffic without quota_bytes fails.
	if _, err := svc.BatchGenerate(1, dto.AdminGenerateRedemptionRequest{Type: model.RedeemTypeTraffic, MaxUses: 1, Count: 1}); err == nil {
		t.Fatal("expected quota_bytes validation error")
	}
	// Validation: unknown type fails.
	if _, err := svc.BatchGenerate(1, dto.AdminGenerateRedemptionRequest{Type: "bogus", MaxUses: 1, Count: 1}); err == nil {
		t.Fatal("expected invalid type error")
	}
}

func TestRedemptionTraffic(t *testing.T) {
	db := newRedemptionTestDB(t)
	svc := NewRedemptionService(db)
	userID := mustRedeemUser(t, db)

	codes, err := svc.BatchGenerate(1, dto.AdminGenerateRedemptionRequest{Type: model.RedeemTypeTraffic, QuotaBytes: 1 << 30, MaxUses: 2, Count: 1})
	if err != nil {
		t.Fatalf("BatchGenerate: %v", err)
	}
	rec, msg, err := svc.Redeem(userID, codes[0].Code)
	if err != nil || rec == nil {
		t.Fatalf("Redeem: %v rec=%v", err, rec)
	}
	if msg == "" {
		t.Fatal("expected non-empty benefit message")
	}

	var u model.User
	db.First(&u, "id = ?", userID)
	if u.QuotaBytes != 1<<30 {
		t.Errorf("expected quota 1<<30, got %d", u.QuotaBytes)
	}
	if u.QuotaResetEnabled {
		t.Error("expected quota_reset_enabled false after traffic grant")
	}

	// Second redeem by the same user → already redeemed (code not yet exhausted).
	if _, _, err := svc.Redeem(userID, codes[0].Code); err != ErrAlreadyRedeemed {
		t.Fatalf("expected ErrAlreadyRedeemed, got %v", err)
	}
}

func TestRedemptionDuration(t *testing.T) {
	db := newRedemptionTestDB(t)
	svc := NewRedemptionService(db)
	userID := mustRedeemUser(t, db)

	codes, err := svc.BatchGenerate(1, dto.AdminGenerateRedemptionRequest{Type: model.RedeemTypeDuration, DurationDays: 7, MaxUses: 1, Count: 1})
	if err != nil {
		t.Fatalf("BatchGenerate: %v", err)
	}
	if _, _, err := svc.Redeem(userID, codes[0].Code); err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	var u model.User
	db.First(&u, "id = ?", userID)
	if u.ExpireAt == nil {
		t.Fatal("expected expire_at set")
	}
	if u.ExpireAt.Before(time.Now().Add(6 * 24 * time.Hour)) {
		t.Errorf("expected ~7 day extension, got %v", u.ExpireAt)
	}
}

func TestRedemptionPlan(t *testing.T) {
	db := newRedemptionTestDB(t)
	svc := NewRedemptionService(db)
	userID := mustRedeemUser(t, db)

	plan := &model.Plan{
		ID:         util.NewPlanID(),
		Name:       "Test Plan",
		QuotaBytes: 5 << 30,
		Level:      2,
		Enabled:    true,
		Prices:     []model.PlanPrice{{ID: util.NewPlanPriceID(), Period: "month", DurationDays: 30, Price: 100, Enabled: true}},
	}
	if err := db.Create(plan).Error; err != nil {
		t.Fatalf("create plan: %v", err)
	}

	codes, err := svc.BatchGenerate(1, dto.AdminGenerateRedemptionRequest{Type: model.RedeemTypePlan, PlanID: plan.ID, MaxUses: 1, Count: 1})
	if err != nil {
		t.Fatalf("BatchGenerate: %v", err)
	}
	if _, _, err := svc.Redeem(userID, codes[0].Code); err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	var u model.User
	db.First(&u, "id = ?", userID)
	if u.Level != 2 {
		t.Errorf("expected level 2, got %d", u.Level)
	}
	if u.QuotaBytes != 5<<30 {
		t.Errorf("expected quota 5<<30, got %d", u.QuotaBytes)
	}
	if u.CurrentProductID != plan.ID {
		t.Errorf("expected current product %s, got %s", plan.ID, u.CurrentProductID)
	}
}

func TestRedemptionReset(t *testing.T) {
	db := newRedemptionTestDB(t)
	svc := NewRedemptionService(db)
	userID := mustRedeemUser(t, db)

	var u model.User
	db.First(&u, "id = ?", userID)
	u.UpTotal = 100
	u.DownTotal = 200
	db.Save(&u)

	codes, err := svc.BatchGenerate(1, dto.AdminGenerateRedemptionRequest{Type: model.RedeemTypeReset, MaxUses: 1, Count: 1})
	if err != nil {
		t.Fatalf("BatchGenerate: %v", err)
	}
	if _, _, err := svc.Redeem(userID, codes[0].Code); err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	db.First(&u, "id = ?", userID)
	if u.UpTotal != 0 || u.DownTotal != 0 {
		t.Errorf("expected counters zeroed, got up=%d down=%d", u.UpTotal, u.DownTotal)
	}
}

func TestRedemptionExhaustedAndExpired(t *testing.T) {
	db := newRedemptionTestDB(t)
	svc := NewRedemptionService(db)
	userID := mustRedeemUser(t, db)

	// Exhausted: max_uses=1 already consumed by another user.
	codes, err := svc.BatchGenerate(1, dto.AdminGenerateRedemptionRequest{Type: model.RedeemTypeReset, MaxUses: 1, Count: 1})
	if err != nil {
		t.Fatalf("BatchGenerate: %v", err)
	}
	other := mustRedeemUser(t, db)
	if _, _, err := svc.Redeem(other, codes[0].Code); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if _, _, err := svc.Redeem(userID, codes[0].Code); err != ErrCodeExhausted {
		t.Fatalf("expected ErrCodeExhausted, got %v", err)
	}

	// Expired.
	past := time.Now().Add(-time.Hour)
	exp, err := svc.BatchGenerate(1, dto.AdminGenerateRedemptionRequest{Type: model.RedeemTypeReset, MaxUses: 1, Count: 1, ExpiresAt: &past})
	if err != nil {
		t.Fatalf("BatchGenerate expired: %v", err)
	}
	if _, _, err := svc.Redeem(userID, exp[0].Code); err != ErrCodeExhausted {
		t.Fatalf("expected ErrCodeExhausted for expired, got %v", err)
	}

	// Invalid code.
	if _, _, err := svc.Redeem(userID, "nope"); err != ErrInvalidCode {
		t.Fatalf("expected ErrInvalidCode, got %v", err)
	}
}
