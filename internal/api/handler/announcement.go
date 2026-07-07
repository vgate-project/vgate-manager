package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
)

// AdminAnnouncementHandler manages announcements (CRUD) from the admin surface.
type AdminAnnouncementHandler struct {
	svc *service.AnnouncementService
}

func NewAdminAnnouncementHandler(svc *service.AnnouncementService) *AdminAnnouncementHandler {
	return &AdminAnnouncementHandler{svc: svc}
}

// List serves GET /api/v1/admin/announcements — every announcement, paginated.
func (h *AdminAnnouncementHandler) List(c *gin.Context) {
	page, pageSize := ParsePaging(c)
	items, total, err := h.svc.ListForAdmin(page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.Page[model.Announcement]{Items: items, Total: total, Page: page, PageSize: pageSize})
}

// Create serves POST /api/v1/admin/announcements.
func (h *AdminAnnouncementHandler) Create(c *gin.Context) {
	var req dto.AnnouncementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	adminID := c.GetUint("admin_id")
	a, err := h.svc.Create(req.Title, req.Content, req.Pinned, req.Active, adminID)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusCreated, a)
}

// Update serves PUT /api/v1/admin/announcements/:id.
func (h *AdminAnnouncementHandler) Update(c *gin.Context) {
	var req dto.AnnouncementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	a, err := h.svc.Update(c.Param("id"), req.Title, req.Content, req.Pinned, req.Active)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, a)
}

// Delete serves DELETE /api/v1/admin/announcements/:id.
func (h *AdminAnnouncementHandler) Delete(c *gin.Context) {
	if err := h.svc.Delete(c.Param("id")); writeErr(c, err) {
		return
	}
	c.Status(http.StatusNoContent)
}

// UserAnnouncementHandler exposes active announcements to authenticated users.
type UserAnnouncementHandler struct {
	svc *service.AnnouncementService
}

func NewUserAnnouncementHandler(svc *service.AnnouncementService) *UserAnnouncementHandler {
	return &UserAnnouncementHandler{svc: svc}
}

// List serves GET /api/v1/user/announcements — only active, pinned first.
func (h *UserAnnouncementHandler) List(c *gin.Context) {
	items, err := h.svc.ListActive()
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
