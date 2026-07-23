package model

import "time"

// Plan is a purchasable product group: a bundle of traffic quota + user level
// that a user buys via an alipay order. Pricing is NOT stored here — it lives
// in the related PlanPrice rows so a single plan can be offered at different
// price points for different billing periods (month/quarter/half-year/year).
type Plan struct {
	ID          string `gorm:"primaryKey;size:36" json:"id"`
	Name        string `gorm:"size:128;not null" json:"name"`
	// DisplayName is an optional product name pushed to the payment gateway
	// instead of the built-in default subject. Empty ⇒ the global
	// payment.product_name_template (then the built-in default) is used.
	DisplayName string `gorm:"size:128" json:"display_name"`
	Description string `gorm:"type:text" json:"description"`
	Level            int   `gorm:"default:0" json:"level"`
	QuotaBytes       int64 `gorm:"not null;default:0" json:"quota_bytes"`
	SpeedLimitUpBps  int64 `gorm:"not null;default:0" json:"speed_limit_up_bps"`
	SpeedLimitDownBps int64 `gorm:"not null;default:0" json:"speed_limit_down_bps"`
	Enabled          bool  `gorm:"not null" json:"enabled"`
	// AllowRenewOffShelf lets a user who already owns this plan renew it even
	// after the plan is disabled (taken off the shelf). New users can never
	// purchase an off-shelf plan regardless of this flag. Admins are exempt.
	AllowRenewOffShelf bool `gorm:"not null;default:false" json:"allow_renew_off_shelf"`
	// ResetEnabled / ResetPrice define an optional plan-scoped "traffic reset
	// package": when enabled, a user with this plan active can self-purchase a
	// reset that zeroes their used traffic (up_total/down_total) without
	// changing quota_bytes / level / expiry. ResetPrice is in cents.
	ResetEnabled bool        `gorm:"not null;default:false" json:"reset_enabled"`
	ResetPrice   int64       `gorm:"not null;default:0" json:"reset_price"`
	Prices       []PlanPrice `gorm:"foreignKey:PlanID;constraint:OnDelete:CASCADE" json:"prices,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}
