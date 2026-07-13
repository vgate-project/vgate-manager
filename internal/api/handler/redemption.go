package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
)

// AdminRedemptionHandler manages redemption codes from the admin surface
// (batch-generate, list, delete, view records). No per-admin quota applies.
type AdminRedemptionHandler struct {
	svc *service.RedemptionService
}

func NewAdminRedemptionHandler(svc *service.RedemptionService) *AdminRedemptionHandler {
	return &AdminRedemptionHandler{svc: svc}
}

// List serves GET /api/v1/admin/redemption-codes — all codes, paginated.
func (h *AdminRedemptionHandler) List(c *gin.Context) {
	page, pageSize := ParsePaging(c)
	codes, total, err := h.svc.ListForAdmin(page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.Page[model.RedemptionCode]{Items: codes, Total: total, Page: page, PageSize: pageSize})
}

// Generate serves POST /api/v1/admin/redemption-codes — mints Count codes.
func (h *AdminRedemptionHandler) Generate(c *gin.Context) {
	var req dto.AdminGenerateRedemptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	adminID := c.GetUint("admin_id")
	codes, err := h.svc.BatchGenerate(adminID, req)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusCreated, codes)
}

// Records serves GET /api/v1/admin/redemption-codes/:id/records — who redeemed.
func (h *AdminRedemptionHandler) Records(c *gin.Context) {
	recs, err := h.svc.ListRecordsForAdmin(c.Param("id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": recs, "total": len(recs)})
}

// Delete serves DELETE /api/v1/admin/redemption-codes/:id — removes a code.
func (h *AdminRedemptionHandler) Delete(c *gin.Context) {
	if err := h.svc.DeleteAdmin(c.Param("id")); writeErr(c, err) {
		return
	}
	c.Status(http.StatusNoContent)
}

// UserRedemptionHandler lets an authenticated user redeem a code and view
// their own redemption history.
type UserRedemptionHandler struct {
	svc *service.RedemptionService
}

func NewUserRedemptionHandler(svc *service.RedemptionService) *UserRedemptionHandler {
	return &UserRedemptionHandler{svc: svc}
}

// Redeem serves POST /api/v1/user/redemption-codes/redeem — apply a code's
// benefit to the caller.
func (h *UserRedemptionHandler) Redeem(c *gin.Context) {
	var req dto.RedeemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rec, message, err := h.svc.Redeem(c.GetString("user_id"), req.Code)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.RedeemResponse{Record: *rec, Type: rec.Type, Message: message})
}

// Records serves GET /api/v1/user/redemption-codes/records — caller's history.
func (h *UserRedemptionHandler) Records(c *gin.Context) {
	recs, err := h.svc.ListRecordsForUser(c.GetString("user_id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": recs, "total": len(recs)})
}
