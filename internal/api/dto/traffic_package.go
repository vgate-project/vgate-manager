package dto

// --- Traffic packages (one-time traffic add-ons) ---

// TrafficPackageRequest is the create/update body for a traffic package.
// Enabled is a pointer so "false" is distinguishable from omitted (defaults to
// true on create).
type TrafficPackageRequest struct {
	Name         string `json:"name" binding:"required"`
	Price        int64  `json:"price" binding:"required"` // cents
	QuotaBytes   int64  `json:"quota_bytes" binding:"required"`
	ValidityDays int    `json:"validity_days"` // 0 = no expiry extension
	Description  string `json:"description"`
	Enabled      *bool  `json:"enabled"`
}
