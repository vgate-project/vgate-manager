package model

import "time"

// InviteCode grants the holder the right to register (when registration is
// gated by invite) up to MaxUses times. Codes are created either by an admin
// (CreatedByUserID is nil) or by a regular user within their invite quota
// (CreatedByUserID set, and Σ UsedCount across that user's codes must stay
// within the quota). Consuming a code increments UsedCount; registration is
// rejected once UsedCount >= MaxUses or the code is expired.
type InviteCode struct {
	ID               string     `gorm:"primaryKey;size:36" json:"id"`
	Code             string     `gorm:"uniqueIndex;size:64" json:"code"`
	CreatedByUserID  *string    `gorm:"size:36;index" json:"created_by_user_id,omitempty"` // nil ⇒ admin-generated
	CreatedByAdminID *uint      `gorm:"index" json:"created_by_admin_id,omitempty"`
	Note             string     `gorm:"size:255" json:"note,omitempty"`
	MaxUses          int        `gorm:"default:1" json:"max_uses"`
	UsedCount        int        `gorm:"default:0" json:"used_count"`
	ExpiresAt        *time.Time `gorm:"index" json:"expires_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// Remaining returns how many more registrations this code can sponsor.
func (c *InviteCode) Remaining() int {
	r := c.MaxUses - c.UsedCount
	if r < 0 {
		return 0
	}
	return r
}

// Exhausted reports whether the code has no remaining uses or is expired.
func (c *InviteCode) Exhausted(now time.Time) bool {
	if c.Remaining() <= 0 {
		return true
	}
	if c.ExpiresAt != nil && now.After(*c.ExpiresAt) {
		return true
	}
	return false
}
