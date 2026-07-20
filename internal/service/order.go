package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/payment"
	"github.com/vgate-project/vgate-manager/internal/util"
)

const (
	// ChannelPC is the desktop website payment (alipay.trade.page.pay).
	ChannelPC = "pc"
	// ChannelWap is the mobile website payment (alipay.trade.wap.pay).
	ChannelWap = "wap"

	// orderTimeout is how long an order stays payable before the cron closes it.
	orderTimeout = 30 * time.Minute
)

var (
	// ErrPendingOrderExists is returned when Create would leave a user with
	// more than one open (pending) order.
	ErrPendingOrderExists = errors.New("a pending order already exists")
	// ErrOrderNotPending is returned when CloseMine/PayMine is called on an
	// order that is not in the pending state.
	ErrOrderNotPending = errors.New("order is not pending")
	// ErrInvalidOrderKind is returned when Create is given an unknown kind.
	ErrInvalidOrderKind = errors.New("unknown order kind")
	// ErrEmailNotVerified is returned when a self-service purchase is attempted
	// by an account that has not yet verified its email. Admins placing orders
	// on a user's behalf are exempt.
	ErrEmailNotVerified = errors.New("email not verified")
)

// CreateOrderParams describes what a user wants to buy.
type CreateOrderParams struct {
	Kind             string // model.OrderKindPlan | model.OrderKindTraffic
	PlanID           string // required when Kind=plan
	PlanPriceID      string // required when Kind=plan
	TrafficPackageID string // required when Kind=traffic
	Channel          string // optional: "pc" | "wap" | "" (auto by UA)
	Platform         string // optional: payment gateway; defaults to alipay
}

// OrderService handles plan/traffic purchases, payment-url generation,
// async notify reconciliation, and expired-order cleanup.
type OrderService struct {
	db          *gorm.DB
	sys         *SystemConfigService
	planSvc     *PlanService
	trafficSvc  *TrafficPackageService
	payments    *payment.Registry
	telegramSvc *TelegramService
}

func NewOrderService(db *gorm.DB, sys *SystemConfigService, payments *payment.Registry) *OrderService {
	return &OrderService{
		db:         db,
		sys:        sys,
		planSvc:    NewPlanService(db),
		trafficSvc: NewTrafficPackageService(db),
		payments:   payments,
	}
}

// SetTelegramService wires the Telegram bot service so a paid order can emit
// an admin alert (when the admin enabled the order_paid alert).
func (s *OrderService) SetTelegramService(svc *TelegramService) {
	s.telegramSvc = svc
}

// resolvePaymentSubject picks the product name shown on the payment gateway,
// with the following precedence:
//  1. the per-product DisplayName (set on the plan or traffic package);
//  2. the global template payment.product_name_template (rendered with
//     placeholders);
//  3. the built-in default subject.
func (s *OrderService) resolvePaymentSubject(kind, productName, period string, amountCents int64, displayName string) (string, error) {
	if displayName != "" {
		return displayName, nil
	}
	if tmpl, err := s.sys.Get(CfgKeyPaymentProductName); err == nil && tmpl != "" {
		return renderProductTemplate(tmpl, productName, period, amountCents), nil
	}
	switch kind {
	case model.OrderKindTraffic:
		return productName, nil
	case model.OrderKindReset:
		return productName + " traffic reset", nil
	default: // plan
		return period + " plan", nil
	}
}

// renderProductTemplate substitutes the supported placeholders in the global
// product-name template. {plan} = product name, {period} = billing period
// (empty for traffic/reset), {amount} = order amount in yuan (2 decimals).
func renderProductTemplate(tmpl, productName, period string, amountCents int64) string {
	amount := strconv.FormatFloat(float64(amountCents)/100.0, 'f', 2, 64)
	return strings.NewReplacer(
		"{plan}", productName,
		"{period}", period,
		"{amount}", amount,
	).Replace(tmpl)
}

// Create builds an order for the given user and returns a PayDirective telling
// the frontend how to collect payment. The amount is taken from the
// authoritative source (plan price or traffic package); any client-supplied
// amount is ignored.
func (s *OrderService) Create(userID string, p CreateOrderParams) (*model.Order, *payment.PayDirective, error) {
	return s.createFor(userID, p, false)
}

// AdminCreate is like Create but lets an admin place an order on behalf of any
// user. adminID is kept for audit only; isAdmin=true relaxes the reset
// ownership check (an admin intentionally acts for the user).
func (s *OrderService) AdminCreate(adminID, targetUserID string, p CreateOrderParams) (*model.Order, *payment.PayDirective, error) {
	return s.createFor(targetUserID, p, true)
}

