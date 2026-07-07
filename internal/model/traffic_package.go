package model

import "time"

// TrafficPackage is a one-time, non-recurring traffic add-on a user buys via an
// alipay order. Unlike a Plan it does not carry a user level and has no fixed
// billing period. ValidityDays is OPTIONAL: when > 0 the purchased quota is
// usable only until the user's ExpireAt is extended by that many days; when 0
// the quota is added with no extra expiry (the user's existing ExpireAt gates
// access as usual).
type TrafficPackage struct {
	ID           string    `gorm:"primaryKey;size:36" json:"id"`
	Name         string    `gorm:"size:128;not null" json:"name"`
	Price        int64     `gorm:"not null" json:"price"` // cents (server truth)
	QuotaBytes   int64     `gorm:"not null" json:"quota_bytes"`
	ValidityDays int       `gorm:"default:0" json:"validity_days"` // 0 = no expiry extension
	Description  string    `gorm:"type:text" json:"description"`
	Enabled      bool      `gorm:"default:true" json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
