package model

import "time"

// Balance transaction reasons. These classify every ledger entry so the wallet
// history is auditable.
const (
	// BalanceReasonPlanChangeRefund credits the remaining (unamortized) value of
	// a plan the user is leaving when they switch plans (upgrade or downgrade).
	BalanceReasonPlanChangeRefund = "plan_change_refund"
	// BalanceReasonPurchase debits the wallet to pay for an order (plan,
	// traffic package, or reset).
	BalanceReasonPurchase = "purchase"
	// BalanceReasonAdminAdjust is a manual credit/debit applied by an admin.
	BalanceReasonAdminAdjust = "admin_adjust"
	// BalanceReasonRefund credits the wallet when an order is refunded.
	BalanceReasonRefund = "refund"
)

// BalanceTransaction is a single append-only ledger row for a user's account
// balance. Credit and debit rows both carry a positive AmountCents; Type
// indicates the direction. BalanceAfter is the running balance (in cents)
// immediately after this row, kept for fast, tamper-evident history without
// recomputing a sum.
type BalanceTransaction struct {
	ID           string    `gorm:"primaryKey;size:36" json:"id"`
	UserID       string    `gorm:"index;size:36;not null" json:"user_id"`
	Type         string    `gorm:"size:16;not null" json:"type"` // "credit" | "debit"
	AmountCents  int64     `gorm:"not null" json:"amount_cents"` // always positive
	Reason       string    `gorm:"size:32;not null" json:"reason"`
	RefOrderID   string    `gorm:"size:36;index" json:"ref_order_id,omitempty"`
	BalanceAfter int64     `gorm:"not null" json:"balance_after"`
	Remark       string    `json:"remark,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Balance transaction types.
const (
	BalanceTypeCredit = "credit"
	BalanceTypeDebit  = "debit"
)
