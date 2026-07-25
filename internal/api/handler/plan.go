package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
)

type PlanHandler struct {
	svc *service.PlanService
}

func NewPlanHandler(svc *service.PlanService) *PlanHandler {
	return &PlanHandler{svc: svc}
}

// ListActive lists enabled plans (with their enabled prices) for users. When
// the caller owns a disabled plan that allows off-shelf renewal, that plan is
// appended so they can still renew it.
func (h *PlanHandler) ListActive(c *gin.Context) {
	plans, err := h.svc.List(true, c.GetString("user_id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, plans)
}

// ListAll lists all plans (admin, includes disabled and their prices).
func (h *PlanHandler) ListAll(c *gin.Context) {
	plans, err := h.svc.List(false, "")
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, plans)
}

// Get returns a single plan (super admin).
func (h *PlanHandler) Get(c *gin.Context) {
	plan, err := h.svc.Get(c.Param("id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, plan)
}

// Create adds a new plan (super admin). The body carries the product attributes
// plus a list of billing-period prices.
func (h *PlanHandler) Create(c *gin.Context) {
	var req dto.PlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	plan := &model.Plan{
		Name:              req.Name,
		DisplayName:       req.DisplayName,
		QuotaBytes:        req.QuotaBytes,
		Description:       req.Description,
		Level:             req.Level,
		SpeedLimitUpBps:   req.SpeedLimitUpBps,
		SpeedLimitDownBps: req.SpeedLimitDownBps,
		Prices:            toPlanPrices(req.Prices),
	}
	plan.Enabled = true
	if req.Enabled != nil {
		plan.Enabled = *req.Enabled
	}
	if req.AllowRenewOffShelf != nil {
		plan.AllowRenewOffShelf = *req.AllowRenewOffShelf
	}
	if err := h.svc.Create(plan); writeErr(c, err) {
		return
	}
	c.JSON(http.StatusCreated, plan)
}

// Update replaces a plan (super admin), including its price list.
func (h *PlanHandler) Update(c *gin.Context) {
	plan, err := h.svc.Get(c.Param("id"))
	if writeErr(c, err) {
		return
	}
	var req dto.PlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	plan.Name = req.Name
	plan.DisplayName = req.DisplayName
	plan.QuotaBytes = req.QuotaBytes
	plan.Description = req.Description
	plan.Level = req.Level
	plan.SpeedLimitUpBps = req.SpeedLimitUpBps
	plan.SpeedLimitDownBps = req.SpeedLimitDownBps
	plan.Prices = toPlanPrices(req.Prices)
	if req.Enabled != nil {
		plan.Enabled = *req.Enabled
	}
	if req.AllowRenewOffShelf != nil {
		plan.AllowRenewOffShelf = *req.AllowRenewOffShelf
	}
	if err := h.svc.Update(plan); writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, plan)
}

// Delete removes a plan (super admin).
func (h *PlanHandler) Delete(c *gin.Context) {
	if err := h.svc.Delete(c.Param("id")); writeErr(c, err) {
		return
	}
	c.Status(http.StatusNoContent)
}

// toPlanPrices converts DTO price inputs into PlanPriceEntry entries for
// Plan.Prices JSON column. DurationDays is filled from the canonical period
// when omitted; Enabled defaults to true.
func toPlanPrices(in []dto.PlanPriceInput) model.PlanPrices {
	out := make(model.PlanPrices, 0, len(in))
	for _, p := range in {
		enabled := true
		if p.Enabled != nil {
			enabled = *p.Enabled
		}
		dur := p.DurationDays
		if dur <= 0 {
			dur = model.DefaultDurationForPeriod(p.Period)
		}
		out = append(out, model.PlanPriceEntry{
			Period:       p.Period,
			Price:        p.Price,
			DurationDays: dur,
			Sort:         p.Sort,
			Enabled:      enabled,
		})
	}
	return out
}
