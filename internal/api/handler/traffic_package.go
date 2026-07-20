package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
)

type TrafficPackageHandler struct {
	svc *service.TrafficPackageService
}

func NewTrafficPackageHandler(svc *service.TrafficPackageService) *TrafficPackageHandler {
	return &TrafficPackageHandler{svc: svc}
}

// ListActive lists enabled traffic packages for users.
func (h *TrafficPackageHandler) ListActive(c *gin.Context) {
	pkgs, err := h.svc.List(true)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, pkgs)
}

// ListAll lists all traffic packages (admin, includes disabled).
func (h *TrafficPackageHandler) ListAll(c *gin.Context) {
	pkgs, err := h.svc.List(false)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, pkgs)
}

// Get returns a single traffic package (super admin).
func (h *TrafficPackageHandler) Get(c *gin.Context) {
	pkg, err := h.svc.Get(c.Param("id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, pkg)
}

// Create adds a new traffic package (super admin).
func (h *TrafficPackageHandler) Create(c *gin.Context) {
	var req dto.TrafficPackageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	pkg := &model.TrafficPackage{
		Name:         req.Name,
		DisplayName:  req.DisplayName,
		Price:        req.Price,
		QuotaBytes:   req.QuotaBytes,
		ValidityDays: req.ValidityDays,
		Description:  req.Description,
	}
	pkg.Enabled = true
	if req.Enabled != nil {
		pkg.Enabled = *req.Enabled
	}
	if err := h.svc.Create(pkg); writeErr(c, err) {
		return
	}
	c.JSON(http.StatusCreated, pkg)
}

// Update replaces a traffic package (super admin).
func (h *TrafficPackageHandler) Update(c *gin.Context) {
	pkg, err := h.svc.Get(c.Param("id"))
	if writeErr(c, err) {
		return
	}
	var req dto.TrafficPackageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	pkg.Name = req.Name
	pkg.DisplayName = req.DisplayName
	pkg.Price = req.Price
	pkg.QuotaBytes = req.QuotaBytes
	pkg.ValidityDays = req.ValidityDays
	pkg.Description = req.Description
	if req.Enabled != nil {
		pkg.Enabled = *req.Enabled
	}
	if err := h.svc.Update(pkg); writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, pkg)
}

// Delete removes a traffic package (super admin).
func (h *TrafficPackageHandler) Delete(c *gin.Context) {
	if err := h.svc.Delete(c.Param("id")); writeErr(c, err) {
		return
	}
	c.Status(http.StatusNoContent)
}
