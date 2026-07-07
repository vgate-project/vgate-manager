package model

import "time"

// TrafficHourlyStat stores a per-user cumulative-traffic snapshot at each hour
// boundary. The hourly aggregation job upserts one row per (user, hour). 24h
// usage is computed by subtracting the snapshot from 24 hours ago from the
// current cumulative total. Rows older than 48 hours are pruned.
type TrafficHourlyStat struct {
	UserID    string    `gorm:"primaryKey;size:36;index"`
	Hour      time.Time `gorm:"primaryKey;index"` // hour bucket (UTC, truncated to hour)
	UpTotal   int64     `gorm:"default:0"`        // cumulative up_total at this hour
	DownTotal int64     `gorm:"default:0"`        // cumulative down_total at this hour
	CreatedAt time.Time
}
