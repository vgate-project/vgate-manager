package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/wire"
)

func pkgTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Node{}, &model.Plan{}, &model.PlanPrice{},
		&model.TrafficPackage{}, &model.Order{}, &model.TrafficGrant{},
		&model.UserNodeTraffic{}, &model.TrafficHourlyStat{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestReportTrafficChargesPackagesFirst verifies that traffic is charged to an
// active grant (FIFO) before the base plan quota, and that the global
// up_total/down_total still grow by the full delta.
func TestReportTrafficChargesPackagesFirst(t *testing.T) {
	db := pkgTestDB(t)
	user := model.User{ID: "u1", Credential: "u1", Email: "u1@example.com", SubToken: "s1",
		Level: 1, QuotaBytes: 1000, TrafficQuotaBytes: 500, PackageUsedBytes: 0}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	grant := model.TrafficGrant{ID: "g1", UserID: "u1", Source: model.GrantSourceTrafficPackage,
		SourceID: "tp1", Name: "Pack 500", QuotaBytes: 500, GrantedAt: time.Now()}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewServerService(db)

	// First report: 300 bytes total (up=100, down=200). All fits in the grant.
	if err := svc.ReportTraffic("n1", []wire.UserTraffic{{Email: "u1@example.com", Up: 100, Down: 200}}); err != nil {
		t.Fatal(err)
	}
	assertGrantUsed(t, db, "g1", 300)
	assertUserCols(t, db, "u1", map[string]int64{
		"package_used_bytes": 300, "up_total": 100, "down_total": 200, "traffic_quota_bytes": 500,
	})

	// Second report: 400 bytes (up=400). Total used = 700 > capacity 500, so
	// 200 more package + 200 base.
	if err := svc.ReportTraffic("n1", []wire.UserTraffic{{Email: "u1@example.com", Up: 400, Down: 0}}); err != nil {
		t.Fatal(err)
	}
	assertGrantUsed(t, db, "g1", 500) // grant capped at its quota
	assertUserCols(t, db, "u1", map[string]int64{
		"package_used_bytes": 500, "up_total": 500, "down_total": 200, "traffic_quota_bytes": 500,
	})
	// base used = (up+down) - package_used = 700 - 500 = 200
	var u model.User
	db.Where("id = ?", "u1").First(&u)
	if got := u.BaseUsedBytes(); got != 200 {
		t.Errorf("BaseUsedBytes = %d, want 200", got)
	}
}

// TestReportTrafficNoGrantIsBaseOnly verifies a user with no grants charges
// everything to the base quota (package_used_bytes stays 0).
func TestReportTrafficNoGrantIsBaseOnly(t *testing.T) {
	db := pkgTestDB(t)
	user := model.User{ID: "u2", Credential: "u2", Email: "u2@example.com", SubToken: "s2",
		Level: 1, QuotaBytes: 1000, TrafficQuotaBytes: 0, PackageUsedBytes: 0}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewServerService(db)
	if err := svc.ReportTraffic("n1", []wire.UserTraffic{{Email: "u2@example.com", Up: 100, Down: 200}}); err != nil {
		t.Fatal(err)
	}
	assertUserCols(t, db, "u2", map[string]int64{
		"package_used_bytes": 0, "up_total": 100, "down_total": 200,
	})
}

// TestResetDueQuotasPreservesPackage verifies the monthly reset zeroes only the
// base-plan usage and keeps the package-used portion.
func TestResetDueQuotasPreservesPackage(t *testing.T) {
	db := pkgTestDB(t)
	// base used = 500, package used = 300 → up/down split 400/400.
	user := model.User{ID: "u4", Credential: "u4", Email: "u4@example.com", SubToken: "s4",
		Level: 1, QuotaBytes: 1000, TrafficQuotaBytes: 500, PackageUsedBytes: 300,
		UpTotal: 400, DownTotal: 400, QuotaResetEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewUserService(db, nil)
	if _, err := svc.ResetDueQuotas(); err != nil {
		t.Fatal(err)
	}
	// base zeroed, package (300) preserved, split ~150/150.
	assertUserCols(t, db, "u4", map[string]int64{
		"package_used_bytes": 300, "up_total": 150, "down_total": 150,
	})
}

func assertGrantUsed(t *testing.T, db *gorm.DB, id string, want int64) {
	t.Helper()
	var g model.TrafficGrant
	if err := db.Where("id = ?", id).First(&g).Error; err != nil {
		t.Fatalf("load grant %s: %v", id, err)
	}
	if g.UsedBytes != want {
		t.Errorf("grant %s used_bytes = %d, want %d", id, g.UsedBytes, want)
	}
}

func assertUserCols(t *testing.T, db *gorm.DB, id string, cols map[string]int64) {
	t.Helper()
	var u model.User
	if err := db.Where("id = ?", id).First(&u).Error; err != nil {
		t.Fatalf("load user %s: %v", id, err)
	}
	for col, want := range cols {
		var got int64
		switch col {
		case "package_used_bytes":
			got = u.PackageUsedBytes
		case "up_total":
			got = u.UpTotal
		case "down_total":
			got = u.DownTotal
		case "traffic_quota_bytes":
			got = u.TrafficQuotaBytes
		}
		if got != want {
			t.Errorf("user %s %s = %d, want %d", id, col, got, want)
		}
	}
}
