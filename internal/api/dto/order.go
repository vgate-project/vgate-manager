package dto

import "github.com/vgate-project/vgate-manager/internal/model"

// --- Orders (alipay purchases) ---

type CreateOrderRequest struct {
	Kind             string `json:"kind" binding:"required"` // "plan" | "traffic" | "reset"
	PlanID           string `json:"plan_id"`                 // required when kind=plan
	PlanPriceID      string `json:"plan_price_id"`           // required when kind=plan
	TrafficPackageID string `json:"traffic_package_id"`      // required when kind=traffic
	// Platform is the chosen payment gateway (one of the values returned by
	// GET /user/payment-methods). Optional: when empty the server picks the
	// first admin-enabled, configured channel.
	Platform string `json:"platform"`
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
	// Wallet fields: the amount of this order the wallet balance covered, and
	// the balance remaining after that debit. Populated whenever the wallet is
	// used (0 for a pure gateway payment) so the client can prompt the user
	// that the wallet auto-paid instead of the gateway they selected.
	WalletUsedCents      int64 `json:"wallet_used_cents"`
	WalletRemainingCents int64 `json:"wallet_remaining_cents"`
}

// ChangePlanRequest is the body for POST /user/change-plan.
type ChangePlanRequest struct {
	PlanID      string `json:"plan_id" binding:"required"`
	PlanPriceID string `json:"plan_price_id" binding:"required"`
	// Platform is the chosen payment gateway for the top-up portion of the
	// switch. Optional: when empty the server picks the first enabled,
	// configured channel.
	Platform string `json:"platform"`
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

// PaymentMethodInfo describes a payment channel surfaced to the frontend
// picker. Enabled reflects the admin's explicit toggle (absent = enabled);
// Configured reflects whether the gateway credentials are present.
type PaymentMethodInfo struct {
	Platform   string `json:"platform"`
	Label      string `json:"label"`
	Mode       string `json:"mode"` // "redirect" | "qr" | "iap"
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
}

// AppleVerifyRequest is the body for POST /user/orders/:id/apple-verify, posted
// by the native app after an App Store purchase. transaction is the signed
// JWS transaction returned by StoreKit.
type AppleVerifyRequest struct {
	Transaction string `json:"transaction" binding:"required"`
}
