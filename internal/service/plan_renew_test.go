package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

func renewTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Plan{}, &model.PlanPrice{}, &model.Order{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestListAppendsOffShelfRenewablePlan(t *testing.T) {
	db := renewTestDB(t)
	now := time.Now()
	expire := now.Add(10 * 24 * time.Hour)

	// User owns an off-shelf plan that allows renewal.
	user := model.User{
		ID:                "u1",
		Credential:        "u1",
		Email:             "u1@example.com",
		SubToken:          "sub-u1",
		CurrentProductID:   "p1",
		CurrentProductKind: model.OrderKindPlan,
		ExpireAt:          &expire,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	plan := model.Plan{
		ID:                 "p1",
		Name:               "Legacy",
		Enabled:            false,
		AllowRenewOffShelf: true,
		Prices: []model.PlanPrice{
			{ID: "pp1", Period: model.PlanPeriodMonth, Price: 990, DurationDays: 30, Enabled: true},
		},
	}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	// An unrelated enabled plan that should also appear.
	onShelf := model.Plan{
		ID:      "p2",
		Name:    "Current",
		Enabled: true,
		Prices: []model.PlanPrice{
			{ID: "pp2", Period: model.PlanPeriodMonth, Price: 1990, DurationDays: 30, Enabled: true},
		},
	}
	if err := db.Create(&onShelf).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewPlanService(db)
	plans, err := svc.List(true, "u1")
	if err != nil {
		t.Fatal(err)
	}

	var foundOffShelf bool
	for _, p := range plans {
		t.Logf("plan id=%s enabled=%v allowRenew=%v prices=%d", p.ID, p.Enabled, p.AllowRenewOffShelf, len(p.Prices))
		if p.ID == "p1" {
			foundOffShelf = true
			if len(p.Prices) == 0 {
				t.Fatalf("off-shelf plan returned with no prices")
			}
		}
	}
	if !foundOffShelf {
		t.Fatalf("off-shelf renewable plan was NOT returned for its owner")
	}
}

func TestListHidesOffShelfPlanWhenRenewNotAllowed(t *testing.T) {
	db := renewTestDB(t)
	now := time.Now()
	expire := now.Add(10 * 24 * time.Hour)
	user := model.User{
		ID:                "u1",
		Credential:        "u1",
		Email:             "u1@example.com",
		SubToken:          "sub-u1",
		CurrentProductID:   "p1",
		CurrentProductKind: model.OrderKindPlan,
		ExpireAt:          &expire,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	plan := model.Plan{
		ID:                 "p1",
		Name:               "Legacy",
		Enabled:            false,
		AllowRenewOffShelf: false, // not allowed
		Prices: []model.PlanPrice{
			{ID: "pp1", Period: model.PlanPeriodMonth, Price: 990, DurationDays: 30, Enabled: true},
		},
	}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewPlanService(db)
	plans, err := svc.List(true, "u1")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.ID == "p1" {
			t.Fatalf("off-shelf plan returned even though renewal is not allowed")
		}
	}
}
