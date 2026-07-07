package model

import "time"

// UserNodeTraffic tracks cumulative per-node-per-user traffic, enabling the
// admin traffic view to filter by node_id. (Per-user totals also live on
// User.) Populated atomically from node-reported deltas.
type UserNodeTraffic struct {
	UserID    string `gorm:"primaryKey;size:36"`
	NodeID    string `gorm:"primaryKey;size:26"`
	UpTotal   int64  `gorm:"default:0"`
	DownTotal int64  `gorm:"default:0"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName fixes the table name (GORM would otherwise pluralize to the
// grammatically-wrong "user_node_traffics").
func (UserNodeTraffic) TableName() string { return "user_node_traffic" }
