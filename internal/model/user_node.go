package model

import "time"

// UserNode is the admin override / exception table: it grants a specific user
// access to a node whose level is above the user's level (the default gate is
// node.level <= user.level, enforced without any row here). Within-level nodes
// are usable by virtue of the level tier and need no UserNode row. Override marks
// a grant that bypasses the level gate.
type UserNode struct {
	UserID string `gorm:"primaryKey;size:36"`
	NodeID string `gorm:"primaryKey;size:26"`
	User   User   `gorm:"foreignKey:UserID"`
	Node   Node   `gorm:"foreignKey:NodeID"`
	// Override lets an admin grant a specific user access to a node whose level
	// exceeds the user's level (the default gate is node.level <= user.level).
	Override  bool `gorm:"default:false" json:"override"`
	CreatedAt time.Time
}
