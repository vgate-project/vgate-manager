package dto

import (
	"time"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// --- Redemption codes (admin + user) ---

// AdminGenerateRedemptionRequest is the body for an admin batch code generation.
// Type selects the benefit; the relevant param is required:
//   - traffic  → QuotaBytes > 0
//   - duration → DurationDays > 0
//   - plan     → PlanID set (must be an enabled plan)
//   - reset    → no extra params
//
// Count is how many distinct codes to mint; MaxUses is how many distinct users
// may redeem each code; ExpiresAt is optional.
type AdminGenerateRedemptionRequest struct {
	Type         string     `json:"type" binding:"required"`
	QuotaBytes   int64      `json:"quota_bytes"`
	DurationDays int        `json:"duration_days"`
	PlanID       string     `json:"plan_id"`
	MaxUses      int        `json:"max_uses"`
	ExpiresAt    *time.Time `json:"expires_at"`
	Count        int        `json:"count" binding:"required,min=1"`
	Note         string     `json:"note"`
}

// RedeemRequest is the body for a user redeeming a code.
type RedeemRequest struct {
	Code string `json:"code" binding:"required"`
}

// RedeemResponse summarizes the benefit applied by a successful redemption.
type RedeemResponse struct {
	Record  model.RedemptionRecord `json:"record"`
	Type    string                 `json:"type"`
	Message string                 `json:"message"`
}