func (s *OrderService) createFor(userID string, p CreateOrderParams, isAdmin bool) (*model.Order, *payment.PayDirective, error) {
	// Disallow a second open order: a user may only have one pending order.
	var pending int64
	s.db.Model(&model.Order{}).
		Where("user_id = ? AND status = ?", userID, model.OrderStatusPending).
		Count(&pending)
	if pending > 0 {
		return nil, nil, ErrPendingOrderExists
	}

	// Self-service purchases require a verified email; admins placing an order
	// on a user's behalf (isAdmin) are exempt. Traffic itself is also gated at
	// the node (server_api.go filters on email_verified), so an unverified
	// account can log in and manage its profile but cannot buy or consume.
	var user model.User
	if err := s.db.First(&user, "id = ?", userID).Error; err != nil {
		return nil, nil, err
	}
	if !isAdmin && !user.EmailVerified {
		return nil, nil, ErrEmailNotVerified
	}

	platform := p.Platform
	if platform == "" {
		platform = model.OrderPlatformAlipay
	}
	// The Channel (pc/wap) only selects the alipay redirect style; other
	// gateways ignore it, so we coerce it to pc/wap only for alipay.
	if platform == model.OrderPlatformAlipay {
		if p.Channel != ChannelWap {
			p.Channel = ChannelPC
		}
	}

	order := &model.Order{
		ID:         util.NewOrderID(),
		UserID:     userID,
		Kind:       p.Kind,
		Status:     model.OrderStatusPending,
		Platform:   platform,
		OutTradeNo: util.RandomToken(16),
		Channel:    p.Channel,
	}
	now := time.Now()
	order.ExpiredAt = new(now.Add(orderTimeout))

	var subject string

	switch p.Kind {
	case model.OrderKindPlan:
		price, err := s.planSvc.loadEnabledPlanPrice(p.PlanID, p.PlanPriceID)
		if err != nil {
			return nil, nil, err
		}
		order.PlanID = price.PlanID
		order.PlanPriceID = price.ID
		order.Period = price.Period
		order.DurationDays = price.DurationDays
		order.Amount = price.Price
		plan, err := s.planSvc.Get(price.PlanID)
		if err != nil {
			return nil, nil, err
		}
		subject, err = s.resolvePaymentSubject(model.OrderKindPlan, plan.Name, price.Period, order.Amount, plan.DisplayName)
		if err != nil {
			return nil, nil, err
		}
	case model.OrderKindTraffic:
		pkg, err := s.trafficSvc.loadEnabled(p.TrafficPackageID)
		if err != nil {
			return nil, nil, err
		}
		order.TrafficPackageID = pkg.ID
		order.ValidityDays = pkg.ValidityDays
		order.Amount = pkg.Price
		subject, err = s.resolvePaymentSubject(model.OrderKindTraffic, pkg.Name, "", order.Amount, pkg.DisplayName)
		if err != nil {
			return nil, nil, err
		}
	case model.OrderKindReset:
		plan, err := s.planSvc.loadEnabledPlan(p.PlanID)
		if err != nil {
			return nil, nil, err
		}
		if !plan.ResetEnabled {
			return nil, nil, errors.New("plan has no traffic reset package")
		}
		// Self-service reset requires the user's active product to be this plan.
		// Admins creating on a user's behalf skip this ownership check.
		if !isAdmin {
			if user.CurrentProductID != plan.ID {
				return nil, nil, errors.New("traffic reset is only available for your active plan")
			}
		}
		order.PlanID = plan.ID
		order.Amount = plan.ResetPrice
		subject, err = s.resolvePaymentSubject(model.OrderKindReset, plan.Name, "", order.Amount, plan.DisplayName)
		if err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, ErrInvalidOrderKind
	}

	if err := s.db.Create(order).Error; err != nil {
		return nil, nil, err
	}

	prov, err := s.payments.Get(order.Platform)
	if err != nil {
		return nil, nil, err
	}
	directive, err := prov.PayURL(order, subject)
	if err != nil {
		return nil, nil, err
	}
	return order, directive, nil
}

