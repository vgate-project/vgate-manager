package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
)

// AdminReferenceHandler serves GET /api/v1/admin/reference, composing the
// static lookup lists the admin SPA reuses across many views and dialogs into
// one response. The SPA caches it app-wide and re-fetches only after a
// mutation, instead of re-fetching each list on every page and dialog open.
type AdminReferenceHandler struct {
	userSvc  *service.UserService
	nodeSvc  *service.NodeService
	planSvc  *service.PlanService
	pkgSvc   *service.TrafficPackageService
	orderSvc *service.OrderService
}

func NewAdminReferenceHandler(
	userSvc *service.UserService,
	nodeSvc *service.NodeService,
	planSvc *service.PlanService,
	pkgSvc *service.TrafficPackageService,
	orderSvc *service.OrderService,
) *AdminReferenceHandler {
	return &AdminReferenceHandler{
		userSvc:  userSvc,
		nodeSvc:  nodeSvc,
		planSvc:  planSvc,
		pkgSvc:   pkgSvc,
		orderSvc: orderSvc,
	}
}

// Reference serves GET /api/v1/admin/reference.
func (h *AdminReferenceHandler) Reference(c *gin.Context) {
	users, userTotal, err := h.userSvc.List(service.UserListFilter{}, 1, 1000)
	if writeErr(c, err) {
		return
	}
	nodes, nodeTotal, err := h.nodeSvc.List(1, 1000, "")
	if writeErr(c, err) {
		return
	}
	// Admin dialogs need the full catalog (including disabled rows), matching
	// the prior GET /admin/plans and GET /admin/traffic-packages behaviour.
	plans, err := h.planSvc.List(false, "")
	if writeErr(c, err) {
		return
	}
	pkgs, err := h.pkgSvc.List(false)
	if writeErr(c, err) {
		return
	}
	methods := h.orderSvc.ListPaymentMethods()

	c.JSON(http.StatusOK, dto.AdminReference{
		Users:           dto.Page[model.User]{Items: users, Total: userTotal, Page: 1, PageSize: 1000},
		Nodes:           dto.Page[model.Node]{Items: nodes, Total: nodeTotal, Page: 1, PageSize: 1000},
		Plans:           plans,
		TrafficPackages: pkgs,
		PaymentMethods:  methods,
	})
}
