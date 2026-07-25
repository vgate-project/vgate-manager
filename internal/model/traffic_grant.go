package model

import "time"

// TrafficGrant records a single one-time traffic-quota grant applied to a user,
// whether from a purchased TrafficPackage or a redeemed traffic-type
// RedemptionCode. Grants are permanent add-ons: once granted, the bonus lives on
// the user's traffic_quota_bytes until the user is deleted, and it is never
// partially reclaimed. Traffic is charged to grants FIFO across a user's active
// grants so each grant's remaining (QuotaBytes - UsedBytes) can be shown
// precisely.
type TrafficGrant struct {
	ID         string     `gorm:"primaryKey;size:36" json:"id"`
	UserID     string     `gorm:"index;not null" json:"user_id"`
	Source     string     `gorm:"size:16;not null" json:"source"` // "traffic_package" | "redemption"
	SourceID   string     `gorm:"size:36" json:"source_id"`       // traffic_package id or redemption_code id
	// Name is a denormalized display label (the traffic-package Name, or the
	// redemption Code) so clients can render the grant without extra lookups.
	Name       string     `gorm:"size:128" json:"name"`
	QuotaBytes int64      `gorm:"not null" json:"quota_bytes"`
	// UsedBytes is the traffic already consumed from this grant. Traffic is
	// charged to grants FIFO across a user's active grants, so UsedBytes lets
	// clients show the remaining (QuotaBytes - UsedBytes) precisely.
	UsedBytes  int64      `gorm:"default:0" json:"used_bytes"`
	GrantedAt  time.Time  `gorm:"not null" json:"granted_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Traffic grant sources.
const (
	GrantSourceTrafficPackage = "traffic_package"
	GrantSourceRedemption     = "redemption"
)

// RemainingBytes returns the un-consumed quota of the grant (never negative).
func (g *TrafficGrant) RemainingBytes() int64 {
	r := g.QuotaBytes - g.UsedBytes
	if r < 0 {
		return 0
	}
	return r
}
