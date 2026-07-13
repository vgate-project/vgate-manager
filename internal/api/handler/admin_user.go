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
		enabled := strings.EqualFold(v, "true")
		filter.Enabled = &enabled
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
		ExpireAt: req.ExpireAt, QuotaBytes: req.QuotaBytes, QuotaResetEnabled: req.QuotaResetEnabled}
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
