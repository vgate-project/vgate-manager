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