// Reconcile handles an async payment-gateway notification for platform. It
// verifies the signature via the platform's Provider and, for a successful
// payment, marks the matching order paid and applies its effect. Returning a
// non-nil error tells the caller to respond "failure" so the gateway retries.
func (s *OrderService) Reconcile(ctx context.Context, platform string, r *http.Request) error {
	prov, err := s.payments.Get(platform)
	if err != nil {
		return err
	}
	outTradeNo, tradeNo, paid, err := prov.VerifyNotify(ctx, r)
	if err != nil {
		return err
	}
	if !paid {
		return nil
	}
	if err := s.markPaid(outTradeNo, tradeNo, platform); err != nil {
		return err
	}
	// Best-effort admin alert once the payment is applied.
	var o model.Order
	if err := s.db.Where("out_trade_no = ?", outTradeNo).First(&o).Error; err == nil {
		s.alertOrderPaid(&o)
	}
	return nil
}

// markPaid flips the pending order identified by outTradeNo to paid
// (idempotently) and applies its purchase effect inside a single transaction.
func (s *OrderService) markPaid(outTradeNo, tradeNo, platform string) error {
	now := time.Now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Idempotent guard: only the writer that flips pending→paid continues.
		// SQLite-safe (no SELECT ... FOR UPDATE); concurrent notifies that
		// lose the race see RowsAffected==0 and bail out without re-applying.
		res := tx.Model(&model.Order{}).
			Where("out_trade_no = ? AND status = ?", outTradeNo, model.OrderStatusPending).
			Updates(map[string]any{
				"status":   model.OrderStatusPaid,
				"trade_no": tradeNo,
				"platform": platform,
				"paid_at":  &now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}

		var order model.Order
		if err := tx.Where("out_trade_no = ?", outTradeNo).First(&order).Error; err != nil {
			return err
		}
		var user model.User
		if err := tx.Where("id = ?", order.UserID).First(&user).Error; err != nil {
			return err
		}

		switch order.Kind {
		case model.OrderKindTraffic:
			var pkg model.TrafficPackage
			if err := tx.Where("id = ?", order.TrafficPackageID).First(&pkg).Error; err != nil {
				return err
			}
			return applyTrafficEffect(tx, &user, &pkg, order.ValidityDays)
		case model.OrderKindReset:
			var plan model.Plan
			if err := tx.Where("id = ?", order.PlanID).First(&plan).Error; err != nil {
				return err
			}
			return applyResetEffect(tx, &user, &plan)
		default: // plan
			var plan model.Plan
			if err := tx.Where("id = ?", order.PlanID).First(&plan).Error; err != nil {
				return err
			}
			return applyPlanEffect(tx, &user, &plan, order.DurationDays)
		}
	})
}

// alertOrderPaid emits the admin "order paid" alert outside the transaction
// once the purchase effect has been applied. It is best-effort.
func (s *OrderService) alertOrderPaid(order *model.Order) {
	if s.telegramSvc == nil {
		return
	}
	s.telegramSvc.NotifyAdminEvent(CfgKeyAlertOrderPaid,
		fmt.Sprintf("Order paid: user %s, amount %d (%s)", order.UserID, order.Amount, order.Kind))
}

// applyPlanEffect extends the user's expiry from the later of now/existing
// expiry, sets the user's quota to the plan's quota (replacing any prior
// quota, not adding to it), resets the cumulative used traffic (UpTotal/
// DownTotal) so the user starts the new plan with a full quota, and sets the
// user's level to the plan's level (a subscription replaces the level). Called
// inside a transaction.
func applyPlanEffect(tx *gorm.DB, user *model.User, plan *model.Plan, durationDays int) error {
	now := time.Now()
	base := now
	if user.ExpireAt != nil && user.ExpireAt.After(now) {
		base = *user.ExpireAt
	}
	user.ExpireAt = new(base.AddDate(0, 0, durationDays))
	user.QuotaBytes = plan.QuotaBytes
	user.UpTotal = 0
	user.DownTotal = 0
	user.LastResetAt = &now
	user.Level = plan.Level
	user.SpeedLimitUpBps = plan.SpeedLimitUpBps
	user.SpeedLimitDownBps = plan.SpeedLimitDownBps
	user.CurrentProductID = plan.ID
	user.CurrentProductKind = model.OrderKindPlan
	return tx.Save(user).Error
}

