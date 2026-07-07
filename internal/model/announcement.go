package model

import "time"

// Announcement is a site-wide message authored by an admin and shown to users
// (e.g. in the user SPA). Pinned announcements sort first; inactive ones are
// hidden from users but remain editable by admins.
type Announcement struct {
	ID            string    `gorm:"primaryKey;size:36" json:"id"`
	Title         string    `gorm:"size:255" json:"title"`
	Content       string    `gorm:"type:text" json:"content"`
	Pinned        bool      `gorm:"default:false" json:"pinned"`
	Active        bool      `gorm:"default:true;index" json:"active"`
	AuthorAdminID uint      `json:"author_admin_id"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
