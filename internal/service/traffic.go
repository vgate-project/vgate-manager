package service

import (
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

// TrafficRow is one per-node-per-user cumulative traffic row.
type TrafficRow struct {
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	NodeID    string `json:"node_id"`
	UpTotal   int64  `json:"up_total"`
	DownTotal int64  `json:"down_total"`
}

// UserTrafficRow is one per-node cumulative traffic row for a single user,
// enriched with the node's display name.
type UserTrafficRow struct {
	NodeID    string `json:"node_id"`
	NodeName  string `json:"node_name"`
	UpTotal   int64  `json:"up_total"`
	DownTotal int64  `json:"down_total"`
}

// List returns per-node-per-user cumulative traffic, optionally filtered by
// user_id and/or node_id. Time-range filtering requires the (deferred)
// traffic log table and is not supported in v1.
func (s *TrafficService) List(userID, nodeID string, page, pageSize int) ([]TrafficRow, int64, error) {
	q := s.db.Table("user_node_traffic").
		Joins("JOIN users ON users.id = user_node_traffic.user_id")
	if userID != "" {
		q = q.Where("user_node_traffic.user_id = ?", userID)
	}
	if nodeID != "" {
		q = q.Where("user_node_traffic.node_id = ?", nodeID)
	}
	var total int64
	q.Count(&total)
	var rows []TrafficRow
	err := q.Select("user_node_traffic.user_id, users.email, user_node_traffic.node_id, user_node_traffic.up_total, user_node_traffic.down_total").
		Order("user_node_traffic.updated_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Scan(&rows).Error
	return rows, total, err
}

// ListForUser returns the caller's per-node cumulative traffic, enriched with
// each node's display name. Used by the user-facing /user/traffic endpoint.
func (s *TrafficService) ListForUser(userID string, page, pageSize int) ([]UserTrafficRow, int64, error) {
	q := s.db.Table("user_node_traffic").
		Joins("JOIN nodes ON nodes.id = user_node_traffic.node_id")
	if userID != "" {
		q = q.Where("user_node_traffic.user_id = ?", userID)
	}
	var total int64
	q.Count(&total)
	var rows []UserTrafficRow
	err := q.Select("user_node_traffic.node_id, nodes.name as node_name, user_node_traffic.up_total, user_node_traffic.down_total").
		Order("user_node_traffic.updated_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Scan(&rows).Error
	return rows, total, err
}

// HourlyForUser returns the caller's per-hour traffic for the last 24 hours.
// Rows in traffic_hourly_stat are written by ServerService.ReportTraffic as
// per-user, per-hour deltas, so each row is already that hour's traffic (no
// telescoping). Mirrors StatsService.GetOverview but filtered to a single user.
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
		Select("hour, up_total AS up, down_total AS down").
		Where("user_id = ? AND hour >= ? AND hour <= ?", userID, cutoff, hourNow).
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
