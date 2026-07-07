package model

import "time"

// SystemConfig stores runtime key/value settings (default_sync_interval,
// jwt_ttl, etc.). The JWT secret stays in config.yml, NOT here.
type SystemConfig struct {
	Key       string `gorm:"primaryKey;size:64"`
	Value     string `gorm:"type:text"`
	UpdatedAt time.Time
}
