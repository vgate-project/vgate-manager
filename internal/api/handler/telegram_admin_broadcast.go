package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/service"
)

// AdminTelegramHandler exposes Telegram admin broadcast controls through the
// admin REST API. The bot logic lives in service.TelegramService / the
// announcement service; this handler is a thin HTTP boundary over them.
type AdminTelegramHandler struct {
	tg     *service.TelegramService
	annSvc *service.AnnouncementService
}

func NewAdminTelegramHandler(tg *service.TelegramService, annSvc *service.AnnouncementService) *AdminTelegramHandler {
	return &AdminTelegramHandler{tg: tg, annSvc: annSvc}
}

// BroadcastRequest is the body for POST /api/v1/admin/telegram/broadcast.
type BroadcastRequest struct {
	// Text is the message delivered to every linked, opted-in user.
	Text string `json:"text"`
	// Announcement, when true, also creates an in-app announcement from the
	// same text (title is derived) so it shows in the user portal.
	Announcement bool `json:"announcement"`
}

// Broadcast serves POST /api/v1/admin/telegram/broadcast — delivers a
// message to all linked users via Telegram and, optionally, persists it as an
// announcement.
func (h *AdminTelegramHandler) Broadcast(c *gin.Context) {
	var req BroadcastRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Text == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "text is required"})
		return
	}
	if req.Announcement {
		if _, err := h.annSvc.Create(req.Text, req.Text, false, true, c.GetUint("admin_id")); writeErr(c, err) {
			return
		}
	}
	h.tg.BroadcastToUsers(req.Text)
	c.JSON(http.StatusOK, gin.H{"message": "broadcast queued"})
}
