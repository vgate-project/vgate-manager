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
	ID        string    `gorm:"primaryKey;size:36" json:"id"`
	UserID    string    `gorm:"index;size:36;not null" json:"user_id"`
	Subject   string    `gorm:"size:255;not null" json:"subject"`
	Priority  string    `gorm:"size:16;default:'normal';index" json:"priority"`
	Status    string    `gorm:"size:16;default:'open';index" json:"status"`
	// NotifyMethod is the ticket owner's preferred channel for being notified of
	// admin replies / status changes on THIS ticket. Empty/"none" = no notification.
	// "email" / "telegram" select the channel. Defaults to "telegram" when the
	// owner has a linked Telegram chat, else "none".
	NotifyMethod string    `gorm:"size:16;default:''" json:"notify_method"`
	UserEmail    string    `gorm:"-" json:"user_email,omitempty"` // admin display only, not persisted
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// TicketMessage is a single message in a ticket conversation thread.
type TicketMessage struct {
	ID        string    `gorm:"primaryKey;size:36" json:"id"`
	TicketID  string    `gorm:"index;size:36;not null" json:"ticket_id"`
	Sender    string    `gorm:"size:8;not null" json:"sender"` // user | admin
	SenderID  string    `gorm:"size:36" json:"sender_id"`      // user id or admin id
	Content   string    `gorm:"type:text;not null" json:"content"`
	CreatedAt time.Time `json:"created_at"`
}