// applyTrafficEffect adds the package's quota. When validityDays > 0 it also
// extends the user's ExpireAt so the traffic is usable for that window; when 0
// the quota is added with no extra expiry (existing ExpireAt still gates use).
// Buying a traffic package opts the user OUT of the global monthly reset
// (quota_reset_enabled = false): a package is a one-time add-on consumed until
// exhausted or expiry, and must NOT be refreshed by the monthly reset.
func applyTrafficEffect(tx *gorm.DB, user *model.User, pkg *model.TrafficPackage, validityDays int) error {
	user.QuotaBytes += pkg.QuotaBytes
	user.QuotaResetEnabled = false
	user.CurrentProductID = pkg.ID
	user.CurrentProductKind = model.OrderKindTraffic
	if validityDays > 0 {
		now := time.Now()
		base := now
		if user.ExpireAt != nil && user.ExpireAt.After(now) {
			base = *user.ExpireAt
		}
		user.ExpireAt = new(base.AddDate(0, 0, validityDays))
	}
	return tx.Save(user).Error
}

// applyResetEffect replenishes the user's plan quota by zeroing the used
// traffic counters (up_total/down_total) and stamping last_reset_at. It does
// NOT change quota_bytes, level, or expire_at — a reset package only restarts
// the usage window so a user who exhausted their plan traffic can continue.
// Called inside a transaction.
func applyResetEffect(tx *gorm.DB, user *model.User, plan *model.Plan) error {
	user.UpTotal = 0
	user.DownTotal = 0
	user.LastResetAt = new(time.Now())
	return tx.Save(user).Error
}

// ListMine returns a user's own orders, newest first.
func (s *OrderService) ListMine(userID string, page, pageSize int) ([]model.Order, int64, error) {
	var orders []model.Order
	var total int64
	s.db.Model(&model.Order{}).Where("user_id = ?", userID).Count(&total)
	err := s.db.Where("user_id = ?", userID).Order("created_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&orders).Error
	return orders, total, err
}

// OrderListFilter holds optional filtering/sorting parameters for List.
type OrderListFilter struct {
	Search string // substring match on user_id or out_trade_no
	Status string // pending|paid|closed; empty = all
	SortBy string // created_at|amount|status|paid_at|user_id|kind
	Order  string // asc|desc
}

// orderSortableColumns whitelists columns for ORDER BY to avoid injecting
// arbitrary user input into SQL.
var orderSortableColumns = map[string]string{
	"created_at": "created_at",
	"amount":     "amount",
	"status":     "status",
	"paid_at":    "paid_at",
	"user_id":    "user_id",
	"kind":       "kind",
}

// List returns all orders (admin), with optional filtering/sorting applied
// server-side. With an empty filter it preserves the original behavior
// (newest first).
func (s *OrderService) List(filter OrderListFilter, page, pageSize int) ([]model.Order, int64, error) {
	q := s.db.Model(&model.Order{})
	if filter.Search != "" {
		like := "%" + filter.Search + "%"
		q = q.Where("user_id LIKE ? OR out_trade_no LIKE ?", like, like)
	}
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	order := "created_at DESC"
	if col, ok := orderSortableColumns[filter.SortBy]; ok {
		dir := "ASC"
		if strings.EqualFold(filter.Order, "desc") {
			dir = "DESC"
		}
		order = col + " " + dir
	}

	var orders []model.Order
	err := q.Order(order).
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&orders).Error
	return orders, total, err
}

// Get returns an order, enforcing that it belongs to userID (returns not-found
// for other users' orders).
func (s *OrderService) Get(id, userID string) (*model.Order, error) {
	var order model.Order
	if err := s.db.First(&order, "id = ?", id).Error; err != nil {
		return nil, err
	}
	if order.UserID != userID {
		return nil, gorm.ErrRecordNotFound
	}
	return &order, nil
}

