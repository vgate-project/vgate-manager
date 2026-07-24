package dto

import "github.com/vgate-project/vgate-manager/internal/model"

// --- Orders (alipay purchases) ---

type CreateOrderRequest struct {
	Kind             string `json:"kind" binding:"required"` // "plan" | "traffic" | "reset"
	PlanID           string `json:"plan_id"`                 // required when kind=plan
	PlanPriceID      string `json:"plan_price_id"`           // required when kind=plan
	TrafficPackageID string `json:"traffic_package_id"`      // required when kind=traffic
	Platform         string `json:"platform"`                // optional: payment gateway; defaults to alipay
}

type AdminCreateOrderRequest struct {
	UserID           string `json:"user_id" binding:"required"`
	Kind             string `json:"kind" binding:"required"`
	PlanID           string `json:"plan_id"`
	PlanPriceID      string `json:"plan_price_id"`
	TrafficPackageID string `json:"traffic_package_id"`
	Platform         string `json:"platform"` // optional: payment gateway; defaults to alipay
}

type CreateOrderResponse struct {
	Order   *model.Order `json:"order"`
	PayURL  string       `json:"pay_url"`
	PayMode string       `json:"pay_mode"` // "redirect" | "qr" — how to present PayURL to the user
	Paid    bool         `json:"paid"`     // true when fully covered by the wallet (no gateway step)
}

// ChangePlanRequest is the body for POST /user/change-plan.
type ChangePlanRequest struct {
	PlanID      string `json:"plan_id" binding:"required"`
	PlanPriceID string `json:"plan_price_id" binding:"required"`
}

// BalanceResponse is the body for GET /user/balance (and the admin variant):
// the current wallet balance plus paginated ledger history.
type BalanceResponse struct {
	BalanceCents int64                      `json:"balance_cents"`
	Ledger       []model.BalanceTransaction `json:"ledger"`
	Total        int64                      `json:"total"`
	Page         int                        `json:"page"`
	PageSize     int                        `json:"page_size"`
}

// AdminAdjustBalanceRequest is the body for POST /admin/users/:id/balance.
type AdminAdjustBalanceRequest struct {
	DeltaCents int64  `json:"delta_cents" binding:"required"` // positive = grant, negative = deduct
	Remark     string `json:"remark"`
}

// UpdateOrderStatusRequest is the admin manual status-change body. Only
// "paid" and "closed" are accepted (pending is the terminal source state).
type UpdateOrderStatusRequest struct {
	Status string `json:"status" binding:"required,oneof=paid closed"`
}
