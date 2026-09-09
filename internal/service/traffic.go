package service

import (
	"math"
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
// each node's display name, newest hour first. Legacy unattributed rows
// (node_id = '') have no node to name and are skipped.
func (s *TrafficService) ListForUser(userID string, page, pageSize int) ([]UserTrafficRecord, int64, error) {
	q := s.db.Table("traffic_hourly_stats").
		Joins("JOIN nodes ON nodes.id = traffic_hourly_stats.node_id").
		Where("traffic_hourly_stats.user_id = ?", userID)
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
