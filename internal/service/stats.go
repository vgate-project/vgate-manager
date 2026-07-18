package service

import (
	"time"

	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
)

type StatsService struct {
	db *gorm.DB
}

func NewStatsService(db *gorm.DB) *StatsService {
	return &StatsService{db: db}
}

// DeleteOldHourlyStats prunes traffic_hourly_stat rows older than 48h. The hourly
// per-user deltas are now written directly by ServerService.ReportTraffic, so
// this job no longer creates snapshots — it only expires stale rows. Idempotent.
func (s *StatsService) DeleteOldHourlyStats() error {
	cutoff := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	return s.db.Where("hour < ?", cutoff).Delete(&model.TrafficHourlyStat{}).Error
}

// GetOverview computes dashboard statistics: node/user counts (total +
// online) and an hourly traffic series for the last 24 hours. The series and
// 24h totals are derived by SUMming the per-user hourly deltas stored in
// traffic_hourly_stat (written additively by ServerService.ReportTraffic), so
// no separate cumulative-total query is needed and a quota reset / plan
// purchase that zeroes users.up_total can no longer produce a negative hour.
func (s *StatsService) GetOverview() (*dto.OverviewStats, error) {
	now := time.Now()
	hourNow := now.UTC().Truncate(time.Hour)
	cutoff := hourNow.Add(-24 * time.Hour)
	dayAgo := now.Add(-24 * time.Hour)
	prevDayAgo := now.Add(-48 * time.Hour)
	weekLater := now.Add(7 * 24 * time.Hour)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	// User stats: total count + active in the last 24h (one query).
	var userStats struct {
		Count     int64
		Online24h int64
	}
	if err := s.db.Model(&model.User{}).Select(
		"COUNT(*) AS count, "+
			"COUNT(CASE WHEN last_traffic_at IS NOT NULL AND last_traffic_at >= ? THEN 1 END) AS online_24h",
		dayAgo,
	).Scan(&userStats).Error; err != nil {
		return nil, err
	}

	// Node stats: total count + online. Virtual child nodes never poll, so we
	// attribute their parent's liveness (consistent with the node list / user
	// node list, which both call hydrateVirtualOnline).
	nodeCount, nodeOnline, err := s.nodeCounts()
	if err != nil {
		return nil, err
	}

	// Per-user hourly deltas are written directly by ServerService.ReportTraffic,
	// so each bucket below is already that hour's traffic (no telescoping). SUM
	// across users per hour; emit one point per hour (0 when no data). Because
	// bars are never derived by subtracting cumulative counters, a quota reset /
	// plan purchase that zeroes up_total can no longer produce a negative hour.
	type snapRow struct {
		Hour time.Time
		Up   int64
		Down int64
	}
	var snaps []snapRow
	prevStart := cutoff.Add(-24 * time.Hour) // start of previous 24h window (= hourNow - 48h)
	if err := s.db.Model(&model.TrafficHourlyStat{}).
		Select("hour, SUM(up_total) AS up, SUM(down_total) AS down").
		Where("hour >= ? AND hour <= ?", prevStart, hourNow).
		Group("hour").Order("hour ASC").
		Scan(&snaps).Error; err != nil {
		return nil, err
	}

	byHour := make(map[time.Time]snapRow, len(snaps))
	for _, r := range snaps {
		byHour[r.Hour] = r
	}

	// One point per hour from cutoff to hourNow (inclusive). Missing hours are 0.
	series := make([]dto.HourlyStat, 0, 24)
	for h := cutoff; !h.After(hourNow); h = h.Add(time.Hour) {
		if cur, ok := byHour[h]; ok {
			series = append(series, dto.HourlyStat{Hour: h, Up: cur.Up, Down: cur.Down})
		} else {
			series = append(series, dto.HourlyStat{Hour: h})
		}
	}

	// 24h totals = sum of the hourly buckets in the current window.
	var up24h, down24h int64
	for _, p := range series {
		up24h += p.Up
		down24h += p.Down
	}

	// Previous 24h traffic totals, for day-over-day comparison: sum of the
	// buckets in the earlier half of the same query window ([prevStart, cutoff)).
	up24hPrev, down24hPrev := int64(0), int64(0)
	for h := prevStart; h.Before(cutoff); h = h.Add(time.Hour) {
		if cur, ok := byHour[h]; ok {
			up24hPrev += cur.Up
			down24hPrev += cur.Down
		}
	}

	// User health: one query with conditional counts.
	var userHealth struct {
		Expiring7d     int64
		QuotaExhausted int64
		Unverified     int64
		NewToday       int64
		NewYesterday   int64
	}
	if err := s.db.Model(&model.User{}).Select(
		"COUNT(CASE WHEN expire_at IS NOT NULL AND expire_at <= ? THEN 1 END) AS expiring_7d, "+
			"COUNT(CASE WHEN quota_bytes > 0 AND (up_total + down_total) >= quota_bytes THEN 1 END) AS quota_exhausted, "+
			"COUNT(CASE WHEN email_verified = false THEN 1 END) AS unverified, "+
			"COUNT(CASE WHEN created_at >= ? THEN 1 END) AS new_today, "+
			"COUNT(CASE WHEN created_at >= ? AND created_at < ? THEN 1 END) AS new_yesterday",
		weekLater, todayStart, prevDayAgo, dayAgo,
	).Scan(&userHealth).Error; err != nil {
		return nil, err
	}

	// Previous 24h active users + paid orders, for day-over-day comparison.
	var onlinePrev int64
	if err := s.db.Model(&model.User{}).Select(
		"COUNT(CASE WHEN last_traffic_at IS NOT NULL AND last_traffic_at >= ? AND last_traffic_at < ? THEN 1 END) AS c",
		prevDayAgo, dayAgo,
	).Scan(&onlinePrev).Error; err != nil {
		return nil, err
	}

	var orderPrev struct {
		Count int64
		Total int64
	}
	if err := s.db.Model(&model.Order{}).
		Select("COUNT(*) AS count, COALESCE(SUM(amount), 0) AS total").
		Where("status = ? AND paid_at >= ? AND paid_at < ?", model.OrderStatusPaid, prevDayAgo, dayAgo).
		Scan(&orderPrev).Error; err != nil {
		return nil, err
	}

	// Pending (unpaid) orders: operational attention metric.
	var orderPending int64
	if err := s.db.Model(&model.Order{}).
		Where("status = ?", model.OrderStatusPending).
		Count(&orderPending).Error; err != nil {
		return nil, err
	}

	// Order stats: paid orders in the last 24h (real revenue). Window is on
	// paid_at so an order created earlier but paid within 24h counts.
	var orderStats struct {
		Count int64
		Total int64
	}
	if err := s.db.Model(&model.Order{}).
		Select("COUNT(*) AS count, COALESCE(SUM(amount), 0) AS total").
		Where("status = ? AND paid_at >= ?", model.OrderStatusPaid, dayAgo).
		Scan(&orderStats).Error; err != nil {
		return nil, err
	}

	return &dto.OverviewStats{
		NodeCount:      nodeCount,
		NodeOnline:     nodeOnline,
		UserCount:      userStats.Count,
		OnlineUsers24h: userStats.Online24h,
		Up24h:          up24h,
		Down24h:        down24h,
		Series:         series,
		OrderCount24h:  orderStats.Count,
		OrderAmount24h: orderStats.Total,

		ExpiringUsers7d:     userHealth.Expiring7d,
		QuotaExhaustedUsers: userHealth.QuotaExhausted,
		UnverifiedUsers:     userHealth.Unverified,
		NewUsersToday:       userHealth.NewToday,
		NewUsersYesterday:   userHealth.NewYesterday,

		OrderPendingCount: orderPending,

		Up24hPrev:          up24hPrev,
		Down24hPrev:        down24hPrev,
		OnlineUsers24hPrev: onlinePrev,
		OrderCount24hPrev:  orderPrev.Count,
		OrderAmount24hPrev: orderPrev.Total,
	}, nil
}

// nodeCounts returns the total number of nodes and how many are currently
// online. Virtual child nodes never poll the manager, so they inherit their
// parent's liveness via hydrateVirtualOnline — the same semantics the node
// list and user node list use. A virtual node is online exactly when its real
// parent is (and offline when the parent is missing or stale).
func (s *StatsService) nodeCounts() (total, online int64, err error) {
	var nodes []*model.Node
	if err := s.db.Model(&model.Node{}).
		Select("id", "parent_id", "last_seen_at", "enabled").
		Find(&nodes).Error; err != nil {
		return 0, 0, err
	}
	if err := hydrateVirtualOnline(s.db, nodes); err != nil {
		return 0, 0, err
	}
	for _, n := range nodes {
		if n.IsOnline() {
			online++
		}
	}
	return int64(len(nodes)), online, nil
}
