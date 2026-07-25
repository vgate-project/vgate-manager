package cmd

import (
	"gorm.io/gorm"
)

// migrations runs idempotent data migrations after AutoMigrate. It is safe to
// call on every startup.
func migrations(db *gorm.DB) {
}
