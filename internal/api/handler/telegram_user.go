package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/service"
)

// TelegramUserHandler exposes Telegram link management to authenticated
// users. The actual bot logic lives in service.TelegramService; this
// handler is a thin HTTP boundary over it.
type TelegramUserHandler struct {
	tg *service.TelegramService
}

func NewTelegramUserHandler(tg *service.TelegramService) *TelegramUserHandler {
	return &TelegramUserHandler{tg: tg}
}

// Status serves GET /api/v1/user/telegram/status — the caller's link state.
func (h *TelegramUserHandler) Status(c *gin.Context) {
	userID := c.GetString("user_id")
	st, err := h.tg.StatusForUser(userID)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, st)
}

// Bind serves POST /api/v1/user/telegram/bind — issues a one-time bind
// code and returns the deep link the user should open in Telegram.
func (h *TelegramUserHandler) Bind(c *gin.Context) {
	userID := c.GetString("user_id")
	code, deepLink, tgLink, err := h.tg.BindCode(userID)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": code, "deep_link": deepLink, "tg_link": tgLink})
}

// Unbind serves POST /api/v1/user/telegram/unbind — clears the link.
func (h *TelegramUserHandler) Unbind(c *gin.Context) {
	userID := c.GetString("user_id")
	if err := h.tg.UnbindByUser(userID); writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Telegram unlinked"})
}

// SetNotify serves PUT /api/v1/user/telegram/notify — toggles announcement
// opt-in. Body: { "notify": true|false }.
func (h *TelegramUserHandler) SetNotify(c *gin.Context) {
	var req struct {
		Notify bool `json:"notify"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	userID := c.GetString("user_id")
	if err := h.tg.SetNotify(userID, req.Notify); writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "notify updated"})
}
