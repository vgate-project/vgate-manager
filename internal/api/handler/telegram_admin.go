package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/service"
)

// TelegramAdminHandler exposes Telegram link management to authenticated
// admins (so they can link their own chat and reply to tickets from the bot).
// The bot logic lives in service.TelegramService; this handler is a thin HTTP
// boundary over it, mirroring TelegramUserHandler for end users.
type TelegramAdminHandler struct {
	tg *service.TelegramService
}

func NewTelegramAdminHandler(tg *service.TelegramService) *TelegramAdminHandler {
	return &TelegramAdminHandler{tg: tg}
}

// Status serves GET /api/v1/admin/me/telegram/status — the admin's link state.
func (h *TelegramAdminHandler) Status(c *gin.Context) {
	adminID := c.GetUint("admin_id")
	st, err := h.tg.StatusForAdmin(adminID)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, st)
}

// Bind serves POST /api/v1/admin/me/telegram/bind — issues a one-time bind
// code and returns the deep link the admin should open in Telegram.
func (h *TelegramAdminHandler) Bind(c *gin.Context) {
	adminID := c.GetUint("admin_id")
	code, deepLink, tgLink, err := h.tg.AdminBindCode(adminID)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": code, "deep_link": deepLink, "tg_link": tgLink})
}

// Unbind serves POST /api/v1/admin/me/telegram/unbind — clears the link.
func (h *TelegramAdminHandler) Unbind(c *gin.Context) {
	adminID := c.GetUint("admin_id")
	if err := h.tg.AdminUnbindByID(adminID); writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Telegram unlinked"})
}
