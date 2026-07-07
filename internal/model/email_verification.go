package model

import "time"

// EmailVerification stores a single-use token emailed to a user to prove
// ownership of an address (currently used for registration activation). A token
// is valid while ConsumedAt is nil and ExpiresAt is in the future.
type EmailVerification struct {
	ID         string     `gorm:"primaryKey;size:36" json:"id"`
	UserID     string     `gorm:"size:36;index" json:"user_id"`
	Email      string     `gorm:"size:255" json:"email"`
	Token      string     `gorm:"uniqueIndex;size:64" json:"-"`
	Purpose    string     `gorm:"size:32" json:"purpose"` // "register" | "reset_password"
	ExpiresAt  time.Time  `gorm:"index" json:"expires_at"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Consumed reports whether the token has already been used.
func (v *EmailVerification) Consumed() bool {
	return v.ConsumedAt != nil
}

// Expired reports whether the token is past its expiry.
func (v *EmailVerification) Expired(now time.Time) bool {
	return now.After(v.ExpiresAt)
}

// Valid reports whether the token can still be redeemed.
func (v *EmailVerification) Valid(now time.Time) bool {
	return !v.Consumed() && !v.Expired(now)
}
