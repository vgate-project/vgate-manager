package model

import "time"

const (
	TicketStatusOpen       = "open"
	TicketStatusInProgress = "in_progress"
	TicketStatusResolved   = "resolved"
	TicketStatusClosed     = "closed"

	TicketPriorityLow    = "low"
	TicketPriorityNormal = "normal"
	TicketPriorityHigh   = "high"
	TicketPriorityUrgent = "urgent"

	TicketSenderUser  = "user"
	TicketSenderAdmin = "admin"

	// Ticket notify-method values: the ticket owner's preferred channel for
	// being notified of admin replies / status changes on THIS ticket.
	TicketNotifyNone     = "none"
	TicketNotifyEmail    = "email"
	TicketNotifyTelegram = "telegram"
)

// Ticket is a support work-order opened by a user and handled by admins.
type Ticket struct {
	ID       string `gorm:"primaryKey;size:36" json:"id"`
	UserID   string `gorm:"index;size:36;not null" json:"user_id"`
	Subject  string `gorm:"size:255;not null" json:"subject"`
	Priority string `gorm:"size:16;default:'normal';index" json:"priority"`
	Status   string `gorm:"size:16;default:'open';index" json:"status"`
	// NotifyMethod is the ticket owner's preferred channel for being notified of
	// admin replies / status changes on THIS ticket. Empty/"none" = no notification.
	// "email" / "telegram" select the channel. Defaults to "telegram" when the
	// owner has a linked Telegram chat, else "none".
	NotifyMethod string `gorm:"size:16;default:''" json:"notify_method"`
	UserEmail    string `gorm:"-" json:"user_email,omitempty"` // admin display only, not persisted
	// LastSender is the role of the author of the most recent message on the
	// ticket (model.TicketSenderUser | model.TicketSenderAdmin). It is
	// denormalized so unread detection can tell, without a sub-query, which
	// side spoke last. Not exposed in JSON.
	LastSender string    `gorm:"size:8;default:''" json:"-"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TicketReadState records, per recipient, when they last opened a ticket. It
// drives the unread-dot in the frontends: a ticket counts as unread for a
// recipient while its last activity is newer than their LastReadAt and the
// last speaker was the other side. Recipient is "u:<userID>" for users, or
// "admin" for a single global state shared by all admins.
type TicketReadState struct {
	TicketID   string    `gorm:"primaryKey;size:36" json:"ticket_id"`
	Recipient  string    `gorm:"primaryKey;size:64" json:"recipient"`
	LastReadAt time.Time `json:"last_read_at"`
}

// TableName pins the read-state table name (GORM would otherwise pluralize).
func (TicketReadState) TableName() string { return "ticket_read_states" }

// TicketMessage is a single message in a ticket conversation thread.
type TicketMessage struct {
	ID        string    `gorm:"primaryKey;size:36" json:"id"`
	TicketID  string    `gorm:"index;size:36;not null" json:"ticket_id"`
	Sender    string    `gorm:"size:8;not null" json:"sender"` // user | admin
	SenderID  string    `gorm:"size:36" json:"sender_id"`      // user id or admin id
	Content   string    `gorm:"type:text;not null" json:"content"`
	CreatedAt time.Time `json:"created_at"`
}
