package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
)

type AdminAdminHandler struct {
	svc *service.AuthService
}

func NewAdminAdminHandler(svc *service.AuthService) *AdminAdminHandler {
	return &AdminAdminHandler{svc: svc}
}

func (h *AdminAdminHandler) List(c *gin.Context) {
	page, pageSize := ParsePaging(c)
	admins, total, err := h.svc.ListAdmins(page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.Page[model.Admin]{Items: admins, Total: total, Page: page, PageSize: pageSize})
}

func (h *AdminAdminHandler) Create(c *gin.Context) {
	var req dto.CreateAdminRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	admin, err := h.svc.CreateAdmin(req.Username, req.Password, req.Role)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusCreated, admin)
}

func (h *AdminAdminHandler) UpdatePassword(c *gin.Context) {
	uid, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req dto.UpdatePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.UpdateAdminPassword(uint(uid), req.Password); writeErr(c, err) {
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *AdminAdminHandler) Get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	admin, err := h.svc.GetAdmin(uint(id))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, admin)
}

func (h *AdminAdminHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req dto.UpdateAdminRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	admin, err := h.svc.UpdateAdmin(uint(id), req.Username, req.Role)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, admin)
}

// Delete removes an admin. The currently authenticated admin cannot delete
// their own account (enforced in the service, but also short-circuited here for
// a clear message); the last remaining super_admin is also protected.
func (h *AdminAdminHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if uint(id) == c.GetUint("admin_id") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot delete your own account"})
		return
	}
	if err := h.svc.DeleteAdmin(uint(id), c.GetUint("admin_id")); writeErr(c, err) {
		return
	}
	c.Status(http.StatusNoContent)
}
