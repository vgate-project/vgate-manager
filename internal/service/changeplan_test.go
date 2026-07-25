package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// changePlanTestDB migrates every model the change-plan tests touch.
func changePlanTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Plan{}, &model.PlanPrice{}, &model.Order{}, &model.TrafficGrant{},
		&model.BalanceTransaction{}, &model.SystemConfig{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// seedPlan creates an enabled plan with a single enabled price and returns its
// ids.
func seedPlan(t *testing.T, db *gorm.DB, planID, priceID string, priceCents int64, durationDays int, level int) {
	t.Helper()
	plan := model.Plan{
		ID: planID, Name: planID, Level: level, QuotaBytes: 1 << 30, Enabled: true,
		Prices: model.PlanPrices{{Period: model.PlanPeriodMonth, Price: priceCents, DurationDays: durationDays, Enabled: true}},
	}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
}

func TestComputeRemainingValue(t *testing.T) {
	now := time.Now()
	halfExpired := new(now.Add(15 * 24 * time.Hour))
	cases := []struct {
		name string
		user model.User
		want int64
	}{
		{
			name: "halfway through a 30-day 3000-cent plan",
			user: model.User{CurrentProductID: "p1", ExpireAt: halfExpired, CurrentPlanPaidCents: 3000, CurrentPlanDurationDays: 30},
			want: 1500,
		},
		{
			name: "expired plan yields zero",
			user: model.User{CurrentProductID: "p1", ExpireAt: new(now.Add(-1 * time.Hour)), CurrentPlanPaidCents: 3000, CurrentPlanDurationDays: 30},
			want: 0,
		},
		{
			name: "no current plan yields zero",
			user: model.User{CurrentProductID: "", ExpireAt: halfExpired, CurrentPlanPaidCents: 3000, CurrentPlanDurationDays: 30},
			want: 0,
		},
		{
			name: "no recorded paid amount yields zero",
			user: model.User{CurrentProductID: "p1", ExpireAt: halfExpired, CurrentPlanPaidCents: 0, CurrentPlanDurationDays: 30},
			want: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := computeRemainingValue(&c.user)
			// Allow a 1-cent tolerance for sub-second drift in the halfway case.
			if got < c.want-1 || got > c.want {
				t.Errorf("computeRemainingValue = %d, want %d (±1)", got, c.want)
			}
		})
	}
}

