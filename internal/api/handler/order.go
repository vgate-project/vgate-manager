package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
)

type OrderHandler struct {
	svc *service.OrderService
}

func NewOrderHandler(svc *service.OrderService) *OrderHandler {
	return &OrderHandler{svc: svc}
}

// Create places an order for the authenticated user and returns a PayDirective
// describing how to collect payment. Any user_id in the body is ignored —
// users can only order for themselves.
func (h *OrderHandler) Create(c *gin.Context) {
	userID := c.GetString("user_id")
	var req dto.CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	order, directive, err := h.svc.Create(userID, toOrderParams(c, req))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.CreateOrderResponse{Order: order, PayURL: directive.URL, PayMode: directive.Kind})
}

// ListMine lists the authenticated user's own orders.
func (h *OrderHandler) ListMine(c *gin.Context) {
	page, pageSize := ParsePaging(c)
	orders, total, err := h.svc.ListMine(c.GetString("user_id"), page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.Page[model.Order]{Items: orders, Total: total, Page: page, PageSize: pageSize})
}

// GetMine returns a single order, enforcing ownership.
func (h *OrderHandler) GetMine(c *gin.Context) {
	order, err := h.svc.Get(c.Param("id"), c.GetString("user_id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, order)
}

// PayMine regenerates a payment directive for the caller's own pending order.
func (h *OrderHandler) PayMine(c *gin.Context) {
	order, directive, err := h.svc.PayMine(c.Param("id"), c.GetString("user_id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.CreateOrderResponse{Order: order, PayURL: directive.URL, PayMode: directive.Kind})
}

// CloseMine lets the caller close their own pending order.
func (h *OrderHandler) CloseMine(c *gin.Context) {
	if err := h.svc.CloseMine(c.Param("id"), c.GetString("user_id")); writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// AdminCreate lets an admin place an order on behalf of any user.
func (h *OrderHandler) AdminCreate(c *gin.Context) {
	adminID := c.GetString("admin_id")
	var req dto.AdminCreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	order, directive, err := h.svc.AdminCreate(adminID, req.UserID, toOrderParams(c, dto.CreateOrderRequest{
		Kind:             req.Kind,
		PlanID:           req.PlanID,
		PlanPriceID:      req.PlanPriceID,
		TrafficPackageID: req.TrafficPackageID,
		Platform:         req.Platform,
	}))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.CreateOrderResponse{Order: order, PayURL: directive.URL, PayMode: directive.Kind})
}

// List lists all orders (admin), with optional filtering/sorting applied
// server-side via query params: search, status, sort_by, order.
func (h *OrderHandler) List(c *gin.Context) {
	page, pageSize := ParsePaging(c)
	filter := service.OrderListFilter{
		Search: c.Query("search"),
		Status: c.Query("status"),
		SortBy: c.Query("sort_by"),
		Order:  c.Query("order"),
	}
	orders, total, err := h.svc.List(filter, page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.Page[model.Order]{Items: orders, Total: total, Page: page, PageSize: pageSize})
}

// Get returns any order by id (admin).
func (h *OrderHandler) Get(c *gin.Context) {
	order, err := h.svc.AdminGet(c.Param("id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, order)
}

// AdminUpdateStatus lets an admin manually set an order's status (paid/closed).
// Marking an order paid applies its purchase effect; closing cancels it.
func (h *OrderHandler) AdminUpdateStatus(c *gin.Context) {
	var req dto.UpdateOrderStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	order, err := h.svc.AdminUpdateStatus(c.Param("id"), req.Status, c.GetString("admin_id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, order)
}

// Notify handles an async payment-gateway notification for the platform named
// in the URL path (e.g. /billing/alipay/notify). It must be unauthenticated.
// The gateway expects the literal body "success" (or "failure").
func (h *OrderHandler) Notify(c *gin.Context) {
	if err := h.svc.Reconcile(c.Request.Context(), c.Param("platform"), c.Request); err != nil {
		c.String(http.StatusInternalServerError, "failure")
		return
	}
	c.String(http.StatusOK, "success")
}

// toOrderParams maps a DTO request into the service's param struct.
func toOrderParams(c *gin.Context, req dto.CreateOrderRequest) service.CreateOrderParams {
	return service.CreateOrderParams{
		Kind:             req.Kind,
		PlanID:           req.PlanID,
		PlanPriceID:      req.PlanPriceID,
		TrafficPackageID: req.TrafficPackageID,
		Platform:         req.Platform,
	}
}
