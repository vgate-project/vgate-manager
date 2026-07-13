package handler

import (
	"net/http"
	"strings"

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

// Create places an order for the authenticated user and returns a payment URL.
// Any user_id in the body is ignored — users can only order for themselves.
func (h *OrderHandler) Create(c *gin.Context) {
	userID := c.GetString("user_id")
	var req dto.CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	order, payURL, err := h.svc.Create(userID, toOrderParams(c, req))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.CreateOrderResponse{Order: order, PayURL: payURL})
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

// PayMine regenerates a payment URL for the caller's own pending order.
func (h *OrderHandler) PayMine(c *gin.Context) {
	order, payURL, err := h.svc.PayMine(c.Param("id"), c.GetString("user_id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.CreateOrderResponse{Order: order, PayURL: payURL})
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
	order, payURL, err := h.svc.AdminCreate(adminID, req.UserID, toOrderParams(c, dto.CreateOrderRequest{
		Kind:             req.Kind,
		PlanID:           req.PlanID,
		PlanPriceID:      req.PlanPriceID,
		TrafficPackageID: req.TrafficPackageID,
		Channel:          req.Channel,
	}))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.CreateOrderResponse{Order: order, PayURL: payURL})
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

// AlipayNotify is the public alipay async-notification endpoint. It must be
// unauthenticated. Alipay expects the literal body "success" (or "failure").
func (h *OrderHandler) AlipayNotify(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		c.String(http.StatusInternalServerError, "failure")
		return
	}
	if err := h.svc.HandleNotify(c.Request.Context(), c.Request.Form); err != nil {
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
		Channel:          channelFromReq(req.Channel, c),
	}
}

// channelFromReq returns the explicit channel if valid, otherwise detects it
// from the User-Agent (mobile → wap, else pc). Channel only chooses the
// redirect URL; it never affects amount or entitlement.
func channelFromReq(channel string, c *gin.Context) string {
	if channel == service.ChannelWap {
		return service.ChannelWap
	}
	if channel == service.ChannelPC {
		return service.ChannelPC
	}
	ua := strings.ToLower(c.Request.UserAgent())
	if strings.Contains(ua, "mobile") || strings.Contains(ua, "android") || strings.Contains(ua, "iphone") {
		return service.ChannelWap
	}
	return service.ChannelPC
}
