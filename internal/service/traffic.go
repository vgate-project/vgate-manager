package service

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
)

type TrafficService struct {
	db *gorm.DB
}

func NewTrafficService(db *gorm.DB) *TrafficService {
	return &TrafficService{db: db}
}

// TrafficRecord is one per-user-per-node-per-hour traffic detail row as shown
// on the admin traffic page: the raw (un-multiplied) bytes reported that hour,
// the multiplier in effect when they were written, and the billed bytes
// (raw × multiplier) that were charged against the user's quota.
type TrafficRecord struct {
	Hour       time.Time `json:"hour"` // UTC hour bucket
	UserID     string    `json:"user_id"`
	Email      string    `json:"email"`
	NodeID     string    `json:"node_id"`
	UpTotal    int64     `json:"up_total"`
	DownTotal  int64     `json:"down_total"`
	Multiplier float64   `json:"multiplier"`
	UpBilled   int64     `json:"up_billed"`
	DownBilled int64     `json:"down_billed"`
}

// UserTrafficRecord is one hourly traffic detail row for a single user,
// enriched with the node's display name. Used by /user/traffic.
type UserTrafficRecord struct {
	Hour       time.Time `json:"hour"` // UTC hour bucket
	NodeID     string    `json:"node_id"`
	NodeName   string    `json:"node_name"`
	UpTotal    int64     `json:"up_total"`
	DownTotal  int64     `json:"down_total"`
	Multiplier float64   `json:"multiplier"`
	UpBilled   int64     `json:"up_billed"`
	DownBilled int64     `json:"down_billed"`
}

// billed rounds raw × multiplier the same way ReportTraffic rounds the bytes
// it charges, so the detail views' billed columns match the deduction math.
func billed(raw int64, mult float64) int64 {
	return int64(math.Round(float64(raw) * mult))
}

