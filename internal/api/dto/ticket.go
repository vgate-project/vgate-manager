package dto

import "github.com/vgate-project/vgate-manager/internal/model"

// TicketCreateRequest is the body for opening a ticket (user surface).
type TicketCreateRequest struct {
	Subject      string `json:"subject" binding:"required"`
	Content      string `json:"content" binding:"required"`
	Priority     string `json:"priority"`                    // optional, defaults to normal
	NotifyMethod string `json:"notify_method"`                // optional; none|email|telegram; empty defaults to telegram if bound
}

// TicketReplyRequest is the body for appending a message to a ticket.
type TicketReplyRequest struct {
	Content string `json:"content" binding:"required"`
}

// TicketStatusRequest is the body for an admin status change.
type TicketStatusRequest struct {
	Status string `json:"status" binding:"required"`
}

// TicketDetail bundles a ticket with its full message thread.
type TicketDetail struct {
	Ticket   model.Ticket          `json:"ticket"`
	Messages []model.TicketMessage `json:"messages"`
}
