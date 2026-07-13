package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/wire"
	"gorm.io/gorm"
)

func trafficTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.TrafficHourlyStat{}, &model.UserNodeTraffic{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestHourlyForUserReadsDeltas verifies HourlyForUser returns the per-hour delta
// stored for the user directly, with no telescoping. traffic_hourly_stat rows
// are written as per-user, per-hour deltas by ReportTraffic, so each row IS that
// hour's traffic. A reset that zeroes the lifetime cumulative counter must NOT
// produce a negative bar here (the old bug), because we never subtract.
func TestHourlyForUserReadsDeltas(t *testing.T) {
	db := trafficTestDB(t)
	userID := "u1"

	hourNow := time.Now().UTC().Truncate(time.Hour)
	cutoff := hourNow.Add(-24 * time.Hour)

	// Per-hour DELTA rows (not cumulative snapshots).
	rows := []model.TrafficHourlyStat{
		{UserID: userID, Hour: cutoff, UpTotal: 500, DownTotal: 1000},
		{UserID: userID, Hour: cutoff.Add(time.Hour), UpTotal: 200, DownTotal: 600},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	svc := NewTrafficService(db)
	series, err := svc.HourlyForUser(userID)
	if err != nil {
		t.Fatalf("HourlyForUser: %v", err)
	}

	var first, second *dto.HourlyStat
	for i := range series {
		if series[i].Hour.Equal(cutoff) {
			first = &series[i]
		}
		if series[i].Hour.Equal(cutoff.Add(time.Hour)) {
			second = &series[i]
		}
	}
	if first == nil {
		t.Fatalf("expected a series point at cutoff %v", cutoff)
	}
	if first.Up != 500 || first.Down != 1000 {
		t.Errorf("cutoff hour = (up=%d, down=%d), want (up=500, down=1000)", first.Up, first.Down)
	}
	if second == nil {
		t.Fatalf("expected a series point at cutoff+1h")
	}
	if second.Up != 200 || second.Down != 600 {
		t.Errorf("cutoff+1h = (up=%d, down=%d), want (up=200, down=600)", second.Up, second.Down)
	}
}

// TestReportTrafficWritesHourlyStat verifies ReportTraffic additively upserts a
// per-user hourly delta into traffic_hourly_stat for the current hour bucket,
// and that the hourly row stores the REAL (un-multiplied) reported bytes while
// the cumulative user total is the multiplied (billing) figure.
func TestReportTrafficWritesHourlyStat(t *testing.T) {
	db := trafficTestDB(t)

	// Node with a 2x traffic multiplier: cumulative totals must be doubled,
	// but the hourly series must store the raw reported bytes.
	node := model.Node{ID: "node1", TrafficMultiplier: 2}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}

	user := model.User{ID: "u1", Email: "a@example.com", Enabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	svc := NewServerService(db)
	deltas := []wire.UserTraffic{{Email: "a@example.com", Up: 100, Down: 200}}
	if err := svc.ReportTraffic("node1", deltas); err != nil {
		t.Fatalf("ReportTraffic: %v", err)
	}

	hour := time.Now().UTC().Truncate(time.Hour)

	// Hourly row must hold the UN-MULTIPLIED real bytes.
	var stat model.TrafficHourlyStat
	if err := db.Where("user_id = ? AND hour = ?", "u1", hour).First(&stat).Error; err != nil {
		t.Fatalf("find stat: %v", err)
	}
	if stat.UpTotal != 100 || stat.DownTotal != 200 {
		t.Errorf("stat = (up=%d, down=%d), want real (up=100, down=200)", stat.UpTotal, stat.DownTotal)
	}

	// Cumulative user total must be MULTIPLIED (2x, billing intent preserved).
	var u model.User
	if err := db.First(&u, "id = ?", "u1").Error; err != nil {
		t.Fatalf("find user: %v", err)
	}
	if u.UpTotal != 200 || u.DownTotal != 400 {
		t.Errorf("user cumulative = (up=%d, down=%d), want multiplied (up=200, down=400)", u.UpTotal, u.DownTotal)
	}

	// A second report with the same delta must accumulate (additive upsert),
	// proving concurrent reports from multiple nodes are safe. Hourly stays
	// real (100+100), cumulative stays multiplied (200+200).
	if err := svc.ReportTraffic("node1", deltas); err != nil {
		t.Fatalf("ReportTraffic #2: %v", err)
	}
	if err := db.Where("user_id = ? AND hour = ?", "u1", hour).First(&stat).Error; err != nil {
		t.Fatalf("find stat #2: %v", err)
	}
	if stat.UpTotal != 200 || stat.DownTotal != 400 {
		t.Errorf("after second report stat = (up=%d, down=%d), want real (up=200, down=400)", stat.UpTotal, stat.DownTotal)
	}
	if err := db.First(&u, "id = ?", "u1").Error; err != nil {
		t.Fatalf("find user #2: %v", err)
	}
	if u.UpTotal != 400 || u.DownTotal != 800 {
		t.Errorf("after second report user cumulative = (up=%d, down=%d), want multiplied (up=400, down=800)", u.UpTotal, u.DownTotal)
	}
}
