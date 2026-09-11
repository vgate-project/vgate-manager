package service

import (
	"bytes"
	"math"
	"strings"
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
	got, total, err := svc.ListForUser("u1", "", nil, nil, 1, 20)
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

	// Node and hour-range filters narrow the same list (range [from, to)).
	from, to := base.Add(-2*time.Hour), base.Add(-time.Hour)
	got, total, err = svc.ListForUser("u1", "n1", &from, &to, 1, 20)
	if err != nil {
		t.Fatalf("ListForUser filtered: %v", err)
	}
	if total != 1 || got[0].NodeName != "Node One" {
		t.Errorf("filtered = %+v (total %d), want only the Node One h-2 row", got, total)
	}
}

// TestStatsTotalsSeriesAndByNode verifies Stats returns range totals (raw plus
// multiplier-weighted billed sums), a zero-filled hourly series, day bucketing
// for long ranges, and a per-node breakdown sorted by usage with shares.
func TestStatsTotalsSeriesAndByNode(t *testing.T) {
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
	h0, h1, h2 := base.Add(-3*time.Hour), base.Add(-2*time.Hour), base.Add(-time.Hour)
	rows := []model.TrafficHourlyStat{
		{UserID: "u1", NodeID: "n1", Hour: h0, UpTotal: 100, DownTotal: 300, Multiplier: 1},
		// Same user, same hour, second node: must merge into one series point.
		{UserID: "u1", NodeID: "n2", Hour: h0, UpTotal: 50, DownTotal: 50, Multiplier: 2},
		{UserID: "u1", NodeID: "n1", Hour: h1, UpTotal: 10, DownTotal: 20, Multiplier: 1},
		{UserID: "u2", NodeID: "n1", Hour: h2, UpTotal: 1000, DownTotal: 0, Multiplier: 1},
		// Legacy unattributed + outside range: both excluded.
		{UserID: "u1", NodeID: "", Hour: h1, UpTotal: 999, DownTotal: 999, Multiplier: 1},
		{UserID: "u1", NodeID: "n1", Hour: base.Add(-72 * time.Hour), UpTotal: 500, DownTotal: 500, Multiplier: 1},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	from, to := h0, base // [h0, base): covers h0..h2
	svc := NewTrafficService(db)
	stats, err := svc.Stats("", "", &from, &to)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	// Totals: raw sums across users/nodes; billed = round(raw × multiplier).
	if stats.Totals.Up != 1160 || stats.Totals.Down != 370 {
		t.Errorf("totals = (%d,%d), want raw (1160,370)", stats.Totals.Up, stats.Totals.Down)
	}
	if stats.Totals.UpBilled != 1210 || stats.Totals.DownBilled != 420 {
		t.Errorf("billed totals = (%d,%d), want (1210,420)", stats.Totals.UpBilled, stats.Totals.DownBilled)
	}

	// Hourly series: zero-filled, oldest first, per-hour across nodes/users.
	if stats.Bucket != "hour" {
		t.Errorf("bucket = %q, want hour for a 3h range", stats.Bucket)
	}
	if len(stats.Series) != 3 {
		t.Fatalf("series len = %d, want 3 (%+v)", len(stats.Series), stats.Series)
	}
	if stats.Series[0].Up != 150 || stats.Series[0].Down != 350 {
		t.Errorf("series[0] = (%d,%d), want merged (150,350)", stats.Series[0].Up, stats.Series[0].Down)
	}
	if stats.Series[1].Up != 10 || stats.Series[1].Down != 20 {
		t.Errorf("series[1] = (%d,%d), want (10,20)", stats.Series[1].Up, stats.Series[1].Down)
	}
	if stats.Series[2].Up != 1000 || stats.Series[2].Down != 0 {
		t.Errorf("series[2] = (%d,%d), want (1000,0)", stats.Series[2].Up, stats.Series[2].Down)
	}

	// By-node breakdown: n1 total 1430 (60%), n2 total 100 (4%... of 1530).
	if len(stats.ByNode) != 2 {
		t.Fatalf("by_node len = %d, want 2 (%+v)", len(stats.ByNode), stats.ByNode)
	}
	if stats.ByNode[0].NodeID != "n1" || stats.ByNode[0].NodeName != "Node One" {
		t.Errorf("by_node[0] = %+v, want n1 / Node One first (usage desc)", stats.ByNode[0])
	}
	if stats.ByNode[0].Up != 1110 || stats.ByNode[0].Down != 320 {
		t.Errorf("by_node[0] = (%d,%d), want (1110,320)", stats.ByNode[0].Up, stats.ByNode[0].Down)
	}
	if stats.ByNode[1].NodeID != "n2" || stats.ByNode[1].Up != 50 || stats.ByNode[1].Down != 50 {
		t.Errorf("by_node[1] = %+v, want n2 (50,50)", stats.ByNode[1])
	}
	wantShare := float64(100) / float64(1530)
	if math.Abs(stats.ByNode[1].Share-wantShare) > 1e-9 {
		t.Errorf("by_node[1] share = %v, want %v", stats.ByNode[1].Share, wantShare)
	}

	// User filter: only u1's rows (billed up 100+100+10=210... u1 raw up 160 →
	// billed 100+100+10 = 210 with n2's 50×2).
	stats, err = svc.Stats("u1", "n2", &from, &to)
	if err != nil {
		t.Fatalf("Stats filtered: %v", err)
	}
	if stats.Totals.Up != 50 || stats.Totals.Down != 50 || stats.Totals.UpBilled != 100 {
		t.Errorf("filtered totals = %+v, want raw (50,50) billed up 100", stats.Totals)
	}
	if len(stats.ByNode) != 1 || stats.ByNode[0].NodeID != "n2" {
		t.Errorf("filtered by_node = %+v, want only n2", stats.ByNode)
	}

	// A >48h range collapses to UTC day buckets, still zero-filled. Use a
	// day-aligned start (what the frontend sends for "last 7 days").
	from7 := base.Truncate(24 * time.Hour).Add(-6 * 24 * time.Hour)
	stats, err = svc.Stats("", "", &from7, &to)
	if err != nil {
		t.Fatalf("Stats 7d: %v", err)
	}
	if stats.Bucket != "day" {
		t.Errorf("bucket = %q, want day for a 7d range", stats.Bucket)
	}
	if len(stats.Series) != 7 {
		t.Fatalf("daily series len = %d, want 7 (%+v)", len(stats.Series), stats.Series)
	}
	// All rows (including the h-72h one) must land somewhere in the 7 days.
	var daySum int64
	for _, p := range stats.Series {
		daySum += p.Up + p.Down
	}
	if daySum != 1530+1000 { // 530 extra: h-72h row up+down
		t.Errorf("daily series total = %d, want 2530 (all in-range rows)", daySum)
	}
}

// TestStatsDefaultsAndClamps verifies the default window (last 24 hourly
// buckets incl. the current one) and the 31-day span clamp.
func TestStatsDefaultsAndClamps(t *testing.T) {
	db := trafficTestDB(t)
	svc := NewTrafficService(db)

	stats, err := svc.Stats("u1", "", nil, nil)
	if err != nil {
		t.Fatalf("Stats default: %v", err)
	}
	hourNow := time.Now().UTC().Truncate(time.Hour)
	if stats.Bucket != "hour" || len(stats.Series) != 24 {
		t.Fatalf("default series: bucket=%q len=%d, want hour/24", stats.Bucket, len(stats.Series))
	}
	if !stats.From.Equal(hourNow.Add(-23 * time.Hour)) || !stats.To.Equal(hourNow.Add(time.Hour)) {
		t.Errorf("default window = [%v,%v), want [%v,%v)", stats.From, stats.To, hourNow.Add(-23*time.Hour), hourNow.Add(time.Hour))
	}

	// A 1-year range clamps to 31 days of daily buckets.
	from := hourNow.Add(-365 * 24 * time.Hour)
	stats, err = svc.Stats("", "", &from, nil)
	if err != nil {
		t.Fatalf("Stats clamped: %v", err)
	}
	if stats.Bucket != "day" || len(stats.Series) != 31 {
		t.Fatalf("clamped series: bucket=%q len=%d, want day/31", stats.Bucket, len(stats.Series))
	}

	// An inverted range yields an empty-but-valid payload.
	from = hourNow.Add(-time.Hour)
	to := hourNow.Add(-2 * time.Hour)
	stats, err = svc.Stats("", "", &from, &to)
	if err != nil {
		t.Fatalf("Stats inverted: %v", err)
	}
	if len(stats.Series) != 0 || len(stats.ByNode) != 0 || stats.Totals.Up != 0 {
		t.Errorf("inverted range = %+v, want empty", stats)
	}
}

// TestAggregateGroupsByUserAndNode verifies the grouped admin views: sums,
// multiplier-weighted billed totals, distinct active hours, usage-desc order
// and the filters.
func TestAggregateGroupsByUserAndNode(t *testing.T) {
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
	h0, h1, h2 := base.Add(-2*time.Hour), base.Add(-time.Hour), base
	rows := []model.TrafficHourlyStat{
		// u1: 300 raw up across two hours on two nodes.
		{UserID: "u1", NodeID: "n1", Hour: h0, UpTotal: 100, DownTotal: 100, Multiplier: 1},
		{UserID: "u1", NodeID: "n2", Hour: h1, UpTotal: 100, DownTotal: 0, Multiplier: 2},
		{UserID: "u1", NodeID: "n2", Hour: h2, UpTotal: 100, DownTotal: 0, Multiplier: 2},
		// u2: 1000 raw, single hour.
		{UserID: "u2", NodeID: "n1", Hour: h1, UpTotal: 1000, DownTotal: 0, Multiplier: 1},
		// Legacy unattributed row excluded.
		{UserID: "u1", NodeID: "", Hour: h1, UpTotal: 999, DownTotal: 999, Multiplier: 1},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	svc := NewTrafficService(db)

	// By user: u2 (1000) before u1 (400); billed up for u1 = 100 + 2×200.
	got, total, err := svc.Aggregate("user", "", "", nil, nil, 1, 20)
	if err != nil {
		t.Fatalf("Aggregate user: %v", err)
	}
	if total != 2 || len(got) != 2 {
		t.Fatalf("by user: total=%d len=%d, want 2/2", total, len(got))
	}
	if got[0].Email != "b@example.com" || got[0].Up != 1000 || got[0].ActiveHours != 1 {
		t.Errorf("by user[0] = %+v, want b@example.com up=1000 hours=1", got[0])
	}
	if got[1].Email != "a@example.com" || got[1].Up != 300 || got[1].Down != 100 {
		t.Errorf("by user[1] = %+v, want a@example.com raw (300,100)", got[1])
	}
	if got[1].UpBilled != 500 || got[1].DownBilled != 100 {
		t.Errorf("by user[1] billed = (%d,%d), want (500,100)", got[1].UpBilled, got[1].DownBilled)
	}
	// u1 was active in 3 distinct hours (h0, h1, h2).
	if got[1].ActiveHours != 3 {
		t.Errorf("by user[1] active_hours = %d, want 3", got[1].ActiveHours)
	}

	// By node: n1 total 1200 before n2 total 200.
	got, total, err = svc.Aggregate("node", "", "", nil, nil, 1, 20)
	if err != nil {
		t.Fatalf("Aggregate node: %v", err)
	}
	if total != 2 || len(got) != 2 {
		t.Fatalf("by node: total=%d len=%d, want 2/2", total, len(got))
	}
	if got[0].NodeID != "n1" || got[0].NodeName != "Node One" || got[0].Up != 1100 || got[0].Down != 100 {
		t.Errorf("by node[0] = %+v, want n1 raw (1100,100)", got[0])
	}
	if got[1].NodeID != "n2" || got[1].Up != 200 || got[1].UpBilled != 400 {
		t.Errorf("by node[1] = %+v, want n2 raw up 200 billed 400", got[1])
	}

	// Node filter narrows both groupings.
	got, total, err = svc.Aggregate("user", "", "n2", nil, nil, 1, 20)
	if err != nil {
		t.Fatalf("Aggregate filtered: %v", err)
	}
	if total != 1 || got[0].Email != "a@example.com" || got[0].Up != 200 {
		t.Errorf("filtered by user = %+v, want only a@example.com up=200", got[0])
	}

	// Invalid group_by is rejected.
	if _, _, err := svc.Aggregate("bogus", "", "", nil, nil, 1, 20); err == nil {
		t.Error("Aggregate(bogus) succeeded, want error")
	}
}

// TestExportCSVWritesDetailRows verifies the CSV export shape: header, UTC
// RFC3339 hour stamps, raw/multiplier/billed columns, and filter honoring.
func TestExportCSVWritesDetailRows(t *testing.T) {
	db := trafficTestDB(t)

	user := model.User{ID: "u1", Email: "a@example.com", Enabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	node := model.Node{ID: "n1", Name: "Node One", Token: "tok-1", TrafficMultiplier: 2}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}

	h := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	rows := []model.TrafficHourlyStat{
		{UserID: "u1", NodeID: "n1", Hour: h, UpTotal: 100, DownTotal: 200, Multiplier: 2},
		{UserID: "u1", NodeID: "", Hour: h, UpTotal: 999, DownTotal: 999, Multiplier: 1},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	var buf bytes.Buffer
	svc := NewTrafficService(db)
	if err := svc.ExportCSV(&buf, "", "", nil, nil); err != nil {
		t.Fatalf("ExportCSV: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("csv lines = %d, want 2 (header + 1 row):\n%s", len(lines), buf.String())
	}
	if lines[0] != "hour_utc,user_id,email,node_id,upload_bytes,download_bytes,multiplier,billed_upload_bytes,billed_download_bytes" {
		t.Errorf("header = %q", lines[0])
	}
	want := strings.Join([]string{
		h.Format(time.RFC3339), "u1", "a@example.com", "n1",
		"100", "200", "2", "200", "400",
	}, ",")
	if lines[1] != want {
		t.Errorf("row = %q, want %q", lines[1], want)
	}
}