func TestChangePlanUpgradeImmediate(t *testing.T) {
	db := changePlanTestDB(t)
	// Current plan P1: 30-day, paid 3000 (daily 100). 10 days left.
	seedPlan(t, db, "p1", "pp1", 3000, 30, 1)
	seedPlan(t, db, "p2", "pp2", 3000, 30, 3) // P2 is pricier per day (level 3, same price but we test by daily)

	expire := time.Now().Add(10 * 24 * time.Hour)
	user := model.User{
		ID: "u1", Credential: "u1", Email: "u1@example.com", SubToken: "s1",
		EmailVerified: true, CurrentProductID: "p1", ExpireAt: &expire, CurrentPlanPaidCents: 3000, CurrentPlanDurationDays: 30,
		BalanceCents: 5000,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewOrderService(db, NewSystemConfigService(db), nil, NewBalanceService(db))
	res, err := svc.ChangePlan("u1", "p2", model.PlanPeriodMonth, "")
	if err != nil {
		t.Fatalf("ChangePlan: %v", err)
	}
	if !res.Immediate {
		t.Error("cross-plan change should be immediate")
	}
	if !res.Paid {
		t.Error("fully wallet-covered upgrade should be Paid")
	}
	// 10/30 of 3000 ≈ 1000 credited (floor; allow 1-cent sub-second drift);
	// net = 3000 - credit.
	if res.CreditCents < 999 || res.CreditCents > 1000 {
		t.Errorf("CreditCents = %d, want 999..1000", res.CreditCents)
	}
	if res.NetChargeCents != 3000-res.CreditCents {
		t.Errorf("NetChargeCents = %d, want %d", res.NetChargeCents, 3000-res.CreditCents)
	}

	// Wallet: 5000 (pre-existing) + credit (old refund) - newPrice (debited in
	// full). The refund is NOT double-counted, so this is 5000 + R - 3000, not
	// 5000 + R - net.
	var got model.User
	if err := db.First(&got, "id = ?", "u1").Error; err != nil {
		t.Fatal(err)
	}
	wantBalance := int64(5000) + res.CreditCents - int64(3000)
	if got.BalanceCents != wantBalance {
		t.Errorf("BalanceCents = %d, want %d", got.BalanceCents, wantBalance)
	}
	// New plan starts now (not extended from old expiry): ~30 days out.
	expectExpire := time.Now().Add(30 * 24 * time.Hour)
	if got.ExpireAt == nil || got.ExpireAt.Before(expectExpire.Add(-time.Hour)) || got.ExpireAt.After(expectExpire.Add(time.Hour)) {
		t.Errorf("ExpireAt = %v, want ~now+30d", got.ExpireAt)
	}
	if got.CurrentPlanPaidCents != 3000 {
		t.Errorf("CurrentPlanPaidCents = %d, want 3000 (gross new price)", got.CurrentPlanPaidCents)
	}
	if got.CurrentProductID != "p2" {
		t.Errorf("CurrentProductID = %s, want p2", got.CurrentProductID)
	}
}

// TestChangePlanDowngradeImmediateRefunds verifies that a downgrade is no
// longer deferred: the old plan's remaining value is credited to the wallet
// immediately, the net charge is clamped to zero (the new plan is cheaper), and
// the cheaper plan takes effect at once.
func TestChangePlanDowngradeImmediateRefunds(t *testing.T) {
	db := changePlanTestDB(t)
	// Current plan P1: 30-day, paid 3000 (daily 100). 10 days left.
	seedPlan(t, db, "p1", "pp1", 3000, 30, 3)
	seedPlan(t, db, "p2", "pp2", 500, 30, 1) // cheaper target → downgrade

	expire := time.Now().Add(10 * 24 * time.Hour)
	user := model.User{
		ID: "u1", Credential: "u1", Email: "u1@example.com", SubToken: "s1",
		EmailVerified: true, CurrentProductID: "p1", ExpireAt: &expire, CurrentPlanPaidCents: 3000, CurrentPlanDurationDays: 30,
		BalanceCents: 0,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewOrderService(db, NewSystemConfigService(db), nil, NewBalanceService(db))
	res, err := svc.ChangePlan("u1", "p2", model.PlanPeriodMonth, "")
	if err != nil {
		t.Fatalf("ChangePlan: %v", err)
	}
	if !res.Immediate {
		t.Error("downgrade should now be immediate")
	}
	// ~1000 credited for the 10 remaining days of the 3000-cent plan.
	if res.CreditCents < 999 || res.CreditCents > 1000 {
		t.Errorf("CreditCents = %d, want 999..1000", res.CreditCents)
	}
	// New plan (500) is cheaper than the remainder (1000), so the net is
	// negative: 500 - 1000 ≈ -500, meaning the user is refunded the difference.
	if res.NetChargeCents >= 0 {
		t.Errorf("NetChargeCents = %d, want negative (refund)", res.NetChargeCents)
	}
	if !res.Paid {
		t.Error("wallet-covering downgrade should be Paid (no gateway step)")
	}
	// Wallet: 0 (start) + credit (1000) - newPrice (500 debited in full) = 500.
	// The old buggy code left the full credit (1000) in the wallet, inflating
	// the balance on every switch.
	var got model.User
	if err := db.First(&got, "id = ?", "u1").Error; err != nil {
		t.Fatal(err)
	}
	wantBalance := res.CreditCents - int64(500)
	if got.BalanceCents != wantBalance {
		t.Errorf("BalanceCents = %d, want %d (credit - newPrice)", got.BalanceCents, wantBalance)
	}
	if got.CurrentProductID != "p2" {
		t.Errorf("CurrentProductID = %s, want p2 (applied immediately)", got.CurrentProductID)
	}
	expectExpire := time.Now().Add(30 * 24 * time.Hour)
	if got.ExpireAt == nil || got.ExpireAt.Before(expectExpire.Add(-time.Hour)) || got.ExpireAt.After(expectExpire.Add(time.Hour)) {
		t.Errorf("ExpireAt = %v, want ~now+30d", got.ExpireAt)
	}
}

// TestChangePlanSwitchNoBalanceInflation is a direct regression for the bug
// where switching plans credited the old plan's remaining value to the wallet
// without charging the new plan, so the balance grew on every switch. Here the
// wallet must end at exactly (refund - newPrice), never at the full refund.
func TestChangePlanSwitchNoBalanceInflation(t *testing.T) {
	db := changePlanTestDB(t)
	// Current plan P1: 30-day, paid 3000. 10 days left → ~1000 remaining.
	seedPlan(t, db, "p1", "pp1", 3000, 30, 3)
	seedPlan(t, db, "p2", "pp2", 500, 30, 1) // cheaper target → downgrade

	expire := time.Now().Add(10 * 24 * time.Hour)
	user := model.User{
		ID: "u1", Credential: "u1", Email: "u1@example.com", SubToken: "s1",
		EmailVerified: true, CurrentProductID: "p1", ExpireAt: &expire, CurrentPlanPaidCents: 3000, CurrentPlanDurationDays: 30,
		BalanceCents: 0,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewOrderService(db, NewSystemConfigService(db), nil, NewBalanceService(db))
	res, err := svc.ChangePlan("u1", "p2", model.PlanPeriodMonth, "")
	if err != nil {
		t.Fatalf("ChangePlan: %v", err)
	}
	if !res.Paid {
		t.Fatal("downgrade should be fully wallet-covered (Paid)")
	}

	var got model.User
	if err := db.First(&got, "id = ?", "u1").Error; err != nil {
		t.Fatal(err)
	}
	wantBalance := res.CreditCents - int64(500)
	if got.BalanceCents != wantBalance {
		t.Errorf("BalanceCents = %d, want %d (credit - newPrice); balance must not inflate", got.BalanceCents, wantBalance)
	}
	// Economic sanity: balance + value of new plan == old remaining value.
	if got.BalanceCents+int64(500) != res.CreditCents {
		t.Errorf("total value after switch = %d, want credited remainder %d", got.BalanceCents+int64(500), res.CreditCents)
	}
}

// TestPreviewChangePlan verifies that PreviewChangePlan returns the proration
// numbers without mutating any state (no order, no wallet change, no plan
// switch).
func TestPreviewChangePlan(t *testing.T) {
	db := changePlanTestDB(t)
	// Current plan P1: 30-day, paid 3000 (daily 100). 10 days left → ~1000 remaining.
	seedPlan(t, db, "p1", "pp1", 3000, 30, 1)
	seedPlan(t, db, "p2", "pp2", 3000, 30, 3) // same price target

	expire := time.Now().Add(10 * 24 * time.Hour)
	user := model.User{
		ID: "u1", Credential: "u1", Email: "u1@example.com", SubToken: "s1",
		EmailVerified: true, CurrentProductID: "p1", ExpireAt: &expire, CurrentPlanPaidCents: 3000, CurrentPlanDurationDays: 30,
		BalanceCents: 5000,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewOrderService(db, NewSystemConfigService(db), nil, NewBalanceService(db))
	res, err := svc.PreviewChangePlan("u1", "p2", model.PlanPeriodMonth)
	if err != nil {
		t.Fatalf("PreviewChangePlan: %v", err)
	}
	if !res.Immediate {
		t.Error("preview should report immediate")
	}
	if res.CreditCents < 999 || res.CreditCents > 1000 {
		t.Errorf("CreditCents = %d, want 999..1000", res.CreditCents)
	}
	// net = price (3000) - credit (~1000) ≈ 2000, no clamp for a positive net.
	if res.NetChargeCents != 3000-res.CreditCents {
		t.Errorf("NetChargeCents = %d, want %d", res.NetChargeCents, 3000-res.CreditCents)
	}

	// Nothing should have been written: no orders, wallet unchanged, plan intact.
	var orderCount int64
	if err := db.Model(&model.Order{}).Count(&orderCount).Error; err != nil {
		t.Fatal(err)
	}
	if orderCount != 0 {
		t.Errorf("preview created %d orders, want 0", orderCount)
	}
	var got model.User
	if err := db.First(&got, "id = ?", "u1").Error; err != nil {
		t.Fatal(err)
	}
	if got.BalanceCents != 5000 {
		t.Errorf("preview mutated wallet: BalanceCents = %d, want 5000", got.BalanceCents)
	}
	if got.CurrentProductID != "p1" {
		t.Errorf("preview changed plan: CurrentProductID = %s, want p1", got.CurrentProductID)
	}
}

func TestCreateDeductsWalletFully(t *testing.T) {
	db := changePlanTestDB(t)
	seedPlan(t, db, "p1", "pp1", 500, 30, 1)
	user := model.User{
		ID: "u1", Credential: "u1", Email: "u1@example.com", SubToken: "s1",
		EmailVerified: true, BalanceCents: 500,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewOrderService(db, NewSystemConfigService(db), nil, NewBalanceService(db))
	order, _, err := svc.Create("u1", CreateOrderParams{Kind: model.OrderKindPlan, PlanID: "p1", PlanPriceID: "month"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if order.Status != model.OrderStatusPaid {
		t.Errorf("order status = %s, want paid (wallet covered)", order.Status)
	}
	var got model.User
	db.First(&got, "id = ?", "u1")
	if got.BalanceCents != 0 {
		t.Errorf("BalanceCents = %d, want 0", got.BalanceCents)
	}
	if got.CurrentProductID != "p1" {
		t.Errorf("plan not applied; CurrentProductID=%s, want p1", got.CurrentProductID)
	}
}
