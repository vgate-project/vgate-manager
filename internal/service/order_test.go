package service

import (
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

func orderTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.SystemConfig{}, &model.User{}, &model.Plan{}, &model.PlanPrice{},
		&model.TrafficPackage{}, &model.Order{}, &model.TrafficGrant{},
		&model.BalanceTransaction{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newOrderService(t *testing.T, db *gorm.DB) *OrderService {
	t.Helper()
	sys := NewSystemConfigService(db)
	return NewOrderService(db, sys, nil, NewBalanceService(db))
}

func TestRenderProductTemplate(t *testing.T) {
	got := renderProductTemplate("{plan} {period} - {amount}", "VIP", "month", 990)
	if got != "VIP month - 9.90" {
		t.Errorf("renderProductTemplate = %q, want %q", got, "VIP month - 9.90")
	}
	// Missing tokens are left untouched; empty period yields a double space.
	got = renderProductTemplate("{plan} {period}", "Add-on", "", 0)
	if got != "Add-on " {
		t.Errorf("renderProductTemplate = %q, want %q", got, "Add-on ")
	}
}

func TestResolvePaymentSubjectDisplayNameWins(t *testing.T) {
	svc := newOrderService(t, orderTestDB(t))
	// A per-product DisplayName takes precedence over the global template.
	if err := svc.sys.SetAll(map[string]string{CfgKeyPaymentProductName: "{plan} {period}"}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.resolvePaymentSubject(model.OrderKindPlan, "VIP", "month", 990, "My Custom Name")
	if err != nil {
		t.Fatal(err)
	}
	if got != "My Custom Name" {
		t.Errorf("subject = %q, want %q", got, "My Custom Name")
	}
}

func TestResolvePaymentSubjectTemplateFallback(t *testing.T) {
	db := orderTestDB(t)
	svc := newOrderService(t, db)
	if err := svc.sys.SetAll(map[string]string{CfgKeyPaymentProductName: "{plan}-{period}-{amount}"}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.resolvePaymentSubject(model.OrderKindPlan, "VIP", "month", 990, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "VIP-month-9.90" {
		t.Errorf("subject = %q, want %q", got, "VIP-month-9.90")
	}
}

func TestResolvePaymentSubjectBuiltInDefault(t *testing.T) {
	svc := newOrderService(t, orderTestDB(t)) // no template seeded

	plan, err := svc.resolvePaymentSubject(model.OrderKindPlan, "VIP", "month", 990, "")
	if err != nil {
		t.Fatal(err)
	}
	if plan != "month plan" {
		t.Errorf("plan subject = %q, want %q", plan, "month plan")
	}

	traffic, err := svc.resolvePaymentSubject(model.OrderKindTraffic, "100GB Add-on", "", 500, "")
	if err != nil {
		t.Fatal(err)
	}
	if traffic != "100GB Add-on" {
		t.Errorf("traffic subject = %q, want %q", traffic, "100GB Add-on")
	}
}

func seedTrafficPackage(t *testing.T, db *gorm.DB, id string, priceCents, quotaBytes int64) {
	t.Helper()
	pkg := model.TrafficPackage{
		ID: id, Name: id, Price: priceCents, QuotaBytes: quotaBytes,
		Enabled: true,
	}
	if err := db.Create(&pkg).Error; err != nil {
		t.Fatal(err)
	}
}

// TestCreateTrafficRequiresActivePlan verifies that a traffic package cannot be
// bought standalone: a user with no current plan is rejected before any wallet
// or payment logic runs.
func TestCreateTrafficRequiresActivePlan(t *testing.T) {
	db := orderTestDB(t)
	seedPlan(t, db, "p1", "pp1", 500, 30, 1)
	seedTrafficPackage(t, db, "tp1", 1000, 100<<30)
	user := model.User{
		ID: "u1", Credential: "u1", Email: "u1@example.com", SubToken: "s1",
		EmailVerified: true, BalanceCents: 1000,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	svc := newOrderService(t, db)
	_, _, err := svc.Create("u1", CreateOrderParams{Kind: model.OrderKindTraffic, TrafficPackageID: "tp1"})
	if !errors.Is(err, ErrNoActivePlan) {
		t.Fatalf("Create traffic without plan: err=%v, want ErrNoActivePlan", err)
	}
}

// TestCreateTrafficAsPlanBonus verifies that a traffic package bought on top of
// an active plan is treated as an add-on: it adds to TrafficQuotaBytes and
// creates a TrafficGrant, but never overwrites the plan as the current product,
// never disables quota reset, and never extends ExpireAt.
func TestCreateTrafficAsPlanBonus(t *testing.T) {
	db := orderTestDB(t)
	seedPlan(t, db, "p1", "pp1", 500, 30, 1)
	seedTrafficPackage(t, db, "tp1", 1000, 100<<30)
	expire := time.Now().Add(10 * 24 * time.Hour)
	user := model.User{
		ID: "u1", Credential: "u1", Email: "u1@example.com", SubToken: "s1",
		EmailVerified: true, CurrentProductID: "p1",
		ExpireAt: &expire, QuotaResetEnabled: true, BalanceCents: 1000, // wallet fully covers
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	svc := newOrderService(t, db)
	order, _, err := svc.Create("u1", CreateOrderParams{Kind: model.OrderKindTraffic, TrafficPackageID: "tp1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if order.Status != model.OrderStatusPaid {
		t.Errorf("order status = %s, want paid (wallet covered)", order.Status)
	}
	var got model.User
	if err := db.First(&got, "id = ?", "u1").Error; err != nil {
		t.Fatal(err)
	}
	if got.CurrentProductID != "p1" {
		t.Errorf("CurrentProductID = %s, want p1 (plan must remain the current product)", got.CurrentProductID)
	}
	if got.TrafficQuotaBytes != 100<<30 {
		t.Errorf("TrafficQuotaBytes = %d, want %d (bonus added)", got.TrafficQuotaBytes, int64(100<<30))
	}
	if !got.QuotaResetEnabled {
		t.Error("QuotaResetEnabled = false, want true (must not be disabled by a traffic purchase)")
	}
	if got.ExpireAt == nil || got.ExpireAt.Sub(expire).Abs() > time.Minute {
		t.Errorf("ExpireAt changed unexpectedly: %v, want ~%v", got.ExpireAt, expire)
	}
	if got.ExpireAt.After(expire.Add(29 * 24 * time.Hour)) {
		t.Errorf("ExpireAt was extended by the package validity; old ExpireAt=%v new=%v", expire, got.ExpireAt)
	}
	var grants []model.TrafficGrant
	if err := db.Where("user_id = ? AND source = ?", "u1", model.GrantSourceTrafficPackage).Find(&grants).Error; err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 {
		t.Fatalf("TrafficGrant count = %d, want 1", len(grants))
	}
	if grants[0].QuotaBytes != 100<<30 {
		t.Errorf("grant QuotaBytes = %d, want %d", grants[0].QuotaBytes, int64(100<<30))
	}
}