// AdminGet returns any order by id (admin).
func (s *OrderService) AdminGet(id string) (*model.Order, error) {
	var order model.Order
	if err := s.db.First(&order, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

// CloseMine lets the owner close their own pending order. Returns
// ErrOrderNotPending if the order is already paid/closed and a not-found error
// for orders belonging to another user (via Get's ownership check).
func (s *OrderService) CloseMine(id, userID string) error {
	order, err := s.Get(id, userID)
	if err != nil {
		return err
	}
	if order.Status != model.OrderStatusPending {
		return ErrOrderNotPending
	}
	return s.db.Model(&model.Order{}).
		Where("id = ?", id).
		Update("status", model.OrderStatusClosed).Error
}

// PayMine regenerates a payment URL for the owner's pending order and returns
// the order alongside it. Returns ErrOrderNotPending for non-pending orders
// and a not-found error for orders belonging to another user.
func (s *OrderService) PayMine(id, userID string) (*model.Order, *payment.PayDirective, error) {
	order, err := s.Get(id, userID)
	if err != nil {
		return nil, nil, err
	}
	if order.Status != model.OrderStatusPending {
		return nil, nil, ErrOrderNotPending
	}
	var subject string
	switch order.Kind {
	case model.OrderKindTraffic:
		pkg, err := s.trafficSvc.Get(order.TrafficPackageID)
		if err != nil {
			return nil, nil, err
		}
		subject, err = s.resolvePaymentSubject(model.OrderKindTraffic, pkg.Name, "", order.Amount, pkg.DisplayName)
		if err != nil {
			return nil, nil, err
		}
	default:
		price, err := s.planSvc.loadEnabledPlanPrice(order.PlanID, order.PlanPriceID)
		if err != nil {
			return nil, nil, err
		}
		plan, err := s.planSvc.Get(price.PlanID)
		if err != nil {
			return nil, nil, err
		}
		subject, err = s.resolvePaymentSubject(model.OrderKindPlan, plan.Name, price.Period, order.Amount, plan.DisplayName)
		if err != nil {
			return nil, nil, err
		}
	}
	prov, err := s.payments.Get(order.Platform)
	if err != nil {
		return nil, nil, err
	}
	directive, err := prov.PayURL(order, subject)
	if err != nil {
		return nil, nil, err
	}
	return order, directive, nil
}

// AdminUpdateStatus lets an admin manually set an order's status. Only a
// pending order can be changed:
//   - to "paid":   applies the purchase effect (as a gateway notify would) and
//     stamps paid_at + a manual trade_no for audit.
//   - to "closed": cancels the order with no entitlement effect.
//
// Paid/closed orders are terminal; re-statusing them returns ErrOrderNotPending.
func (s *OrderService) AdminUpdateStatus(id, status, adminID string) (*model.Order, error) {
	now := time.Now()
	var updated model.Order
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&updated, "id = ?", id).Error; err != nil {
			return err
		}
		if updated.Status != model.OrderStatusPending {
			return ErrOrderNotPending
		}
		if status == model.OrderStatusClosed {
			if err := tx.Model(&updated).
				Where("id = ?", id).
				Update("status", model.OrderStatusClosed).Error; err != nil {
				return err
			}
			updated.Status = model.OrderStatusClosed
			return nil
		}

		// status == paid: flip pending→paid (idempotent) then grant the effect.
		res := tx.Model(&model.Order{}).
			Where("id = ? AND status = ?", id, model.OrderStatusPending).
			Updates(map[string]any{
				"status":   model.OrderStatusPaid,
				"trade_no": "manual:" + adminID,
				"platform": model.OrderPlatformManual,
				"paid_at":  &now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrOrderNotPending
		}

		var order model.Order
		if err := tx.Where("id = ?", id).First(&order).Error; err != nil {
			return err
		}
		var user model.User
		if err := tx.Where("id = ?", order.UserID).First(&user).Error; err != nil {
			return err
		}
		switch order.Kind {
		case model.OrderKindTraffic:
			var pkg model.TrafficPackage
			if err := tx.Where("id = ?", order.TrafficPackageID).First(&pkg).Error; err != nil {
				return err
			}
			if err := applyTrafficEffect(tx, &user, &pkg, order.ValidityDays); err != nil {
				return err
			}
		case model.OrderKindReset:
			var plan model.Plan
			if err := tx.Where("id = ?", order.PlanID).First(&plan).Error; err != nil {
				return err
			}
			if err := applyResetEffect(tx, &user, &plan); err != nil {
				return err
			}
		default: // plan
			var plan model.Plan
			if err := tx.Where("id = ?", order.PlanID).First(&plan).Error; err != nil {
				return err
			}
			if err := applyPlanEffect(tx, &user, &plan, order.DurationDays); err != nil {
				return err
			}
		}
		updated.Status = model.OrderStatusPaid
		updated.TradeNo = "manual:" + adminID
		updated.Platform = model.OrderPlatformManual
		updated.PaidAt = &now
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Best-effort admin alert once the manual payment is applied.
	s.alertOrderPaid(&updated)
	return &updated, nil
}

// CloseExpired flips timed-out pending orders to closed. It never applies the
// plan effect, so re-running is safe.
func (s *OrderService) CloseExpired() (int64, error) {
	res := s.db.Model(&model.Order{}).
		Where("status = ? AND expired_at IS NOT NULL AND expired_at < ?", model.OrderStatusPending, time.Now()).
		Update("status", model.OrderStatusClosed)
	return res.RowsAffected, res.Error
}
