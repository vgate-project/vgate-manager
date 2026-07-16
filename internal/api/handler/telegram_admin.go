package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/service"
)

// AdminTelegramHandler lets an admin broadcast a message to Telegram-linked
// users, optionally also publishing it as a user announcement.
type AdminTelegramHandler struct {
	tg     *service.TelegramService
	annSvc *service.AnnouncementService
}

func NewAdminTelegramHandler(tg *service.TelegramService, annSvc *service.AnnouncementService) *AdminTelegramHandler {
	return &AdminTelegramHandler{tg: tg, annSvc: annSvc}
}

// Broadcast serves POST /api/v1/admin/telegram/broadcast. It delivers Message to
// every linked, opted-in user. When CreateAnnouncement is set, the content is
// also persisted as an announcement (which the announcement service pushes to
// Telegram via its own hook), so it is not broadcast a second time here.
func (h *AdminTelegramHandler) Broadcast(c *gin.Context) {
	var req dto.AdminTelegramBroadcastRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.CreateAnnouncement {
		title := req.Title
		if title == "" {
			title = req.Message
		}
		adminID := c.GetUint("admin_id")
		if _, err := h.annSvc.Create(title, req.Message, req.Pinned, true, adminID); err != nil {
			writeErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"message": "Announcement created and broadcast via Telegram (if enabled).",
		})
		return
	}

	sent, total := h.tg.BroadcastToUsers(req.Message)
	c.JSON(http.StatusOK, gin.H{
		"message": "Telegram broadcast sent",
		"sent":    sent,
		"total":   total,
	})
}
