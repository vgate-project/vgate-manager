package model

import "time"

// TrafficHourlyStat stores one per-user-per-node-per-hour traffic delta row,
// written additively by ServerService.ReportTraffic. The (user_id, node_id,
// hour) primary key lets the dashboard series aggregate across all nodes or
// filter down to a single entry point (a real node or one of its virtual
// children). Rows older than 48 hours are pruned. node_id = '' marks rows
// written before the node dimension existed ("unattributed"); they count
// toward all-node totals but toward no individual node.
type TrafficHourlyStat struct {
	UserID    string    `gorm:"primaryKey;size:36;index"`
	NodeID    string    `gorm:"primaryKey;size:26;index"` // entry point the traffic was booked to ('' = legacy unattributed)
	Hour      time.Time `gorm:"primaryKey;index"`         // hour bucket (UTC, truncated to hour)
	UpTotal   int64     `gorm:"default:0"`                // raw (un-multiplied) up bytes this hour
	DownTotal int64     `gorm:"default:0"`                // raw (un-multiplied) down bytes this hour
	CreatedAt time.Time
}
