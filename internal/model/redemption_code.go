package model

import "time"

// Redemption benefit types. A RedemptionCode grants the benefit
// selected by Type when an authenticated user redeems its Code.
const (
	// RedeemTypeTraffic adds QuotaBytes to the user's quota (opts the user
	// out of the global monthly reset so the grant is not wiped).
	RedeemTypeTraffic = "traffic"
	// RedeemTypeDuration extends the user's ExpireAt by DurationDays (and
	// re-enables the user if currently disabled).
	RedeemTypeDuration = "duration"
	// RedeemTypePlan applies the plan identified by PlanID as a free
	// subscription (sets quota/level/expiry like a paid plan order).
	RedeemTypePlan = "plan"
	// RedeemTypeReset zeroes the user's used traffic counters (a traffic
	// reset package), leaving quota/level/expiry untouched.
	RedeemTypeReset = "reset"
)

// RedemptionCode is an admin-issued token that grants a benefit when redeemed
// by a user. Type selects the benefit; the relevant *Params fields carry the
// benefit's parameters. A code may be redeemed up to MaxUses times, once per
// distinct user (enforced via RedemptionRecord), and never after ExpiresAt.
type RedemptionCode struct {
	ID        string     `gorm:"primaryKey;size:36" json:"id"`
	Code      string     `gorm:"uniqueIndex;size:64" json:"code"`
	Type      string     `gorm:"size:16;index" json:"type"` // traffic|duration|plan|reset
	MaxUses   int        `gorm:"default:1" json:"max_uses"`
	UsedCount int        `gorm:"default:0" json:"used_count"`
	ExpiresAt *time.Time `gorm:"index" json:"expires_at,omitempty"`
	Note      string     `gorm:"size:255" json:"note,omitempty"`

	// Benefit parameters, interpreted according to Type.
	QuotaBytes   int64  `gorm:"default:0" json:"quota_bytes,omitempty"`   // type=traffic
	DurationDays int    `gorm:"default:0" json:"duration_days,omitempty"` // type=duration
	PlanID       string `gorm:"size:36;index" json:"plan_id,omitempty"`   // type=plan

	CreatedByAdminID *uint     `gorm:"index" json:"created_by_admin_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Remaining returns how many more distinct users can redeem this code.
func (c *RedemptionCode) Remaining() int {
	r := c.MaxUses - c.UsedCount
	if r < 0 {
		return 0
	}
	return r
}

// Exhausted reports whether the code has no remaining uses or is expired.
func (c *RedemptionCode) Exhausted(now time.Time) bool {
	if c.Remaining() <= 0 {
		return true
	}
	if c.ExpiresAt != nil && now.After(*c.ExpiresAt) {
		return true
	}
	return false
}

// RedemptionRecord is one user redemption of a code. The (code_id, user_id)
// unique pair prevents a single user from draining a multi-use code, so
// MaxUses counts distinct users rather than total redemptions.
type RedemptionRecord struct {
	ID         string    `gorm:"primaryKey;size:36" json:"id"`
	CodeID     string    `gorm:"size:36;uniqueIndex:uk_code_user;index" json:"code_id"`
	UserID     string    `gorm:"size:36;uniqueIndex:uk_code_user;index" json:"user_id"`
	Type       string    `gorm:"size:16" json:"type"` // snapshot of code.Type
	RedeemedAt time.Time `json:"redeemed_at"`
}
