package model

import "time"

// TrafficGrant records a single one-time traffic-quota grant applied to a user,
// whether from a purchased TrafficPackage or a redeemed traffic-type
// RedemptionCode. It lets the manager reclaim the granted quota precisely when
// the grant expires, instead of merging the bonus into the user's scalar
// quota_bytes (which could never be partially reclaimed).
//
// Grants with a nil ExpireAt are permanent: they survive until the user is
// deleted and are never auto-reclaimed. That models a TrafficPackage with
// validity_days = 0 (no independent expiry — access is gated only by the
// user's own expire_at) and a redeemed traffic code.
type TrafficGrant struct {
	ID         string     `gorm:"primaryKey;size:36" json:"id"`
	UserID     string     `gorm:"index;not null" json:"user_id"`
	Source     string     `gorm:"size:16;not null" json:"source"` // "traffic_package" | "redemption"
	SourceID   string     `gorm:"size:36" json:"source_id"`       // traffic_package id or redemption_code id
	QuotaBytes int64      `gorm:"not null" json:"quota_bytes"`
	GrantedAt  time.Time  `gorm:"not null" json:"granted_at"`
	ExpireAt   *time.Time `gorm:"index" json:"expire_at,omitempty"` // nil = no independent expiry (permanent)
	Reclaimed  bool       `gorm:"default:false;index" json:"reclaimed"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Traffic grant sources.
const (
	GrantSourceTrafficPackage = "traffic_package"
	GrantSourceRedemption     = "redemption"
)

// IsExpired reports whether the grant's own expiry has passed. Permanent grants
// (nil ExpireAt) are never expired.
func (g *TrafficGrant) IsExpired(now time.Time) bool {
	return g.ExpireAt != nil && g.ExpireAt.Before(now)
}
