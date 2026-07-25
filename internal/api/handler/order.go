package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
)

type OrderHandler struct {
	svc        *service.OrderService
	balanceSvc *service.BalanceService
}

func NewOrderHandler(svc *service.OrderService, balanceSvc *service.BalanceService) *OrderHandler {
	return &OrderHandler{svc: svc, balanceSvc: balanceSvc}
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
	c.JSON(http.StatusOK, dto.CreateOrderResponse{Order: order, PayURL: directive.URL, PayMode: directive.Kind, Paid: order.Status == model.OrderStatusPaid, WalletUsedCents: directive.WalletUsedCents, WalletRemainingCents: directive.WalletRemainingCents})
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
	c.JSON(http.StatusOK, dto.CreateOrderResponse{Order: order, PayURL: directive.URL, PayMode: directive.Kind, Paid: order.Status == model.OrderStatusPaid, WalletUsedCents: directive.WalletUsedCents, WalletRemainingCents: directive.WalletRemainingCents})
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
	c.JSON(http.StatusOK, dto.CreateOrderResponse{Order: order, PayURL: directive.URL, PayMode: directive.Kind, Paid: order.Status == model.OrderStatusPaid, WalletUsedCents: directive.WalletUsedCents, WalletRemainingCents: directive.WalletRemainingCents})
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

// PaymentMethods lists the admin-enabled, configured payment channels so the
// user portal can render a payment-method picker.
func (h *OrderHandler) PaymentMethods(c *gin.Context) {
	c.JSON(http.StatusOK, h.svc.ListPaymentMethods())
}

// AdminPaymentMethods is the admin counterpart of PaymentMethods.
func (h *OrderHandler) AdminPaymentMethods(c *gin.Context) {
	c.JSON(http.StatusOK, h.svc.ListPaymentMethods())
}

// appleVerifier is the subset of the apple provider used to verify an App Store
// transaction JWS. Declared locally to avoid a hard import of the concrete
// provider package.
type appleVerifier interface {
	VerifyTransaction(jws string) (originalTxnID, productID string, ok bool, err error)
}

// AppleVerify completes an Apple IAP purchase: the native app posts the signed
// transaction JWS it received from StoreKit; the backend verifies the signature
// and, on success, marks the order paid and grants entitlement.
func (h *OrderHandler) AppleVerify(c *gin.Context) {
	userID := c.GetString("user_id")
	orderID := c.Param("id")

	var req dto.AppleVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Load and ownership-check the order before touching any provider.
	var order model.Order
	if err := h.svc.DB().Where("id = ? AND user_id = ?", orderID, userID).First(&order).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}
	if order.Platform != model.OrderPlatformApple {
		c.JSON(http.StatusBadRequest, gin.H{"error": "order is not an Apple IAP order"})
		return
	}
	if order.Status != model.OrderStatusPending {
		c.JSON(http.StatusConflict, gin.H{"error": "order is not pending", "status": order.Status})
		return
	}

	prov, err := h.svc.Payments().Get(model.OrderPlatformApple)
	if err != nil {
		writeErr(c, err)
		return
	}
	av, ok := prov.(appleVerifier)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "apple provider does not support verification"})
		return
	}
	originalTxnID, _, verified, err := av.VerifyTransaction(req.Transaction)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to verify apple transaction: " + err.Error()})
		return
	}
	if !verified || originalTxnID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "apple transaction is invalid"})
		return
	}

	if err := h.svc.MarkApplePaid(order.OutTradeNo, originalTxnID); err != nil {
		writeErr(c, err)
		return
	}
	var updated model.Order
	_ = h.svc.DB().Where("id = ?", orderID).First(&updated).Error
	c.JSON(http.StatusOK, gin.H{"ok": true, "order": updated})
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

// ChangePlan serves POST /api/v1/user/change-plan — switch the caller to a
// different plan, crediting the old plan's remaining value to the wallet and
// charging (or refunding) the net difference. All changes take effect
// immediately.
func (h *OrderHandler) ChangePlan(c *gin.Context) {
	userID := c.GetString("user_id")
	var req dto.ChangePlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	res, err := h.svc.ChangePlan(userID, req.PlanID, req.PlanPriceID, req.Platform)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, res)
}

// ChangePlanPreview serves GET /api/v1/user/change-plan/preview — compute the
// proration numbers (remaining credit + net charge) for a prospective plan
// change without mutating anything. Query params: plan_id, plan_price_id.
func (h *OrderHandler) ChangePlanPreview(c *gin.Context) {
	userID := c.GetString("user_id")
	planID := c.Query("plan_id")
	planPriceID := c.Query("plan_price_id")
	if planID == "" || planPriceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "plan_id and plan_price_id are required"})
		return
	}
	res, err := h.svc.PreviewChangePlan(userID, planID, planPriceID)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, res)
}

// GetBalance serves GET /api/v1/user/balance — the caller's wallet balance and
// ledger history.
func (h *OrderHandler) GetBalance(c *gin.Context) {
	userID := c.GetString("user_id")
	page, pageSize := ParsePaging(c)
	balance, err := h.balanceSvc.GetBalance(userID)
	if writeErr(c, err) {
		return
	}
	ledger, total, err := h.balanceSvc.ListLedger(userID, page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.BalanceResponse{
		BalanceCents: balance,
		Ledger:       ledger,
		Total:        total,
		Page:         page,
		PageSize:     pageSize,
	})
}

// AdminGetBalance serves GET /api/v1/admin/users/:id/balance.
func (h *OrderHandler) AdminGetBalance(c *gin.Context) {
	id := c.Param("id")
	page, pageSize := ParsePaging(c)
	balance, err := h.balanceSvc.GetBalance(id)
	if writeErr(c, err) {
		return
	}
	ledger, total, err := h.balanceSvc.ListLedger(id, page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.BalanceResponse{
		BalanceCents: balance,
		Ledger:       ledger,
		Total:        total,
		Page:         page,
		PageSize:     pageSize,
	})
}

// AdminAdjustBalance serves POST /api/v1/admin/users/:id/balance — grant or
// deduct from a user's wallet.
func (h *OrderHandler) AdminAdjustBalance(c *gin.Context) {
	id := c.Param("id")
	var req dto.AdminAdjustBalanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.balanceSvc.AdminAdjust(id, req.DeltaCents, req.Remark); writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
