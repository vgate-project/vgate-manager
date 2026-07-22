package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
)

type AdminUserHandler struct {
	svc *service.UserService
}

func NewAdminUserHandler(svc *service.UserService) *AdminUserHandler {
	return &AdminUserHandler{svc: svc}
}

func (h *AdminUserHandler) List(c *gin.Context) {
	page, pageSize := ParsePaging(c)
	filter := service.UserListFilter{
		Search: c.Query("search"),
		SortBy: c.Query("sort_by"),
		Order:  c.Query("order"),
	}
	if v := c.Query("enabled"); v != "" {
		filter.Enabled = new(strings.EqualFold(v, "true"))
	}
	users, total, err := h.svc.List(filter, page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.Page[model.User]{Items: users, Total: total, Page: page, PageSize: pageSize})
}

func (h *AdminUserHandler) Create(c *gin.Context) {
	var req dto.UserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	user := &model.User{Email: req.Email, Username: req.Username, Level: req.Level,
		ExpireAt: req.ExpireAt, QuotaBytes: req.QuotaBytes, QuotaResetEnabled: req.QuotaResetEnabled,
		SpeedLimitUpBps: req.SpeedLimitUpBps, SpeedLimitDownBps: req.SpeedLimitDownBps}
	// Admin-created accounts are trusted as verified (email_verified is the
	// source of truth for "can use the service"); the user has no self-service
	// verification flow.
	user.EmailVerified = true
	if req.MaxInvites != nil {
		user.MaxInvites = *req.MaxInvites
	}
	if req.Enabled != nil {
		user.Enabled = *req.Enabled
	} else {
		user.Enabled = true
	}
	// Password is intentionally not accepted here — use SetPassword endpoint.
	if err := h.svc.Create(user, ""); err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, dto.UserWithSubToken{User: user, SubToken: user.SubToken})
}

func (h *AdminUserHandler) Get(c *gin.Context) {
	user, err := h.svc.Get(c.Param("id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, user)
}

func (h *AdminUserHandler) Update(c *gin.Context) {
	user, err := h.svc.Get(c.Param("id"))
	if writeErr(c, err) {
		return
	}
	var req dto.UserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	user.Email = req.Email
	user.Username = req.Username
	user.Level = req.Level
	user.ExpireAt = req.ExpireAt
	user.QuotaBytes = req.QuotaBytes
	user.QuotaResetEnabled = req.QuotaResetEnabled
	user.SpeedLimitUpBps = req.SpeedLimitUpBps
	user.SpeedLimitDownBps = req.SpeedLimitDownBps
	if req.MaxInvites != nil {
		user.MaxInvites = *req.MaxInvites
	}
	if req.Enabled != nil {
		user.Enabled = *req.Enabled
	} else {
		user.Enabled = true
	}
	if err := h.svc.Update(user); err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, user)
}

func (h *AdminUserHandler) Delete(c *gin.Context) {
	if err := h.svc.Delete(c.Param("id")); writeErr(c, err) {
		return
	}
	c.Status(http.StatusNoContent)
}

// PreviewZombies counts how many users match the supplied zombie criteria.
// It is a read-only probe so an admin can see the blast radius before deleting.
func (h *AdminUserHandler) PreviewZombies(c *gin.Context) {
	var req dto.ZombieFilterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	count, err := h.svc.CountZombies(service.ZombieFilter{
		NeverUsedProxy:    req.NeverUsedProxy,
		EmailUnverified:   req.EmailUnverified,
		NoPaidOrders:      req.NoPaidOrders,
		InactiveDays:      req.InactiveDays,
		MinAccountDays:    req.MinAccountDays,
		ProtectActiveSubs: req.ProtectActiveSubs,
	})
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": count})
}

// CleanupZombies deletes every user matching the supplied zombie criteria. At
// least one criterion is required so a misconfigured request cannot wipe all
// users; the operation is super-admin only (routed under superAuth).
func (h *AdminUserHandler) CleanupZombies(c *gin.Context) {
	var req dto.ZombieFilterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !req.NeverUsedProxy && !req.EmailUnverified && !req.NoPaidOrders && req.InactiveDays <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one cleanup criterion is required"})
		return
	}
	deleted, err := h.svc.DeleteZombies(service.ZombieFilter{
		NeverUsedProxy:    req.NeverUsedProxy,
		EmailUnverified:   req.EmailUnverified,
		NoPaidOrders:      req.NoPaidOrders,
		InactiveDays:      req.InactiveDays,
		MinAccountDays:    req.MinAccountDays,
		ProtectActiveSubs: req.ProtectActiveSubs,
	})
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
}

func (h *AdminUserHandler) RegenerateSubToken(c *gin.Context) {
	tok, err := h.svc.RegenerateSubToken(c.Param("id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"sub_token": tok})
}

// RegenerateCredential rotates a user's VLESS credential (admin action). The
// primary key is untouched; a leaked credential is revoked immediately and the
// client must re-pull its subscription to pick up the new UUID.
func (h *AdminUserHandler) RegenerateCredential(c *gin.Context) {
	cred, err := h.svc.RegenerateCredential(c.Param("id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"credential": cred})
}

func (h *AdminUserHandler) SetPassword(c *gin.Context) {
	var req dto.SetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.SetPassword(c.Param("id"), req.Password); writeErr(c, err) {
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *AdminUserHandler) ListNodes(c *gin.Context) {
	nodes, err := h.svc.ListNodesForUser(c.Param("id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, nodes)
}

func (h *AdminUserHandler) SetNodes(c *gin.Context) {
	var req dto.SetUserNodesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.SetUserNodes(c.Param("id"), req.NodeIDs); writeErr(c, err) {
		return
	}
	c.Status(http.StatusNoContent)
}

// ListUsersForNode serves GET /admin/nodes/:id/users (registered in admin_node routes).
func (h *AdminUserHandler) ListUsersForNode(c *gin.Context) {
	page, pageSize := ParsePaging(c)
	users, total, err := h.svc.ListUsersForNode(c.Param("id"), page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.Page[model.User]{Items: users, Total: total, Page: page, PageSize: pageSize})
}
