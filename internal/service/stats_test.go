package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// TestStatsNodeCountsExcludesVirtualNodes verifies that the overview's node
// counts cover real nodes only: virtual children never poll and have no data
// of their own, so they must not inflate node_count / node_online even when
// their parent is online.
func TestStatsNodeCountsExcludesVirtualNodes(t *testing.T) {
	db := vdb(t)
	ns := NewNodeService(db)
	ss := NewStatsService(db)

	real := &model.Node{
		Name: "real", Address: "real.example.com:8443", Port: 8443,
		Network: "tcp", Security: "none", Enabled: true,
	}
	if err := ns.Create(real); err != nil {
		t.Fatalf("create real: %v", err)
	}
	// Mark the real node as recently seen (online).
	if err := db.Model(&model.Node{}).Where("id = ?", real.ID).
		Update("last_seen_at", time.Now()).Error; err != nil {
		t.Fatalf("set last_seen: %v", err)
	}
	for i := 0; i < 2; i++ {
		child := &model.Node{
			Name: fmt.Sprintf("virtual%d", i), Address: fmt.Sprintf("10.0.0.%d", i+1),
			Enabled:  true,
			ParentID: &real.ID,
		}
		if err := ns.Create(child); err != nil {
			t.Fatalf("create virtual: %v", err)
		}
	}

	total, online, err := ss.nodeCounts()
	if err != nil {
		t.Fatalf("nodeCounts: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1 (virtual children excluded)", total)
	}
	if online != 1 {
		t.Errorf("online = %d, want 1 (real node only)", online)
	}

	// When the real node goes stale, the counts still track it alone.
	if err := db.Model(&model.Node{}).Where("id = ?", real.ID).
		Update("last_seen_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("stale parent: %v", err)
	}
	total, online, err = ss.nodeCounts()
	if err != nil {
		t.Fatalf("nodeCounts: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if online != 0 {
		t.Errorf("online = %d, want 0 (real node stale)", online)
	}
}

func overviewTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Node{}, &model.TrafficHourlyStat{}, &model.Order{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestGetOverviewFiltersSeriesByNode verifies the optional node filter: the
// hourly series and 24h totals narrow to one entry point (real node or virtual
// child), legacy unattributed rows (node_id = '') count toward the unfiltered
// total only, and the non-traffic metrics stay global.
func TestGetOverviewFiltersSeriesByNode(t *testing.T) {
	db := overviewTestDB(t)

	hour := time.Now().UTC().Truncate(time.Hour)
	rows := []model.TrafficHourlyStat{
		{UserID: "u1", NodeID: "n1", Hour: hour, UpTotal: 100, DownTotal: 0},
		{UserID: "u1", NodeID: "c1", Hour: hour, UpTotal: 50, DownTotal: 10},
		{UserID: "u1", NodeID: "", Hour: hour, UpTotal: 25, DownTotal: 5}, // legacy unattributed
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	svc := NewStatsService(db)

	// Unfiltered: everything sums in (100+50+25 up, 0+10+5 down).
	ov, err := svc.GetOverview("")
	if err != nil {
		t.Fatalf("GetOverview(unfiltered): %v", err)
	}
	if ov.Up24h != 175 || ov.Down24h != 15 {
		t.Errorf("unfiltered 24h = up %d / down %d, want 175 / 15", ov.Up24h, ov.Down24h)
	}

	// Real node only.
	ov, err = svc.GetOverview("n1")
	if err != nil {
		t.Fatalf("GetOverview(n1): %v", err)
	}
	if ov.Up24h != 100 || ov.Down24h != 0 {
		t.Errorf("n1 24h = up %d / down %d, want 100 / 0", ov.Up24h, ov.Down24h)
	}

	// Virtual child only.
	ov, err = svc.GetOverview("c1")
	if err != nil {
		t.Fatalf("GetOverview(c1): %v", err)
	}
	if ov.Up24h != 50 || ov.Down24h != 10 {
		t.Errorf("c1 24h = up %d / down %d, want 50 / 10", ov.Up24h, ov.Down24h)
	}
}
