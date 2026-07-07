package dto

// --- Plans (purchasable products) ---

// PlanPriceInput is one billing-period price for a plan, submitted as part of
// the plan create/update body.
type PlanPriceInput struct {
	ID           string `json:"id,omitempty"`              // present on update for existing rows
	Period       string `json:"period" binding:"required"` // month|quarter|half_year|year
	Price        int64  `json:"price" binding:"required"`  // cents
	DurationDays int    `json:"duration_days"`             // optional; defaults from period
	Sort         int    `json:"sort"`
	Enabled      *bool  `json:"enabled"`
}

// PlanRequest is the create/update body for a plan. A plan carries its product
// attributes plus a list of billing-period prices (at least one for purchase).
// Enabled is a pointer so "false" is distinguishable from omitted (defaults to
// true on create).
type PlanRequest struct {
	Name         string           `json:"name" binding:"required"`
	Prices       []PlanPriceInput `json:"prices" binding:"required,min=1"`
	QuotaBytes   int64            `json:"quota_bytes"`
	Description  string           `json:"description"`
	Level        int              `json:"level"`
	Enabled      *bool            `json:"enabled"`
	ResetEnabled bool             `json:"reset_enabled"` // plan-scoped traffic reset package
	ResetPrice   int64            `json:"reset_price"`   // cents
}
