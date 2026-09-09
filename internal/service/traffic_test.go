package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/wire"
)

func trafficTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Node{}, &model.TrafficHourlyStat{}, &model.UserNodeTraffic{}, &model.TrafficGrant{}); err != nil {
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
// and that the hourly row stores the REAL (un-multiplied) reported bytes plus
// the applied multiplier, while the cumulative user total is the multiplied
// (billing) figure.
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

	// Hourly row must hold the UN-MULTIPLIED real bytes, booked to the node,
	// with the applied multiplier captured alongside.
	var stat model.TrafficHourlyStat
	if err := db.Where("user_id = ? AND node_id = ? AND hour = ?", "u1", "node1", hour).First(&stat).Error; err != nil {
		t.Fatalf("find stat: %v", err)
	}
	if stat.NodeID != "node1" {
		t.Errorf("stat node_id = %q, want node1", stat.NodeID)
	}
	if stat.UpTotal != 100 || stat.DownTotal != 200 {
		t.Errorf("stat = (up=%d, down=%d), want real (up=100, down=200)", stat.UpTotal, stat.DownTotal)
	}
	if stat.Multiplier != 2 {
		t.Errorf("stat multiplier = %v, want 2", stat.Multiplier)
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
	if err := db.Where("user_id = ? AND node_id = ? AND hour = ?", "u1", "node1", hour).First(&stat).Error; err != nil {
		t.Fatalf("find stat #2: %v", err)
	}
	if stat.UpTotal != 200 || stat.DownTotal != 400 {
		t.Errorf("after second report stat = (up=%d, down=%d), want real (up=200, down=400)", stat.UpTotal, stat.DownTotal)
	}
	if stat.Multiplier != 2 {
		t.Errorf("after second report stat multiplier = %v, want 2", stat.Multiplier)
	}
	if err := db.First(&u, "id = ?", "u1").Error; err != nil {
		t.Fatalf("find user #2: %v", err)
	}
	if u.UpTotal != 400 || u.DownTotal != 800 {
		t.Errorf("after second report user cumulative = (up=%d, down=%d), want multiplied (up=400, down=800)", u.UpTotal, u.DownTotal)
	}
}

// TestListReturnsHourlyDetailRows verifies the admin traffic list serves the
// actual per-user-per-node hourly detail rows — raw bytes, the multiplier
// captured at write time, and billed bytes derived as raw × multiplier —
// honoring the user/node/hour-range filters and skipping legacy
// unattributed rows (node_id = ”).
func TestListReturnsHourlyDetailRows(t *testing.T) {
	db := trafficTestDB(t)

	for _, u := range []model.User{
		{ID: "u1", Credential: "cred-a", SubToken: "sub-a", Email: "a@example.com", Enabled: true},
		{ID: "u2", Credential: "cred-b", SubToken: "sub-b", Email: "b@example.com", Enabled: true},
	} {
		if err := db.Create(&u).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	for _, n := range []model.Node{
		{ID: "n1", Name: "Node One", Token: "tok-1"},
		{ID: "n2", Name: "Node Two", Token: "tok-2", TrafficMultiplier: 2},
	} {
		if err := db.Create(&n).Error; err != nil {
			t.Fatalf("create node: %v", err)
		}
	}

	base := time.Now().UTC().Truncate(time.Hour)
	h10, h11, h12 := base.Add(-2*time.Hour), base.Add(-time.Hour), base
	rows := []model.TrafficHourlyStat{
		{UserID: "u1", NodeID: "n1", Hour: h10, UpTotal: 100, DownTotal: 200, Multiplier: 1},
		{UserID: "u1", NodeID: "n2", Hour: h11, UpTotal: 100, DownTotal: 200, Multiplier: 2},
		{UserID: "u2", NodeID: "n1", Hour: h11, UpTotal: 50, DownTotal: 0, Multiplier: 1},
		// Legacy unattributed row: must never appear in the detail list.
		{UserID: "u1", NodeID: "", Hour: h12, UpTotal: 999, DownTotal: 999, Multiplier: 1},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	svc := NewTrafficService(db)

	// Unfiltered: newest hour first, email ASC within the same hour.
	got, total, err := svc.List("", "", nil, nil, 1, 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3 (legacy row excluded)", total)
	}
	want := []struct {
		hour                           time.Time
		email, node                    string
		up, down, upBilled, downBilled int64
		mult                           float64
	}{
		{h11, "a@example.com", "n2", 100, 200, 200, 400, 2},
		{h11, "b@example.com", "n1", 50, 0, 50, 0, 1},
		{h10, "a@example.com", "n1", 100, 200, 100, 200, 1},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i, w := range want {
		r := got[i]
		if !r.Hour.Equal(w.hour) || r.Email != w.email || r.NodeID != w.node ||
			r.UpTotal != w.up || r.DownTotal != w.down ||
			r.Multiplier != w.mult || r.UpBilled != w.upBilled || r.DownBilled != w.downBilled {
			t.Errorf("row %d = %+v, want hour=%v email=%s node=%s up=%d down=%d mult=%v billed=(%d,%d)",
				i, r, w.hour, w.email, w.node, w.up, w.down, w.mult, w.upBilled, w.downBilled)
		}
	}

	// Node filter narrows to that entry point.
	got, total, err = svc.List("", "n2", nil, nil, 1, 20)
	if err != nil {
		t.Fatalf("List by node: %v", err)
	}
	if total != 1 || got[0].NodeID != "n2" || got[0].UpBilled != 200 || got[0].DownBilled != 400 {
		t.Errorf("List by node = %+v (total %d), want the single n2 row with billed (200,400)", got, total)
	}

	// Hour range [from, to): from inclusive, to exclusive.
	got, total, err = svc.List("", "", &h10, &h11, 1, 20)
	if err != nil {
		t.Fatalf("List by range: %v", err)
	}
	if total != 1 || !got[0].Hour.Equal(h10) {
		t.Errorf("List by range = %+v (total %d), want only the h10 row", got, total)
	}
}

// TestListForUserReturnsHourlyDetailRows verifies the user-facing list serves
// the caller's hourly detail rows with node names, billed bytes derived from
// the captured multiplier, newest hour first, and legacy unattributed rows
// skipped.
func TestListForUserReturnsHourlyDetailRows(t *testing.T) {
	db := trafficTestDB(t)

	user := model.User{ID: "u1", Email: "a@example.com", Enabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	for _, n := range []model.Node{
		{ID: "n1", Name: "Node One", Token: "tok-1"},
		{ID: "n2", Name: "Node Two", Token: "tok-2", TrafficMultiplier: 0.5},
	} {
		if err := db.Create(&n).Error; err != nil {
			t.Fatalf("create node: %v", err)
		}
	}

	base := time.Now().UTC().Truncate(time.Hour)
	rows := []model.TrafficHourlyStat{
		{UserID: "u1", NodeID: "n1", Hour: base.Add(-2 * time.Hour), UpTotal: 100, DownTotal: 200, Multiplier: 1},
		{UserID: "u1", NodeID: "n2", Hour: base.Add(-time.Hour), UpTotal: 100, DownTotal: 200, Multiplier: 0.5},
		// Another user's row and a legacy unattributed row: both must be excluded.
		{UserID: "u2", NodeID: "n1", Hour: base, UpTotal: 999, DownTotal: 999, Multiplier: 1},
		{UserID: "u1", NodeID: "", Hour: base, UpTotal: 999, DownTotal: 999, Multiplier: 1},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	svc := NewTrafficService(db)
	got, total, err := svc.ListForUser("u1", 1, 20)
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	// Newest hour first.
	if got[0].NodeName != "Node Two" || got[0].Multiplier != 0.5 ||
		got[0].UpBilled != 50 || got[0].DownBilled != 100 {
		t.Errorf("newest row = %+v, want Node Two with mult 0.5 and billed (50,100)", got[0])
	}
	if got[1].NodeName != "Node One" || got[1].UpBilled != 100 || got[1].DownBilled != 200 {
		t.Errorf("older row = %+v, want Node One with billed (100,200)", got[1])
	}
}
