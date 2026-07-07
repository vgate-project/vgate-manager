package model

import "time"

// Billing periods offered for a plan. The integer suffix maps to DurationDays.
const (
	PlanPeriodMonth        = "month"     // 30 days
	PlanPeriodQuarter      = "quarter"   // 90 days
	PlanPeriodHalfYear     = "half_year" // 180 days
	PlanPeriodYear         = "year"      // 365 days
	PlanPeriodMonthDays    = 30
	PlanPeriodQuarterDays  = 90
	PlanPeriodHalfYearDays = 180
	PlanPeriodYearDays     = 365
)

// DefaultDurationForPeriod returns the canonical duration (days) for a billing
// period. Unknown periods fall back to a 30-day month.
func DefaultDurationForPeriod(period string) int {
	switch period {
	case PlanPeriodQuarter:
		return PlanPeriodQuarterDays
	case PlanPeriodHalfYear:
		return PlanPeriodHalfYearDays
	case PlanPeriodYear:
		return PlanPeriodYearDays
	default:
		return PlanPeriodMonthDays
	}
}

// PlanPrice is one billing-period price point for a Plan. A plan may have
// several (e.g. monthly + yearly), each with its own server-authoritative
// Price (cents) and DurationDays. The amount used when creating an order is
// copied from here; clients cannot override it.
type PlanPrice struct {
	ID           string    `gorm:"primaryKey;size:36" json:"id"`
	PlanID       string    `gorm:"index;size:36;not null" json:"plan_id"`
	Period       string    `gorm:"size:16;not null" json:"period"` // month|quarter|half_year|year
	Price        int64     `gorm:"not null" json:"price"`          // cents (server truth)
	DurationDays int       `gorm:"not null" json:"duration_days"`  // 30|90|180|365
	Sort         int       `gorm:"default:0" json:"sort"`
	Enabled      bool      `gorm:"not null" json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
