package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
)

// UserDashboardHandler serves GET /api/v1/user/dashboard, composing the data
// the user home page and global badges need into one response so the SPA does
// not have to issue several independent calls (and re-issue them on every
// navigation via the layout shell).
type UserDashboardHandler struct {
	userSvc     *service.UserService
	trafficSvc  *service.TrafficService
	orderSvc    *service.OrderService
	annSvc      *service.AnnouncementService
	ticketSvc   *service.TicketService
	telegramSvc *service.TelegramService
}

func NewUserDashboardHandler(
	userSvc *service.UserService,
	trafficSvc *service.TrafficService,
	orderSvc *service.OrderService,
	annSvc *service.AnnouncementService,
	ticketSvc *service.TicketService,
	telegramSvc *service.TelegramService,
) *UserDashboardHandler {
	return &UserDashboardHandler{
		userSvc:     userSvc,
		trafficSvc:  trafficSvc,
		orderSvc:    orderSvc,
		annSvc:      annSvc,
		ticketSvc:   ticketSvc,
		telegramSvc: telegramSvc,
	}
}

// Dashboard serves GET /api/v1/user/dashboard.
func (h *UserDashboardHandler) Dashboard(c *gin.Context) {
	userID := c.GetString("user_id")

	user, err := h.userSvc.Get(userID)
	if writeErr(c, err) {
		return
	}
	hourly, err := h.trafficSvc.HourlyForUser(userID)
	if writeErr(c, err) {
		return
	}
	orders, total, err := h.orderSvc.ListMine(userID, 1, 20)
	if writeErr(c, err) {
		return
	}
	nodes, err := h.userSvc.ListNodesForUser(userID)
	if writeErr(c, err) {
		return
	}
	announcements, err := h.annSvc.ListActive()
	if writeErr(c, err) {
		return
	}
	unread, err := h.ticketSvc.UnreadCountForUser(userID)
	if writeErr(c, err) {
		return
	}
	tg, err := h.telegramSvc.StatusForUser(userID)
	if writeErr(c, err) {
		return
	}

	nodeViews := make([]dto.UserNodeView, 0, len(nodes))
	for _, n := range nodes {
		mult, err := h.userSvc.EffectiveTrafficMultiplier(n)
		if writeErr(c, err) {
			return
		}
		nodeViews = append(nodeViews, dto.UserNodeView{
			ID:                n.ID,
			Name:              n.Name,
			Address:           n.Address,
			Port:              n.Port,
			Level:             n.Level,
			Enabled:           n.Enabled,
			Online:            n.IsOnline(),
			LastSeenAt:        n.LastSeenAt,
			TrafficMultiplier: mult,
		})
	}

	// Surface the first pending order for the global "you have an unpaid
	// order" badge/alert; the SPA no longer needs a separate orders call.
	var pending *model.Order
	for i := range orders {
		if orders[i].Status == model.OrderStatusPending {
			p := orders[i]
			pending = &p
			break
		}
	}

	c.JSON(http.StatusOK, dto.UserDashboard{
		Profile:        user,
		HourlyTraffic:  hourly,
		RecentOrders:   dto.Page[model.Order]{Items: orders, Total: total, Page: 1, PageSize: 20},
		Nodes:          nodeViews,
		Announcements:  announcements,
		UnreadTickets:  unread,
		TelegramStatus: dto.TelegramStatus{Bound: tg.Bound, Notify: tg.Notify, Available: tg.Available},
		PendingOrder:   pending,
	})
}
