package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
)

// AdminInviteHandler manages invite codes from the admin surface (no quota).
type AdminInviteHandler struct {
	svc *service.InviteService
}

func NewAdminInviteHandler(svc *service.InviteService) *AdminInviteHandler {
	return &AdminInviteHandler{svc: svc}
}

// List serves GET /api/v1/admin/invites — all codes, paginated, newest first.
func (h *AdminInviteHandler) List(c *gin.Context) {
	page, pageSize := ParsePaging(c)
	codes, total, err := h.svc.ListForAdmin(page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.Page[model.InviteCode]{Items: codes, Total: total, Page: page, PageSize: pageSize})
}

// Create serves POST /api/v1/admin/invites — admin mints a code (no quota).
func (h *AdminInviteHandler) Create(c *gin.Context) {
	var req dto.AdminCreateInviteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	adminID := c.GetUint("admin_id")
	code, err := h.svc.CreateForAdmin(adminID, req.MaxUses, req.ExpiresAt, req.Note)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusCreated, code)
}

// Delete serves DELETE /api/v1/admin/invites/:id — removes any code.
func (h *AdminInviteHandler) Delete(c *gin.Context) {
	if err := h.svc.DeleteAdmin(c.Param("id")); writeErr(c, err) {
		return
	}
	c.Status(http.StatusNoContent)
}

// UserInviteHandler exposes a user's own invite codes and quota.
type UserInviteHandler struct {
	svc *service.InviteService
}

func NewUserInviteHandler(svc *service.InviteService) *UserInviteHandler {
	return &UserInviteHandler{svc: svc}
}

// ListMine serves GET /api/v1/user/invites — codes owned by the caller.
func (h *UserInviteHandler) ListMine(c *gin.Context) {
	codes, err := h.svc.ListForUser(c.GetString("user_id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": codes, "total": len(codes)})
}

// Status serves GET /api/v1/user/invites/status — used/issued/quota.
func (h *UserInviteHandler) Status(c *gin.Context) {
	used, issued, quota, err := h.svc.Status(c.GetString("user_id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.InviteStatusResponse{Used: used, Issued: issued, Quota: quota})
}

// Create serves POST /api/v1/user/invites — mint a code within the quota.
func (h *UserInviteHandler) Create(c *gin.Context) {
	var req dto.UserCreateInviteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	code, err := h.svc.CreateForUser(c.GetString("user_id"), req.MaxUses, req.ExpiresAt, req.Note)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusCreated, code)
}

// Delete serves DELETE /api/v1/user/invites/:id — remove an unused own code.
func (h *UserInviteHandler) Delete(c *gin.Context) {
	err := h.svc.DeleteOwned(c.Param("id"), c.GetString("user_id"))
	if writeErr(c, err) {
		return
	}
	c.Status(http.StatusNoContent)
}
