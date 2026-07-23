package model

import "time"

const (
	OrderStatusPending = "pending"
	OrderStatusPaid    = "paid"
	OrderStatusClosed  = "closed"

	// OrderPlatform* identifies which payment gateway an order belongs to.
	// "alipay" is the implemented gateway; "manual" is set when an admin marks
	// an order paid by hand (no real gateway). Future platforms (wechat,
	// stripe, ...) are added here and wired into the payment Registry.
	OrderPlatformAlipay = "alipay"
	OrderPlatformWechat = "wechat"
	OrderPlatformStripe = "stripe"
	OrderPlatformManual = "manual"

	// OrderKindPlan is a recurring subscription purchase (applies a plan's
	// level + quota + duration to the user).
	OrderKindPlan = "plan"
	// OrderKindTraffic is a one-time traffic add-on purchase.
	OrderKindTraffic = "traffic"
	// OrderKindReset is a plan-scoped traffic reset: zeroes the user's used
	// traffic (up_total/down_total) without changing quota/level/expiry.
	OrderKindReset = "reset"
)

// Order records a single alipay purchase attempt. It is kind-aware:
//   - kind=plan:    references PlanID + PlanPriceID; carries the chosen
//     Period/DurationDays (copied from the price at creation).
//   - kind=traffic: references TrafficPackageID; carries ValidityDays.
//
// Amount is copied from the authoritative source (plan price or traffic
// package) at creation time; clients cannot override it.
type Order struct {
	ID               string     `gorm:"primaryKey;size:36" json:"id"`
	UserID           string     `gorm:"index;size:36;not null" json:"user_id"`
	Kind             string     `gorm:"size:16;not null;default:'plan'" json:"kind"`
	PlanID           string     `gorm:"index;size:36" json:"plan_id,omitempty"`
	PlanPriceID      string     `gorm:"index;size:36" json:"plan_price_id,omitempty"`
	Period           string     `gorm:"size:16" json:"period,omitempty"`
	DurationDays     int        `gorm:"default:0" json:"duration_days"`
	TrafficPackageID string     `gorm:"index;size:36" json:"traffic_package_id,omitempty"`
	ValidityDays     int        `gorm:"default:0" json:"validity_days"`
	Amount           int64      `gorm:"not null" json:"amount"` // cents, copied from source
	Status           string     `gorm:"index;size:16;not null;default:'pending'" json:"status"`
	Platform         string     `gorm:"index;size:16" json:"platform"` // payment gateway: alipay | manual | (future)
	OutTradeNo       string     `gorm:"uniqueIndex;size:64;not null" json:"out_trade_no"`
	TradeNo          string     `gorm:"size:64" json:"trade_no,omitempty"` // gateway-assigned transaction id
	PaidAt           *time.Time `json:"paid_at,omitempty"`
	ExpiredAt        *time.Time `gorm:"index" json:"expired_at,omitempty"` // cron close threshold
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}
