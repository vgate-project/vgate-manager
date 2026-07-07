package dto

import "time"

// --- Invite codes (admin + user) ---

// AdminCreateInviteRequest is the body for an admin-generated invite code.
type AdminCreateInviteRequest struct {
	MaxUses   int        `json:"max_uses"`
	ExpiresAt *time.Time `json:"expires_at"`
	Note      string     `json:"note"`
}

// UserCreateInviteRequest is the body for a user-generated invite code. The
// user's remaining quota is enforced server-side.
type UserCreateInviteRequest struct {
	MaxUses   int        `json:"max_uses"`
	ExpiresAt *time.Time `json:"expires_at"`
	Note      string     `json:"note"`
}

// InviteStatusResponse reports a user's invite usage against their quota.
type InviteStatusResponse struct {
	Used   int `json:"used"`   // successful registrations sponsored
	Issued int `json:"issued"` // total capacity minted (Σ max_uses)
	Quota  int `json:"quota"`  // effective cap
}

// --- Announcements (admin + user) ---

// AnnouncementRequest is the create/update body for an announcement.
type AnnouncementRequest struct {
	Title   string `json:"title" binding:"required"`
	Content string `json:"content"`
	Pinned  bool   `json:"pinned"`
	Active  bool   `json:"active"`
}

// --- Email (admin) ---

// AdminSendEmailRequest is the body for the admin "send email" action.
// Recipients is one of "all" | "active" | "ids" (ids requires UserIDs). When
// CreateAnnouncement is true the same content is persisted as a pinned/active
// announcement so it also shows in the user SPA.
type AdminSendEmailRequest struct {
	Recipients         string   `json:"recipients" binding:"required"`
	UserIDs            []string `json:"user_ids"`
	Subject            string   `json:"subject" binding:"required"`
	Body               string   `json:"body" binding:"required"`
	CreateAnnouncement bool     `json:"create_announcement"`
	Pinned             bool     `json:"pinned"`
}
