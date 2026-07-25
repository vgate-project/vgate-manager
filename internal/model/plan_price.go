package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

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

// PlanPriceEntry is a billing-period price point that lives as a JSON array
// element inside Plan.Prices (replacing the separate plan_prices table).
// It has no database identity of its own — prices are identified by Period
// within a plan.
type PlanPriceEntry struct {
	Period       string `json:"period"`        // month|quarter|half_year|year
	Price        int64  `json:"price"`         // cents
	DurationDays int    `json:"duration_days"` // 30|90|180|365
	Sort         int    `json:"sort"`
	Enabled      bool   `json:"enabled"`
}

// PlanPrices is a JSON column containing a plan's pricing options. It
// implements driver.Valuer / sql.Scanner so GORM can serialize it to a
// single JSON column on the plans table.
type PlanPrices []PlanPriceEntry

func (p *PlanPrices) Scan(value any) error {
	if value == nil {
		*p = nil
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		str, ok := value.(string)
		if !ok {
			return errors.New("unsupported Scan value for PlanPrices")
		}
		bytes = []byte(str)
	}
	return json.Unmarshal(bytes, p)
}

func (p PlanPrices) Value() (driver.Value, error) {
	if len(p) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(p)
	return string(b), err
}

// PlanPrice is the legacy table-backed billing-period price point. It is
// retained only so historical plan_prices rows (referenced by old Orders)
// remain readable. New plans store pricing in Plan.Prices (of type PlanPrices
// / PlanPriceEntry) as a JSON column.
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
