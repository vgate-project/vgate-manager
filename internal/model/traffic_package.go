package model

import "time"

// TrafficPackage is a one-time, non-recurring traffic add-on a user buys via
// an alipay order. Unlike a Plan it does not carry a user level and has no
// fixed billing period. The granted quota is permanent (a TrafficGrant with no
// ExpireAt) — it is never auto-reclaimed and survives base-plan resets.
type TrafficPackage struct {
	ID   string `gorm:"primaryKey;size:36" json:"id"`
	Name string `gorm:"size:128;not null" json:"name"`
	// DisplayName is an optional product name pushed to the payment gateway
	// instead of the built-in default (the package Name). Empty ⇒ the global
	// payment.product_name_template (then the built-in default) is used.
	DisplayName string    `gorm:"size:128" json:"display_name"`
	Price       int64     `gorm:"not null" json:"price"` // cents (server truth)
	QuotaBytes  int64     `gorm:"not null" json:"quota_bytes"`
	Description string    `gorm:"type:text" json:"description"`
	Enabled     bool      `gorm:"default:true" json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
