package model

import "time"

// Admin is a manager administrator account.
type Admin struct {
	ID                    uint       `gorm:"primaryKey" json:"id"`
	Username              string     `gorm:"uniqueIndex;size:64;not null" json:"username"`
	PasswordHash          string     `gorm:"size:128;not null" json:"-"`
	Role                  string     `gorm:"size:16;default:'admin'" json:"role"` // super_admin | admin
	TelegramID            int64      `gorm:"index;default:0" json:"telegram_id"`  // linked Telegram chat ID (0 = none)
	TelegramBindToken     string     `gorm:"size:64;default:''" json:"-"`
	TelegramBindExpiresAt *time.Time `json:"-"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}
