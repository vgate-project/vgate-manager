package dto

import (
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/payment"
)

// UserDashboard bundles the data the user SPA home page and its global badges
// need into a single response. It replaces several independent calls the user
// portal previously issued on every navigation (profile, hourly traffic,
// orders, nodes, announcements, unread tickets, telegram status, and the
// pending-order flag).
type UserDashboard struct {
	Profile        *model.User          `json:"profile"`
	HourlyTraffic  []HourlyStat         `json:"hourly_traffic"`
	RecentOrders   Page[model.Order]    `json:"recent_orders"`
	Nodes          []UserNodeView       `json:"nodes"`
	Announcements  []model.Announcement `json:"announcements"`
	UnreadTickets  int64                `json:"unread_tickets"`
	TelegramStatus TelegramStatus       `json:"telegram_status"`
	PendingOrder   *model.Order         `json:"pending_order"`
}

// TelegramStatus mirrors service.TelegramStatus for the dashboard payload so
// the dto package need not import the service package (which would cycle).
type TelegramStatus struct {
	Bound     bool `json:"bound"`
	Notify    bool `json:"telegram_notify"`
	Available bool `json:"available"`
}

// AdminReference bundles the static lookup lists the admin SPA reuses across
// many views and dialogs (users, nodes, plans, traffic packages, payment
// methods) into one response, so the frontend can cache them app-wide instead
// of re-fetching them on every page and dialog open.
type AdminReference struct {
	Users           Page[model.User]       `json:"users"`
	Nodes           Page[model.Node]       `json:"nodes"`
	Plans           []model.Plan           `json:"plans"`
	TrafficPackages []model.TrafficPackage `json:"traffic_packages"`
	PaymentMethods  []payment.ChannelInfo  `json:"payment_methods"`
}
