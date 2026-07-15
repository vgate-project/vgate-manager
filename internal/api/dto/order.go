package dto

import "github.com/vgate-project/vgate-manager/internal/model"

// --- Orders (alipay purchases) ---

type CreateOrderRequest struct {
	Kind             string `json:"kind" binding:"required"` // "plan" | "traffic" | "reset"
	PlanID           string `json:"plan_id"`                 // required when kind=plan
	PlanPriceID      string `json:"plan_price_id"`           // required when kind=plan
	TrafficPackageID string `json:"traffic_package_id"`      // required when kind=traffic
	Channel          string `json:"channel"`                 // optional: "pc" | "wap" | "" (auto by UA)
	Platform         string `json:"platform"`                // optional: payment gateway; defaults to alipay
}

type AdminCreateOrderRequest struct {
	UserID           string `json:"user_id" binding:"required"`
	Kind             string `json:"kind" binding:"required"`
	PlanID           string `json:"plan_id"`
	PlanPriceID      string `json:"plan_price_id"`
	TrafficPackageID string `json:"traffic_package_id"`
	Channel          string `json:"channel"`
	Platform         string `json:"platform"` // optional: payment gateway; defaults to alipay
}

type CreateOrderResponse struct {
	Order   *model.Order `json:"order"`
	PayURL  string       `json:"pay_url"`
	PayMode string       `json:"pay_mode"` // "redirect" | "qr" — how to present PayURL to the user
}

// UpdateOrderStatusRequest is the admin manual status-change body. Only
// "paid" and "closed" are accepted (pending is the terminal source state).
type UpdateOrderStatusRequest struct {
	Status string `json:"status" binding:"required,oneof=paid closed"`
}
