package service

import (
	"testing"

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
		&model.SystemConfig{}, &model.Plan{}, &model.PlanPrice{},
		&model.TrafficPackage{}, &model.Order{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newOrderService(t *testing.T, db *gorm.DB) *OrderService {
	t.Helper()
	sys := NewSystemConfigService(db)
	return NewOrderService(db, sys, nil)
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

	reset, err := svc.resolvePaymentSubject(model.OrderKindReset, "VIP", "", 300, "")
	if err != nil {
		t.Fatal(err)
	}
	if reset != "VIP traffic reset" {
		t.Errorf("reset subject = %q, want %q", reset, "VIP traffic reset")
	}
}
