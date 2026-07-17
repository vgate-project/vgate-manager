package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
)

// UserTicketHandler exposes ticket operations to the authenticated user.
// Users only ever see tickets they own.
type UserTicketHandler struct {
	svc *service.TicketService
}

func NewUserTicketHandler(svc *service.TicketService) *UserTicketHandler {
	return &UserTicketHandler{svc: svc}
}

// Create serves POST /api/v1/user/tickets.
func (h *UserTicketHandler) Create(c *gin.Context) {
	var req dto.TicketCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	userID := c.GetString("user_id")
	t, err := h.svc.Create(userID, req.Subject, req.Content, req.Priority, req.NotifyMethod)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusCreated, t)
}

// List serves GET /api/v1/user/tickets.
func (h *UserTicketHandler) List(c *gin.Context) {
	page, pageSize := ParsePaging(c)
	status := c.Query("status")
	userID := c.GetString("user_id")
	items, total, err := h.svc.ListForUser(userID, status, page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.Page[model.Ticket]{Items: items, Total: total, Page: page, PageSize: pageSize})
}

// Get serves GET /api/v1/user/tickets/:id.
func (h *UserTicketHandler) Get(c *gin.Context) {
	userID := c.GetString("user_id")
	t, msgs, err := h.svc.GetForUser(userID, c.Param("id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.TicketDetail{Ticket: *t, Messages: msgs})
}

// Reply serves POST /api/v1/user/tickets/:id/messages.
func (h *UserTicketHandler) Reply(c *gin.Context) {
	var req dto.TicketReplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	userID := c.GetString("user_id")
	t, err := h.svc.AddUserMessage(userID, c.Param("id"), req.Content)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, t)
}

// Close serves POST /api/v1/user/tickets/:id/close (owner only).
func (h *UserTicketHandler) Close(c *gin.Context) {
	userID := c.GetString("user_id")
	t, err := h.svc.Close(userID, c.Param("id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, t)
}

// AdminTicketHandler exposes ticket operations to admins (any role).
type AdminTicketHandler struct {
	svc *service.TicketService
}

func NewAdminTicketHandler(svc *service.TicketService) *AdminTicketHandler {
	return &AdminTicketHandler{svc: svc}
}

// List serves GET /api/v1/admin/tickets.
func (h *AdminTicketHandler) List(c *gin.Context) {
	page, pageSize := ParsePaging(c)
	status := c.Query("status")
	q := c.Query("q")
	items, total, err := h.svc.ListForAdmin(status, q, page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.Page[model.Ticket]{Items: items, Total: total, Page: page, PageSize: pageSize})
}

// Get serves GET /api/v1/admin/tickets/:id.
func (h *AdminTicketHandler) Get(c *gin.Context) {
	t, msgs, err := h.svc.GetForAdmin(c.Param("id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.TicketDetail{Ticket: *t, Messages: msgs})
}

// Reply serves POST /api/v1/admin/tickets/:id/messages (notifies the user).
func (h *AdminTicketHandler) Reply(c *gin.Context) {
	var req dto.TicketReplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	adminID := strconv.FormatUint(uint64(c.GetUint("admin_id")), 10)
	t, err := h.svc.AddAdminMessage(adminID, c.Param("id"), req.Content)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, t)
}

// SetStatus serves PUT /api/v1/admin/tickets/:id/status.
func (h *AdminTicketHandler) SetStatus(c *gin.Context) {
	var req dto.TicketStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	adminID := strconv.FormatUint(uint64(c.GetUint("admin_id")), 10)
	t, err := h.svc.SetStatus(adminID, c.Param("id"), req.Status)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, t)
}