// List returns per-user-per-node hourly traffic detail rows from
// traffic_hourly_stats — the actual hourly deltas, not lifetime aggregates —
// optionally filtered by user_id, node_id and an hour range [from, to)
// (bounds truncated to the hour; from inclusive, to exclusive). Legacy
// unattributed rows (node_id = '') carry no entry point to display and are
// skipped; they age out of the retention window.
func (s *TrafficService) List(userID, nodeID string, from, to *time.Time, page, pageSize int) ([]TrafficRecord, int64, error) {
	q := s.db.Table("traffic_hourly_stats").
		Joins("JOIN users ON users.id = traffic_hourly_stats.user_id").
		Where("traffic_hourly_stats.node_id <> ''")
	if userID != "" {
		q = q.Where("traffic_hourly_stats.user_id = ?", userID)
	}
	if nodeID != "" {
		q = q.Where("traffic_hourly_stats.node_id = ?", nodeID)
	}
	if from != nil {
		q = q.Where("traffic_hourly_stats.hour >= ?", from.UTC().Truncate(time.Hour))
	}
	if to != nil {
		q = q.Where("traffic_hourly_stats.hour < ?", to.UTC().Truncate(time.Hour))
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []TrafficRecord
	err := q.Select("traffic_hourly_stats.hour, traffic_hourly_stats.user_id, users.email, traffic_hourly_stats.node_id, traffic_hourly_stats.up_total, traffic_hourly_stats.down_total, traffic_hourly_stats.multiplier").
		Order("traffic_hourly_stats.hour DESC, users.email ASC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	for i := range rows {
		rows[i].UpBilled = billed(rows[i].UpTotal, rows[i].Multiplier)
		rows[i].DownBilled = billed(rows[i].DownTotal, rows[i].Multiplier)
	}
	return rows, total, nil
}

// ListForUser returns the caller's hourly traffic detail rows, enriched with
// each node's display name, newest hour first, optionally filtered by node
// and an hour range [from, to) (bounds truncated to the hour; from inclusive,
// to exclusive). Legacy unattributed rows (node_id = '') have no node to name
// and are skipped.
func (s *TrafficService) ListForUser(userID, nodeID string, from, to *time.Time, page, pageSize int) ([]UserTrafficRecord, int64, error) {
	q := s.db.Table("traffic_hourly_stats").
		Joins("JOIN nodes ON nodes.id = traffic_hourly_stats.node_id").
		Where("traffic_hourly_stats.user_id = ?", userID)
	if nodeID != "" {
		q = q.Where("traffic_hourly_stats.node_id = ?", nodeID)
	}
	if from != nil {
		q = q.Where("traffic_hourly_stats.hour >= ?", from.UTC().Truncate(time.Hour))
	}
	if to != nil {
		q = q.Where("traffic_hourly_stats.hour < ?", to.UTC().Truncate(time.Hour))
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []UserTrafficRecord
	err := q.Select("traffic_hourly_stats.hour, traffic_hourly_stats.node_id, nodes.name as node_name, traffic_hourly_stats.up_total, traffic_hourly_stats.down_total, traffic_hourly_stats.multiplier").
		Order("traffic_hourly_stats.hour DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	for i := range rows {
		rows[i].UpBilled = billed(rows[i].UpTotal, rows[i].Multiplier)
		rows[i].DownBilled = billed(rows[i].DownTotal, rows[i].Multiplier)
	}
	return rows, total, nil
}

// HourlyForUser returns the caller's per-hour traffic for the last 24 hours.
// Rows in traffic_hourly_stats are written by ServerService.ReportTraffic as
// per-user-per-node-per-hour deltas, so the same (user, hour) can span
// multiple entry points; SUM per hour to reassemble the user's total series.
// Mirrors StatsService.GetOverview but filtered to a single user.
// The series spans [cutoff, hourNow] (one point per hour), oldest first;
// missing hours are reported as 0.
func (s *TrafficService) HourlyForUser(userID string) ([]dto.HourlyStat, error) {
	hourNow := time.Now().UTC().Truncate(time.Hour)
	cutoff := hourNow.Add(-24 * time.Hour)

	type snapRow struct {
		Hour time.Time
		Up   int64
		Down int64
	}
	var snaps []snapRow
	if err := s.db.Model(&model.TrafficHourlyStat{}).
		Select("hour, SUM(up_total) AS up, SUM(down_total) AS down").
		Where("user_id = ? AND hour >= ? AND hour <= ?", userID, cutoff, hourNow).
		Group("hour").
		Order("hour ASC").
		Scan(&snaps).Error; err != nil {
		return nil, err
	}

	byHour := make(map[time.Time]snapRow, len(snaps))
	for _, r := range snaps {
		byHour[r.Hour] = r
	}

	series := make([]dto.HourlyStat, 0, 24)
	for h := cutoff; !h.After(hourNow); h = h.Add(time.Hour) {
		if cur, ok := byHour[h]; ok {
			series = append(series, dto.HourlyStat{Hour: h, Up: cur.Up, Down: cur.Down})
		} else {
			series = append(series, dto.HourlyStat{Hour: h})
		}
	}
	return series, nil
}

// --- Aggregated stats views for the traffic pages ---

const (
	// trafficStatsMaxSpan caps the stats/aggregate range; hourly rows are
	// pruned after 30 days (DeleteOldHourlyStats), so anything older is empty.
	// Daily bucketing rounds the start down to UTC midnight, so a capped
	// range yields at most 31 day buckets.
	trafficStatsMaxSpan = 30 * 24 * time.Hour
	// trafficStatsHourlyMaxSpan is the longest range rendered as hourly
	// buckets; longer ranges collapse to UTC day buckets.
	trafficStatsHourlyMaxSpan = 48 * time.Hour
	// trafficExportRowCap caps the CSV export so a huge window cannot
	// exhaust memory building the file.
	trafficExportRowCap = 100_000
)

// TrafficTotals is the raw and billed usage over a stats range. Billed bytes
// are raw × the per-row multiplier captured at write time, rounded the same
// way ReportTraffic rounds the bytes it charges.
type TrafficTotals struct {
	Up         int64 `json:"up"`
	Down       int64 `json:"down"`
	UpBilled   int64 `json:"up_billed"`
	DownBilled int64 `json:"down_billed"`
}

// BucketStat is one point of the usage series: a UTC hour bucket or, for
// ranges longer than trafficStatsHourlyMaxSpan, a UTC day bucket.
type BucketStat struct {
	Bucket time.Time `json:"bucket"`
	Up     int64     `json:"up"`
	Down   int64     `json:"down"`
}

// NodeUsage is one node's share of the usage in a stats range.
type NodeUsage struct {
	NodeID   string  `json:"node_id"`
	NodeName string  `json:"node_name"`
	Up       int64   `json:"up"`
	Down     int64   `json:"down"`
	Share    float64 `json:"share"` // fraction of total up+down, 0..1
}

// TrafficStats is the aggregated usage view served to the traffic pages:
// totals for the summary cards, a zero-filled series for the trend chart and
// a per-node breakdown for the distribution panel.
type TrafficStats struct {
	From   time.Time     `json:"from"`            // inclusive, UTC hour bucket
	To     time.Time     `json:"to"`              // exclusive, UTC hour bucket
	Bucket string        `json:"bucket"`          // "hour" or "day"
	Totals TrafficTotals `json:"totals"`          // over the whole range
	Series []BucketStat  `json:"series"`          // oldest first, gaps zero-filled
	ByNode []NodeUsage   `json:"by_node"`         // sorted by total usage desc
}

// Stats aggregates traffic_hourly_stats over [from, to) for the summary
// cards, trend chart and per-node breakdown. userID empty means all users
// (admin view); nodeID empty means all entry points. An empty/missing range
// defaults to the last 24 hourly buckets including the current one; the range
// is clamped to the current hour and to trafficStatsMaxSpan. Legacy
// unattributed rows (node_id = '') are skipped, matching the detail lists.
func (s *TrafficService) Stats(userID, nodeID string, from, to *time.Time) (*TrafficStats, error) {
	hourNow := time.Now().UTC().Truncate(time.Hour)
	start := hourNow.Add(-23 * time.Hour)
	end := hourNow.Add(time.Hour) // exclusive; includes the current partial hour
	if from != nil {
		start = from.UTC().Truncate(time.Hour)
	}
	if to != nil {
		end = to.UTC().Truncate(time.Hour)
	}
	if end.After(hourNow.Add(time.Hour)) {
		end = hourNow.Add(time.Hour)
	}
	if start.Before(end.Add(-trafficStatsMaxSpan)) {
		start = end.Add(-trafficStatsMaxSpan)
	}

	stats := &TrafficStats{From: start, To: end, Bucket: "hour", Series: []BucketStat{}, ByNode: []NodeUsage{}}
	if !start.Before(end) {
		return stats, nil
	}
	if end.Sub(start) > trafficStatsHourlyMaxSpan {
		stats.Bucket = "day"
	}

	// Each query chain gets a fresh builder: the filter set is shared, but a
	// finished GORM statement must not be reused across finishers.
	base := func() *gorm.DB {
		q := s.db.Table("traffic_hourly_stats").
			Where("traffic_hourly_stats.node_id <> ''")
		if userID != "" {
			q = q.Where("traffic_hourly_stats.user_id = ?", userID)
		}
		if nodeID != "" {
			q = q.Where("traffic_hourly_stats.node_id = ?", nodeID)
		}
		return q.Where("traffic_hourly_stats.hour >= ? AND traffic_hourly_stats.hour < ?", start, end)
	}

	var hourRows []struct {
		Hour time.Time
		Up   int64
		Down int64
	}
	if err := base().
		Select("traffic_hourly_stats.hour, SUM(traffic_hourly_stats.up_total) AS up, SUM(traffic_hourly_stats.down_total) AS down").
		Group("traffic_hourly_stats.hour").
		Order("traffic_hourly_stats.hour ASC").
		Scan(&hourRows).Error; err != nil {
		return nil, err
	}

	var totals struct {
		Up          int64
		Down        int64
		UpBilled    float64
		DownBilled  float64
	}
	if err := base().
		Select("COALESCE(SUM(traffic_hourly_stats.up_total), 0) AS up, COALESCE(SUM(traffic_hourly_stats.down_total), 0) AS down, "+
			"COALESCE(SUM(traffic_hourly_stats.up_total * traffic_hourly_stats.multiplier), 0) AS up_billed, "+
			"COALESCE(SUM(traffic_hourly_stats.down_total * traffic_hourly_stats.multiplier), 0) AS down_billed").
		Scan(&totals).Error; err != nil {
		return nil, err
	}
	stats.Totals = TrafficTotals{
		Up:         totals.Up,
		Down:       totals.Down,
		UpBilled:   int64(math.Round(totals.UpBilled)),
		DownBilled: int64(math.Round(totals.DownBilled)),
	}

	var nodeRows []struct {
		NodeID   string
		NodeName string
		Up       int64
		Down     int64
	}
	if err := base().
		Joins("JOIN nodes ON nodes.id = traffic_hourly_stats.node_id").
		Select("traffic_hourly_stats.node_id, nodes.name AS node_name, SUM(traffic_hourly_stats.up_total) AS up, SUM(traffic_hourly_stats.down_total) AS down").
		Group("traffic_hourly_stats.node_id, nodes.name").
		Scan(&nodeRows).Error; err != nil {
		return nil, err
	}

	// Bucket the hourly sums (zero-filling gaps); ranges beyond the hourly
	// limit collapse to UTC day buckets, merged in Go so the SQL stays
	// dialect-agnostic between SQLite and Postgres.
	byBucket := make(map[time.Time]*BucketStat)
	for _, r := range hourRows {
		key := r.Hour
		if stats.Bucket == "day" {
			key = r.Hour.Truncate(24 * time.Hour)
		}
		cur, ok := byBucket[key]
		if !ok {
			cur = &BucketStat{Bucket: key}
			byBucket[key] = cur
		}
		cur.Up += r.Up
		cur.Down += r.Down
	}
	if stats.Bucket == "hour" {
		for h := start; h.Before(end); h = h.Add(time.Hour) {
			if cur, ok := byBucket[h]; ok {
				stats.Series = append(stats.Series, *cur)
			} else {
				stats.Series = append(stats.Series, BucketStat{Bucket: h})
			}
		}
	} else {
		for d := start.Truncate(24 * time.Hour); d.Before(end); d = d.Add(24 * time.Hour) {
			if cur, ok := byBucket[d]; ok {
				stats.Series = append(stats.Series, *cur)
			} else {
				stats.Series = append(stats.Series, BucketStat{Bucket: d})
			}
		}
	}

	grand := stats.Totals.Up + stats.Totals.Down
	for _, r := range nodeRows {
		share := 0.0
		if grand > 0 {
			share = float64(r.Up+r.Down) / float64(grand)
		}
		stats.ByNode = append(stats.ByNode, NodeUsage{
			NodeID: r.NodeID, NodeName: r.NodeName, Up: r.Up, Down: r.Down, Share: share,
		})
	}
	sort.Slice(stats.ByNode, func(i, j int) bool {
		a, b := stats.ByNode[i], stats.ByNode[j]
		return a.Up+a.Down > b.Up+b.Down
	})
	return stats, nil
}

// TrafficAggregateRow is one row of the admin traffic page's grouped views:
// usage summed per user ("By User") or per entry point ("By Node") over the
// filtered range. Only the key fields of the chosen grouping are populated.
type TrafficAggregateRow struct {
	UserID      string `json:"user_id,omitempty"`
	Email       string `json:"email,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	NodeName    string `json:"node_name,omitempty"`
	Up          int64  `json:"up"`
	Down        int64  `json:"down"`
	UpBilled    int64  `json:"up_billed"`
	DownBilled  int64  `json:"down_billed"`
	ActiveHours int64  `json:"active_hours"` // distinct hour buckets with traffic
}

// Aggregate groups traffic_hourly_stats by "user" or "node" over the filtered
// range, sorted by total usage desc, paginated. Powers the admin page's
// By User / By Node tabs.
func (s *TrafficService) Aggregate(groupBy, userID, nodeID string, from, to *time.Time, page, pageSize int) ([]TrafficAggregateRow, int64, error) {
	q := s.db.Table("traffic_hourly_stats").
		Where("traffic_hourly_stats.node_id <> ''")
	if userID != "" {
		q = q.Where("traffic_hourly_stats.user_id = ?", userID)
	}
	if nodeID != "" {
		q = q.Where("traffic_hourly_stats.node_id = ?", nodeID)
	}
	if from != nil {
		q = q.Where("traffic_hourly_stats.hour >= ?", from.UTC().Truncate(time.Hour))
	}
	if to != nil {
		q = q.Where("traffic_hourly_stats.hour < ?", to.UTC().Truncate(time.Hour))
	}

	var total int64
	var err error
	switch groupBy {
	case "user":
		err = q.Distinct("traffic_hourly_stats.user_id").Count(&total).Error
	case "node":
		err = q.Distinct("traffic_hourly_stats.node_id").Count(&total).Error
	default:
		return nil, 0, fmt.Errorf("invalid group_by %q: want \"user\" or \"node\"", groupBy)
	}
	if err != nil {
		return nil, 0, err
	}

	var scan []struct {
		UserID      string
		Email       string
		NodeID      string
		NodeName    string
		Up          int64
		Down        int64
		UpBilled    float64
		DownBilled  float64
		ActiveHours int64
	}
	if groupBy == "user" {
		q = q.Joins("JOIN users ON users.id = traffic_hourly_stats.user_id").
			Select("traffic_hourly_stats.user_id, users.email, "+
				"SUM(traffic_hourly_stats.up_total) AS up, SUM(traffic_hourly_stats.down_total) AS down, "+
				"SUM(traffic_hourly_stats.up_total * traffic_hourly_stats.multiplier) AS up_billed, "+
				"SUM(traffic_hourly_stats.down_total * traffic_hourly_stats.multiplier) AS down_billed, "+
				"COUNT(DISTINCT traffic_hourly_stats.hour) AS active_hours").
			Group("traffic_hourly_stats.user_id, users.email")
	} else {
		q = q.Joins("JOIN nodes ON nodes.id = traffic_hourly_stats.node_id").
			Select("traffic_hourly_stats.node_id, nodes.name AS node_name, "+
				"SUM(traffic_hourly_stats.up_total) AS up, SUM(traffic_hourly_stats.down_total) AS down, "+
				"SUM(traffic_hourly_stats.up_total * traffic_hourly_stats.multiplier) AS up_billed, "+
				"SUM(traffic_hourly_stats.down_total * traffic_hourly_stats.multiplier) AS down_billed, "+
				"COUNT(DISTINCT traffic_hourly_stats.hour) AS active_hours").
			Group("traffic_hourly_stats.node_id, nodes.name")
	}
	if err := q.
		Order("SUM(traffic_hourly_stats.up_total) + SUM(traffic_hourly_stats.down_total) DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Scan(&scan).Error; err != nil {
		return nil, 0, err
	}

	rows := make([]TrafficAggregateRow, 0, len(scan))
	for _, r := range scan {
		row := TrafficAggregateRow{
			Up: r.Up, Down: r.Down,
			UpBilled:    int64(math.Round(r.UpBilled)),
			DownBilled:  int64(math.Round(r.DownBilled)),
			ActiveHours: r.ActiveHours,
		}
		if groupBy == "user" {
			row.UserID, row.Email = r.UserID, r.Email
		} else {
			row.NodeID, row.NodeName = r.NodeID, r.NodeName
		}
		rows = append(rows, row)
	}
	return rows, total, nil
}

// ExportCSV writes the hourly detail rows matching the filters as CSV (one
// row per user-per-node-per-hour, same shape as List). The caller writes the
// UTF-8 BOM; hour stamps are UTC RFC3339 so the file is timezone-explicit.
// Output is capped at trafficExportRowCap rows.
func (s *TrafficService) ExportCSV(w io.Writer, userID, nodeID string, from, to *time.Time) error {
	rows, _, err := s.List(userID, nodeID, from, to, 1, trafficExportRowCap)
	if err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{
		"hour_utc", "user_id", "email", "node_id",
		"upload_bytes", "download_bytes", "multiplier",
		"billed_upload_bytes", "billed_download_bytes",
	}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{
			r.Hour.UTC().Format(time.RFC3339),
			r.UserID, r.Email, r.NodeID,
			strconv.FormatInt(r.UpTotal, 10),
			strconv.FormatInt(r.DownTotal, 10),
			strconv.FormatFloat(r.Multiplier, 'f', -1, 64),
			strconv.FormatInt(r.UpBilled, 10),
			strconv.FormatInt(r.DownBilled, 10),
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
