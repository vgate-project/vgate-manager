package service

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
)

type StatsService struct {
	db *gorm.DB
}

func NewStatsService(db *gorm.DB) *StatsService {
	return &StatsService{db: db}
}

// AggregateHourly snapshots each user's cumulative traffic totals into
// traffic_hourly_stat for the current hour, then prunes rows older than 48h.
// Idempotent: re-running for the same hour overwrites the snapshot.
func (s *StatsService) AggregateHourly() error {
	hour := time.Now().UTC().Truncate(time.Hour)

	var users []model.User
	if err := s.db.Select("id", "up_total", "down_total").Find(&users).Error; err != nil {
		return err
	}

	if len(users) > 0 {
		rows := make([]model.TrafficHourlyStat, 0, len(users))
		for _, u := range users {
			rows = append(rows, model.TrafficHourlyStat{
				UserID:    u.ID,
				Hour:      hour,
				UpTotal:   u.UpTotal,
				DownTotal: u.DownTotal,
			})
		}
		if err := s.db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "hour"}},
			DoUpdates: clause.AssignmentColumns([]string{"up_total", "down_total"}),
		}).Create(&rows).Error; err != nil {
			return err
		}
	}

	cutoff := hour.Add(-48 * time.Hour)
	return s.db.Where("hour < ?", cutoff).Delete(&model.TrafficHourlyStat{}).Error
}

// GetOverview computes dashboard statistics: node/user counts (total +
// online) and an hourly traffic series for the last 24 hours. The 24h traffic
// totals are derived by summing the series deltas (a telescoping sum of the
// hourly snapshots), so no separate cumulative-total query is needed.
func (s *StatsService) GetOverview() (*dto.OverviewStats, error) {
	now := time.Now()
	hourNow := now.UTC().Truncate(time.Hour)
	cutoff := hourNow.Add(-24 * time.Hour)
	dayAgo := now.Add(-24 * time.Hour)
	prevDayAgo := now.Add(-48 * time.Hour)
	weekLater := now.Add(7 * 24 * time.Hour)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	lastSeenCutoff := now.Add(-model.NodeOnlineWindow)

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

	// Node stats: total count + online (last_seen within 2 min, one query).
	var nodeStats struct {
		Count  int64
		Online int64
	}
	if err := s.db.Model(&model.Node{}).Select(
		"COUNT(*) AS count, "+
			"COUNT(CASE WHEN last_seen_at IS NOT NULL AND last_seen_at >= ? THEN 1 END) AS online",
		lastSeenCutoff,
	).Scan(&nodeStats).Error; err != nil {
		return nil, err
	}

	// Hourly cumulative sums for the last 25 hours (one extra to compute the
	// first delta). Each series point = snapshot[h] - snapshot[h-1].
	type snapRow struct {
		Hour time.Time
		Up   int64
		Down int64
	}
	var snaps []snapRow
	if err := s.db.Model(&model.TrafficHourlyStat{}).
		Select("hour, SUM(up_total) AS up, SUM(down_total) AS down").
		// Fetch 49h: the previous 24h period needs its baseline snapshot at
		// (cutoff-24h)-1h, so the window must start one hour before that.
		Where("hour >= ? AND hour <= ?", cutoff.Add(-25*time.Hour), hourNow).
		Group("hour").Order("hour ASC").
		Scan(&snaps).Error; err != nil {
		return nil, err
	}

	// Build a lookup so missing hours are treated as carry-forward (0 delta).
	byHour := make(map[time.Time]snapRow, len(snaps))
	for _, r := range snaps {
		byHour[r.Hour] = r
	}

	series := make([]dto.HourlyStat, 0, 24)
	prevUp, prevDown := int64(0), int64(0)
	if baseline, ok := byHour[cutoff.Add(-time.Hour)]; ok {
		prevUp, prevDown = baseline.Up, baseline.Down
	}
	for h := cutoff; !h.After(hourNow); h = h.Add(time.Hour) {
		cur2, ok := byHour[h]
		if ok {
			series = append(series, dto.HourlyStat{
				Hour: h,
				Up:   cur2.Up - prevUp,
				Down: cur2.Down - prevDown,
			})
			prevUp, prevDown = cur2.Up, cur2.Down
		} else {
			// No snapshot for this hour → 0 usage (no data collected).
			series = append(series, dto.HourlyStat{Hour: h})
		}
	}

	// 24h totals = sum of the hourly deltas.
	var up24h, down24h int64
	for _, p := range series {
		up24h += p.Up
		down24h += p.Down
	}

	// Previous 24h traffic totals, for day-over-day comparison. Same
	// telescoping over the snapshot map, shifted back 24h.
	prevStart := cutoff.Add(-24 * time.Hour)
	prevBaseUp, prevBaseDown := int64(0), int64(0)
	if b, ok := byHour[prevStart.Add(-time.Hour)]; ok {
		prevBaseUp, prevBaseDown = b.Up, b.Down
	}
	up24hPrev, down24hPrev := int64(0), int64(0)
	for h := prevStart; !h.After(cutoff); h = h.Add(time.Hour) {
		if cur, ok := byHour[h]; ok {
			up24hPrev += cur.Up - prevBaseUp
			down24hPrev += cur.Down - prevBaseDown
			prevBaseUp, prevBaseDown = cur.Up, cur.Down
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
		NodeCount:      nodeStats.Count,
		NodeOnline:     nodeStats.Online,
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
