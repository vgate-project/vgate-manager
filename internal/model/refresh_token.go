package model

import "time"

// RefreshToken is a revocable admin refresh token (DB-stored, not a pure JWT).
type RefreshToken struct {
	ID        string    `gorm:"primaryKey;size:32"` // crypto-random opaque token
	AdminID   uint      `gorm:"index;not null"`
	ExpiresAt time.Time `gorm:"index"`
	Revoked   bool      `gorm:"default:false"`
	CreatedAt time.Time
}
