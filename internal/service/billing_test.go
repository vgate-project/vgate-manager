package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Plan{}, &model.PlanPrice{},
		&model.TrafficPackage{}, &model.Order{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestApplyPlanEffectSetsLevel(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	expire := now.Add(10 * 24 * time.Hour)
	user := model.User{ID: "u1", Credential: "u1", Email: "u1@example.com", SubToken: "sub-u1", Level: 1, ExpireAt: &expire, QuotaBytes: 0}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	plan := model.Plan{ID: "p1", Level: 5, QuotaBytes: 100}

	err := db.Transaction(func(tx *gorm.DB) error {
		var u model.User
		if err := tx.Where("id = ?", "u1").First(&u).Error; err != nil {
			return err
		}
		return applyPlanEffect(tx, &u, &plan, 30)
	})
	if err != nil {
		t.Fatal(err)
	}

	var got model.User
	db.Where("id = ?", "u1").First(&got)
	if got.Level != 5 {
		t.Errorf("Level = %d, want 5 (subscription must set plan level)", got.Level)
	}
	if got.QuotaBytes != 100 {
		t.Errorf("QuotaBytes = %d, want 100", got.QuotaBytes)
	}
	if got.ExpireAt == nil || !got.ExpireAt.After(expire.Add(29*24*time.Hour)) {
		t.Errorf("ExpireAt not extended by ~30 days: %v", got.ExpireAt)
	}
}

func TestApplyPlanEffectReplacesNotAdds(t *testing.T) {
	db := testDB(t)
	now := time.Now() // User already has a 100-byte quota and has consumed 30 bytes
	// (10 up + 20 down). Buying a 200-byte plan must REPLACE the quota with
	// 200 (not 300), and must NOT touch the already-consumed traffic.
	user := model.User{
		ID: "u1", Credential: "u1", Email: "u1@example.com", SubToken: "sub-u1", Level: 1,
		ExpireAt: new(now.Add(10 * 24 * time.Hour)), QuotaBytes: 100, UpTotal: 10, DownTotal: 20,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	plan := model.Plan{ID: "p1", Level: 5, QuotaBytes: 200}

	err := db.Transaction(func(tx *gorm.DB) error {
		var u model.User
		if err := tx.Where("id = ?", "u1").First(&u).Error; err != nil {
			return err
		}
		return applyPlanEffect(tx, &u, &plan, 30)
	})
	if err != nil {
		t.Fatal(err)
	}

	var got model.User
	db.Where("id = ?", "u1").First(&got)
	if got.QuotaBytes != 200 {
		t.Errorf("QuotaBytes = %d, want 200 (plan must set, not add to, prior quota)", got.QuotaBytes)
	}
	if got.UpTotal != 0 || got.DownTotal != 0 {
		t.Errorf("used traffic not reset: up=%d down=%d, want 0/0 (new plan must start with full quota)", got.UpTotal, got.DownTotal)
	}
	if got.LastResetAt == nil {
		t.Errorf("LastResetAt not stamped on plan purchase")
	}
	// Remaining = quota - used = 200 - 0 = 200 (fresh full quota).
	if got.QuotaBytes-(got.UpTotal+got.DownTotal) != 200 {
		t.Errorf("remaining = %d, want 200", got.QuotaBytes-(got.UpTotal+got.DownTotal))
	}
}

func TestApplyTrafficEffectValidity(t *testing.T) {
	db := testDB(t)

	// Case 1: validity_days > 0 extends ExpireAt by that window.
	now := time.Now()
	expire := now.Add(5 * 24 * time.Hour)
	u1 := model.User{ID: "u1", Credential: "u1", Email: "u1@example.com", SubToken: "sub-u1", Level: 2, ExpireAt: &expire, QuotaBytes: 0}
	db.Create(&u1)
	pkg := model.TrafficPackage{ID: "tp1", QuotaBytes: 500, ValidityDays: 7}

	err := db.Transaction(func(tx *gorm.DB) error {
		var u model.User
		tx.Where("id = ?", "u1").First(&u)
		return applyTrafficEffect(tx, &u, &pkg, 7)
	})
	if err != nil {
		t.Fatal(err)
	}
	var got1 model.User
	db.Where("id = ?", "u1").First(&got1)
	if got1.QuotaBytes != 500 {
		t.Errorf("QuotaBytes = %d, want 500", got1.QuotaBytes)
	}
	if got1.Level != 2 {
		t.Errorf("Level changed for traffic purchase: %d, want 2", got1.Level)
	}
	if got1.ExpireAt == nil || !got1.ExpireAt.After(expire.Add(6*24*time.Hour)) {
		t.Errorf("ExpireAt not extended by ~7 days: %v", got1.ExpireAt)
	}

	// Case 2: validity_days == 0 adds quota but does NOT change ExpireAt.
	u2 := model.User{ID: "u2", Credential: "u2", Email: "u2@example.com", SubToken: "sub-u2", Level: 3, QuotaBytes: 0}
	db.Create(&u2)
	err = db.Transaction(func(tx *gorm.DB) error {
		var u model.User
		tx.Where("id = ?", "u2").First(&u)
		return applyTrafficEffect(tx, &u, &pkg, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	var got2 model.User
	db.Where("id = ?", "u2").First(&got2)
	if got2.ExpireAt != nil {
		t.Errorf("ExpireAt should be nil when validity=0, got %v", got2.ExpireAt)
	}
	if got2.QuotaBytes != 500 {
		t.Errorf("QuotaBytes = %d, want 500", got2.QuotaBytes)
	}

	// Case 3: buying a traffic package opts the user OUT of the global monthly
	// reset (quota_reset_enabled = false), so the one-time package quota is
	// never refreshed by ResetDueQuotas.
	u3 := model.User{ID: "u3", Credential: "u3", Email: "u3@example.com", SubToken: "sub-u3", Level: 3,
		QuotaBytes: 0, QuotaResetEnabled: true, UpTotal: 10, DownTotal: 20}
	db.Create(&u3)
	err = db.Transaction(func(tx *gorm.DB) error {
		var u model.User
		tx.Where("id = ?", "u3").First(&u)
		return applyTrafficEffect(tx, &u, &pkg, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	var got3 model.User
	db.Where("id = ?", "u3").First(&got3)
	if got3.QuotaResetEnabled {
		t.Errorf("QuotaResetEnabled = %v, want false (traffic purchase must opt out of monthly reset)", got3.QuotaResetEnabled)
	}
	if got3.QuotaBytes != 500 {
		t.Errorf("QuotaBytes = %d, want 500", got3.QuotaBytes)
	}
	// The already-consumed traffic must be preserved (only the reset flag, not
	// the usage, is what we care about here).
	if got3.UpTotal != 10 || got3.DownTotal != 20 {
		t.Errorf("used traffic changed: up=%d down=%d, want 10/20", got3.UpTotal, got3.DownTotal)
	}
	// ResetDueQuotas must now skip this user (opted out).
	n, err := NewUserService(db, nil).ResetDueQuotas()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("ResetDueQuotas reset %d users, want 0 (package buyer must not reset)", n)
	}
	var after model.User
	db.Where("id = ?", "u3").First(&after)
	if after.UpTotal != 10 || after.DownTotal != 20 {
		t.Errorf("after reset up=%d down=%d, want 10/20 (traffic must persist)", after.UpTotal, after.DownTotal)
	}
}

func TestResetDueQuotasRespectsFlag(t *testing.T) {
	db := testDB(t)

	// enabled + finite quota -> reset.
	on := model.User{ID: "on", Credential: "on", Email: "on@example.com", SubToken: "s-on", QuotaBytes: 100, QuotaResetEnabled: true, UpTotal: 30, DownTotal: 40}
	// enabled=false -> skipped even with finite quota.
	off := model.User{ID: "off", Credential: "off", Email: "off@example.com", SubToken: "s-off", QuotaBytes: 100, QuotaResetEnabled: false, UpTotal: 30, DownTotal: 40}
	// unlimited quota (-1) -> skipped.
	unlimited := model.User{ID: "ul", Credential: "ul", Email: "ul@example.com", SubToken: "s-ul", QuotaBytes: -1, QuotaResetEnabled: true, UpTotal: 30, DownTotal: 40}
	db.Create(&on)
	db.Create(&off)
	db.Create(&unlimited)

	n, err := NewUserService(db, nil).ResetDueQuotas()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("ResetDueQuotas reset %d users, want 1 (only enabled+finite)", n)
	}

	var gotOn, gotOff, gotUl model.User
	db.Where("id = ?", "on").First(&gotOn)
	db.Where("id = ?", "off").First(&gotOff)
	db.Where("id = ?", "ul").First(&gotUl)

	if gotOn.UpTotal != 0 || gotOn.DownTotal != 0 {
		t.Errorf("enabled user not reset: up=%d down=%d, want 0/0", gotOn.UpTotal, gotOn.DownTotal)
	}
	if gotOn.LastResetAt == nil {
		t.Errorf("LastResetAt not stamped on reset")
	}
	if gotOff.UpTotal != 30 || gotOff.DownTotal != 40 {
		t.Errorf("opted-out user wrongly reset: up=%d down=%d, want 30/40", gotOff.UpTotal, gotOff.DownTotal)
	}
	if gotUl.UpTotal != 30 || gotUl.DownTotal != 40 {
		t.Errorf("unlimited user wrongly reset: up=%d down=%d, want 30/40", gotUl.UpTotal, gotUl.DownTotal)
	}
}

func TestPlanServiceNestedPrices(t *testing.T) {
	db := testDB(t)
	svc := NewPlanService(db)

	plan := &model.Plan{
		Name:       "Pro",
		Level:      3,
		QuotaBytes: 1000,
		Enabled:    true,
		Prices: []model.PlanPrice{
			{Period: model.PlanPeriodMonth, Price: 9900, DurationDays: 30, Enabled: true},
			{Period: model.PlanPeriodYear, Price: 99000, DurationDays: 365, Enabled: true},
		},
	}
	if err := svc.Create(plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Prices) != 2 {
		t.Fatalf("expected 2 created prices, got %d", len(plan.Prices))
	}

	// Reload and verify prices persisted with FK.
	reloaded, err := svc.Get(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Prices) != 2 {
		t.Fatalf("reloaded prices = %d, want 2", len(reloaded.Prices))
	}

	// Update: drop the monthly price, keep yearly, add quarterly.
	reloaded.Name = "Pro v2"
	reloaded.Prices = []model.PlanPrice{
		{Period: model.PlanPeriodYear, Price: 99000, DurationDays: 365, Enabled: true},
		{Period: model.PlanPeriodQuarter, Price: 27000, DurationDays: 90, Enabled: true},
	}
	if err := svc.Update(reloaded); err != nil {
		t.Fatal(err)
	}
	again, _ := svc.Get(plan.ID)
	if len(again.Prices) != 2 {
		t.Fatalf("after update prices = %d, want 2", len(again.Prices))
	}
}

// TestPlanServicePersistsDisabledPrice guards against the GORM pitfall where a
// `default:true` tag causes false (the zero value) to be omitted from INSERT,
// coercing disabled prices back to enabled in the database.
func TestPlanServicePersistsDisabledPrice(t *testing.T) {
	db := testDB(t)
	svc := NewPlanService(db)

	plan := &model.Plan{
		Name:    "DisableMe",
		Enabled: true,
		Prices: []model.PlanPrice{
			{Period: model.PlanPeriodMonth, Price: 9900, DurationDays: 30, Enabled: true},
			{Period: model.PlanPeriodYear, Price: 99000, DurationDays: 365, Enabled: false},
		},
	}
	if err := svc.Create(plan); err != nil {
		t.Fatal(err)
	}

	reloaded, err := svc.Get(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Prices) != 2 {
		t.Fatalf("reloaded prices = %d, want 2", len(reloaded.Prices))
	}
	for _, p := range reloaded.Prices {
		if p.Period == model.PlanPeriodYear && p.Enabled {
			t.Errorf("yearly price should be disabled (Enabled=false) but was stored as enabled")
		}
	}

	// And via Update: re-create the same plan with the yearly price disabled.
	reloaded.Prices = []model.PlanPrice{
		{Period: model.PlanPeriodMonth, Price: 9900, DurationDays: 30, Enabled: true},
		{Period: model.PlanPeriodYear, Price: 99000, DurationDays: 365, Enabled: false},
	}
	if err := svc.Update(reloaded); err != nil {
		t.Fatal(err)
	}
	again, _ := svc.Get(plan.ID)
	for _, p := range again.Prices {
		if p.Period == model.PlanPeriodYear && p.Enabled {
			t.Errorf("after update, yearly price should still be disabled but was stored as enabled")
		}
	}
}
