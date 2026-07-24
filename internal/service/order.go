package service

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	Platform         string // optional: payment gateway; defaults to alipay
}

// ChangePlanResult is returned by ChangePlan and describes what happened:
// whether the switch is immediate, how much of the old plan was credited to
// the wallet, and the net charge. For an immediate switch an Order (and
// possibly a PayURL) is returned just like Create.
type ChangePlanResult struct {
	Order          *model.Order `json:"order,omitempty"`
	PayURL         string       `json:"pay_url"`
	PayMode        string       `json:"pay_mode"` // "redirect" | "qr" | "" when already paid
	Paid           bool         `json:"paid"`     // true when fully covered by wallet
	CreditCents    int64        `json:"credit_cents"`
	NetChargeCents int64        `json:"net_charge_cents"` // plan price (difference for upgrades)
	Immediate      bool         `json:"immediate"`
}

// OrderService handles plan/traffic purchases, payment-url generation,
// async notify reconciliation, plan changes with proration, and expired-order
// cleanup.
type OrderService struct {
	db          *gorm.DB
	sys         *SystemConfigService
	planSvc     *PlanService
	trafficSvc  *TrafficPackageService
	payments    *payment.Registry
	balanceSvc  *BalanceService
	telegramSvc *TelegramService
}

func NewOrderService(db *gorm.DB, sys *SystemConfigService, payments *payment.Registry, balanceSvc *BalanceService) *OrderService {
	return &OrderService{
		db:         db,
		sys:        sys,
		planSvc:    NewPlanService(db),
		trafficSvc: NewTrafficPackageService(db),
		payments:   payments,
		balanceSvc: balanceSvc,
	}
}

// SetTelegramService wires the Telegram bot service so a paid order can emit
// an admin alert (when the admin enabled the order_paid alert).
func (s *OrderService) SetTelegramService(svc *TelegramService) {
	s.telegramSvc = svc
}

// resolvePaymentSubject picks the product name shown on the payment gateway.
func (s *OrderService) resolvePaymentSubject(kind, productName, period string, amountCents int64, displayName string) (string, error) {
	if displayName != "" {
		return displayName, nil
	}
	if s.sys != nil {
		if tmpl, err := s.sys.Get(CfgKeyPaymentProductName); err == nil && tmpl != "" {
			return renderProductTemplate(tmpl, productName, period, amountCents), nil
		}
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
// product-name template.
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
// amount is ignored. Any spendable wallet balance is applied first.
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

	// Build the order with its gross amount before any wallet deduction.
	now := time.Now()
	order := &model.Order{
		ID:                  util.NewOrderID(),
		UserID:              userID,
		Kind:                p.Kind,
		Status:              model.OrderStatusPending,
		Platform:            platform,
		OutTradeNo:          util.RandomToken(16),
		ExpiredAt:           new(now.Add(orderTimeout)),
		ExtendFromOldExpiry: true,
	}

	var subject string

	switch p.Kind {
	case model.OrderKindPlan:
		plan, err := s.planSvc.Get(p.PlanID)
		if err != nil {
			return nil, nil, err
		}
		allowDisabled := isAdmin || (plan.AllowRenewOffShelf &&
			user.CurrentProductID == p.PlanID && user.CurrentProductKind == model.OrderKindPlan)
		price, err := s.planSvc.loadPlanPrice(p.PlanID, p.PlanPriceID, allowDisabled)
		if err != nil {
			return nil, nil, err
		}
		order.PlanID = price.PlanID
		order.PlanPriceID = price.ID
		order.Period = price.Period
		order.DurationDays = price.DurationDays
		order.Amount = price.Price
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

	// Apply the wallet: deduct whatever balance covers from the gross amount.
	gross := order.Amount
	var created *model.Order
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var u model.User
		if err := tx.First(&u, "id = ?", userID).Error; err != nil {
			return err
		}
		used := int64(0)
		if u.BalanceCents > 0 {
			used = min(u.BalanceCents, gross)
		}
		if used > 0 {
			if _, err := s.balanceSvc.Debit(tx, userID, used, model.BalanceReasonPurchase, order.ID, "wallet payment"); err != nil {
				return err
			}
		}
		if used == gross {
			order.Status = model.OrderStatusPaid
			order.Platform = model.OrderPlatformBalance
			paidAt := time.Now()
			order.PaidAt = &paidAt
		} else {
			order.Amount = gross - used
		}
		if err := tx.Create(order).Error; err != nil {
			return err
		}
		if used == gross {
			// Fully paid from wallet: grant the entitlement immediately.
			if err := applyOrderEffect(tx, order); err != nil {
				return err
			}
		}
		created = order
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	if created.Status == model.OrderStatusPaid {
		// No gateway step required.
		return created, &payment.PayDirective{}, nil
	}
	prov, err := s.payments.Get(created.Platform)
	if err != nil {
		return nil, nil, err
	}
	directive, err := prov.PayURL(created, subject)
	if err != nil {
		return nil, nil, err
	}
	return created, directive, nil
}

// ownsPlan reports whether the user's currently active product is the given
// plan.
func (s *OrderService) ownsPlan(userID, planID string) (bool, error) {
	var u model.User
	if err := s.db.First(&u, "id = ?", userID).Error; err != nil {
		return false, err
	}
	return u.CurrentProductID == planID && u.CurrentProductKind == model.OrderKindPlan, nil
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
		return applyOrderEffect(tx, &order)
	})
}

// alertOrderPaid emits the admin "order paid" alert outside the transaction.
func (s *OrderService) alertOrderPaid(order *model.Order) {
	if s.telegramSvc == nil {
		return
	}
	s.telegramSvc.NotifyAdminEvent(CfgKeyAlertOrderPaid,
		fmt.Sprintf("Order paid: user %s, amount %d (%s)", order.UserID, order.Amount, order.Kind))
}

// defaultBase returns the time a new plan period should start from for a
// normal purchase/renewal: the later of now and the existing expiry (so the
// new period stacks onto the old one).
func defaultBase(user *model.User) time.Time {
	now := time.Now()
	if user.ExpireAt != nil && user.ExpireAt.After(now) {
		return *user.ExpireAt
	}
	return now
}

// applyOrderEffect loads the purchased product for the order and applies its
// entitlement effect. It is the single source of truth shared by markPaid,
// AdminUpdateStatus, and the wallet-paid path in createFor.
func applyOrderEffect(tx *gorm.DB, order *model.Order) error {
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
		var price model.PlanPrice
		if err := tx.Where("id = ?", order.PlanPriceID).First(&price).Error; err != nil {
			return err
		}
		base := defaultBase(&user)
		if !order.ExtendFromOldExpiry {
			base = time.Now()
		}
		return applyPlanEffect(tx, &user, &plan, order.DurationDays, price.Price, base)
	}
}

// applyPlanEffect applies a plan to the user. paidCents is the gross price of
// the entitlement (used for later proration); base is the start of the new
// period (now for an upgrade that bought out the old period, otherwise the
// existing expiry). It is called inside a transaction.
//
// It uses a targeted column update (not Save) so it never clobbers columns the
// caller may have just mutated in the same transaction — notably balance_cents,
// which a wallet debit has already written.
func applyPlanEffect(tx *gorm.DB, user *model.User, plan *model.Plan, durationDays int, paidCents int64, base time.Time) error {
	now := time.Now()
	return tx.Model(user).Updates(map[string]any{
		"expire_at":                  new(base.AddDate(0, 0, durationDays)),
		"quota_bytes":                plan.QuotaBytes,
		"up_total":                   0,
		"down_total":                 0,
		"last_reset_at":              &now,
		"level":                      plan.Level,
		"speed_limit_up_bps":         plan.SpeedLimitUpBps,
		"speed_limit_down_bps":       plan.SpeedLimitDownBps,
		"current_product_id":         plan.ID,
		"current_product_kind":       model.OrderKindPlan,
		"current_plan_paid_cents":    paidCents,
		"current_plan_duration_days": durationDays,
	}).Error
}

// applyTrafficEffect adds the package's quota. When validityDays > 0 it also
// extends the user's ExpireAt so the traffic is usable for that window.
func applyTrafficEffect(tx *gorm.DB, user *model.User, pkg *model.TrafficPackage, validityDays int) error {
	user.QuotaBytes += pkg.QuotaBytes
	user.QuotaResetEnabled = false
	user.CurrentProductID = pkg.ID
	user.CurrentProductKind = model.OrderKindTraffic
	if validityDays > 0 {
		base := defaultBase(user)
		user.ExpireAt = new(base.AddDate(0, 0, validityDays))
	}
	return tx.Save(user).Error
}

// applyResetEffect replenishes the user's plan quota by zeroing the used
// traffic counters. It does NOT change quota_bytes, level, or expire_at.
func applyResetEffect(tx *gorm.DB, user *model.User, plan *model.Plan) error {
	user.UpTotal = 0
	user.DownTotal = 0
	user.LastResetAt = new(time.Now())
	return tx.Save(user).Error
}

// daily returns the per-day price (cents/day) for a paid entitlement, guarding
// against a zero duration.
func daily(paidCents int64, durationDays int) float64 {
	if durationDays <= 0 {
		return 0
	}
	return float64(paidCents) / float64(durationDays)
}

// computeRemainingValue returns the unamortized (remaining) value of the
// user's current plan entitlement in cents, using time-based proration. It is
// 0 unless the current product is an active plan with recorded paid/duration.
func computeRemainingValue(user *model.User) int64 {
	if user.CurrentProductKind != model.OrderKindPlan ||
		user.ExpireAt == nil || !user.ExpireAt.After(time.Now()) ||
		user.CurrentPlanDurationDays <= 0 || user.CurrentPlanPaidCents <= 0 {
		return 0
	}
	total := float64(user.CurrentPlanDurationDays) * 86400
	rem := user.ExpireAt.Sub(time.Now()).Seconds() / total
	if rem < 0 {
		rem = 0
	}
	if rem > 1 {
		rem = 1
	}
	return int64(math.Floor(float64(user.CurrentPlanPaidCents) * rem))
}

// ChangePlan switches the user to a different plan, handling the price
// difference (差价) per the product rules:
//   - Same-plan period change (e.g. month→year) or a fresh purchase: immediate,
//     behaves like a normal renewal (no credit, period stacks on old expiry).
//   - Cross-plan change (upgrade or downgrade): immediate. The old plan's
//     remaining value is credited back to the wallet and the new plan's full
//     price is then charged from the wallet (so a downgrade leaves the surplus
//     as wallet balance, an upgrade charges the difference); the new period
//     starts now.
func (s *OrderService) ChangePlan(userID, planID, planPriceID string) (*ChangePlanResult, error) {
	var user model.User
	if err := s.db.First(&user, "id = ?", userID).Error; err != nil {
		return nil, err
	}
	if !user.EmailVerified {
		return nil, ErrEmailNotVerified
	}
	// A user may only have one pending order; resolve it before changing plans.
	var pending int64
	s.db.Model(&model.Order{}).
		Where("user_id = ? AND status = ?", userID, model.OrderStatusPending).
		Count(&pending)
	if pending > 0 {
		return nil, ErrPendingOrderExists
	}

	plan, err := s.planSvc.Get(planID)
	if err != nil {
		return nil, err
	}
	allowDisabled := user.CurrentProductID == planID && user.CurrentProductKind == model.OrderKindPlan
	price, err := s.planSvc.loadPlanPrice(planID, planPriceID, allowDisabled)
	if err != nil {
		return nil, err
	}
	newPrice := price.Price
	newDuration := price.DurationDays

	active := user.CurrentProductKind == model.OrderKindPlan &&
		user.ExpireAt != nil && user.ExpireAt.After(time.Now())
	crossPlan := active && user.CurrentProductID != planID

	if crossPlan {
		// Cross-plan change (upgrade OR downgrade): credit the old plan's
		// remaining value back to the wallet, then charge the new plan's full
		// price from the wallet, applying the new plan immediately (period
		// starts now). A downgrade leaves the surplus as wallet balance.
		return s.applyPlanSwitch(userID, plan, price, newPrice, newDuration)
	}

	// Immediate, no credit: same-plan period change, fresh purchase, or a
	// traffic-package current product. Delegate to the normal purchase flow.
	order, directive, err := s.Create(userID, CreateOrderParams{
		Kind:        model.OrderKindPlan,
		PlanID:      planID,
		PlanPriceID: price.ID,
	})
	if err != nil {
		return nil, err
	}
	return &ChangePlanResult{
		Order:          order,
		PayURL:         directive.URL,
		PayMode:        directive.Kind,
		Paid:           order.Status == model.OrderStatusPaid,
		CreditCents:    0,
		NetChargeCents: newPrice,
		Immediate:      true,
	}, nil
}

// applyPlanSwitch handles a cross-plan change (upgrade OR downgrade): credits
// the old plan's remaining value back to the wallet, then charges the new
// plan's full price from the wallet, applying the new plan immediately (period
// starts now). Because the refund is credited first and the full new price is
// charged from the boosted wallet, the wallet delta is exactly
// (remainingValue - newPrice): a surplus (refund) on downgrade, a charge on
// upgrade. NetChargeCents returned to the client is the informational
// newPrice - remainingValue (negative on downgrade).
func (s *OrderService) applyPlanSwitch(userID string, plan *model.Plan, price *model.PlanPrice, newPrice int64, newDuration int) (*ChangePlanResult, error) {
	remainingValue := computeRemainingValue(loadUserForProration(s.db, userID))
	// Net is the economic difference of the switch (new price minus the value
	// credited back). It may be negative for a downgrade — that means the user
	// is refunded the difference into their wallet. Net is informational only
	// (shown to the user, consistent with PreviewChangePlan) and is independent
	// of any pre-existing wallet balance they also pay with.
	net := newPrice - remainingValue

	var result *ChangePlanResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 1. Credit the old plan's remaining value back to the wallet. This is
		// the user's own money being returned, not new balance.
		if remainingValue > 0 {
			if _, err := s.balanceSvc.Credit(tx, userID, remainingValue,
				model.BalanceReasonPlanChangeRefund, "", "plan change refund"); err != nil {
				return err
			}
		}

		// 2. Pay the NEW plan's full price from the (now boosted) wallet. We
		// debit newPrice — not the net difference — so the refund above is not
		// double-counted: the wallet delta is exactly remainingValue - newPrice
		// (a refund on downgrade, a charge on upgrade).
		var u model.User
		if err := tx.First(&u, "id = ?", userID).Error; err != nil {
			return err
		}
		used := int64(0)
		if u.BalanceCents > 0 {
			used = min(u.BalanceCents, newPrice)
		}
		if used > 0 {
			if _, err := s.balanceSvc.Debit(tx, userID, used,
				model.BalanceReasonPurchase, "", "plan change"); err != nil {
				return err
			}
		}

		order := &model.Order{
			ID:                  util.NewOrderID(),
			UserID:              userID,
			Kind:                model.OrderKindPlan,
			PlanID:              plan.ID,
			PlanPriceID:         price.ID,
			Period:              price.Period,
			DurationDays:        newDuration,
			Amount:              newPrice,
			Status:              model.OrderStatusPending,
			Platform:            model.OrderPlatformAlipay,
			OutTradeNo:          util.RandomToken(16),
			ExpiredAt:           new(time.Now().Add(orderTimeout)),
			ExtendFromOldExpiry: false,
		}
		fullyPaid := used == newPrice
		if fullyPaid {
			order.Status = model.OrderStatusPaid
			order.Platform = model.OrderPlatformBalance
			paidAt := time.Now()
			order.PaidAt = &paidAt
		} else {
			order.Amount = newPrice - used
		}
		if err := tx.Create(order).Error; err != nil {
			return err
		}
		if fullyPaid {
			if err := applyPlanEffect(tx, &u, plan, newDuration, newPrice, time.Now()); err != nil {
				return err
			}
		}
		result = &ChangePlanResult{
			Order:          order,
			Paid:           fullyPaid,
			CreditCents:    remainingValue,
			NetChargeCents: net,
			Immediate:      true,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if !result.Paid {
		prov, err := s.payments.Get(result.Order.Platform)
		if err != nil {
			return nil, err
		}
		subject, err := s.resolvePaymentSubject(model.OrderKindPlan, plan.Name, price.Period, result.Order.Amount, plan.DisplayName)
		if err != nil {
			return nil, err
		}
		directive, err := prov.PayURL(result.Order, subject)
		if err != nil {
			return nil, err
		}
		result.PayURL = directive.URL
		result.PayMode = directive.Kind
	}
	return result, nil
}

// loadUserForProration reads the user inside a transaction-free query for
// proration math (the caller re-reads inside the tx for mutations).
func loadUserForProration(db *gorm.DB, userID string) *model.User {
	var u model.User
	if err := db.First(&u, "id = ?", userID).Error; err != nil {
		return &model.User{}
	}
	return &u
}

// PreviewChangePlan computes — without writing anything to the database — the
// proration numbers the client needs to show the user before they confirm a
// cross-plan change: the remaining (unamortized) value of the current plan
// (credited back to the wallet) and the net charge (new price minus that
// credit). The net may be negative for a downgrade, in which case it means the
// user will be refunded the difference into their wallet. Immediate is always
// true because all plan changes now take effect at once.
func (s *OrderService) PreviewChangePlan(userID, planID, planPriceID string) (*ChangePlanResult, error) {
	var user model.User
	if err := s.db.First(&user, "id = ?", userID).Error; err != nil {
		return nil, err
	}
	if !user.EmailVerified {
		return nil, ErrEmailNotVerified
	}
	allowDisabled := user.CurrentProductID == planID && user.CurrentProductKind == model.OrderKindPlan
	price, err := s.planSvc.loadPlanPrice(planID, planPriceID, allowDisabled)
	if err != nil {
		return nil, err
	}
	remaining := computeRemainingValue(&user)
	net := price.Price - remaining
	return &ChangePlanResult{
		CreditCents:    remaining,
		NetChargeCents: net,
		Immediate:      true,
	}, nil
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

// orderSortableColumns whitelists columns for ORDER BY.
var orderSortableColumns = map[string]string{
	"created_at": "created_at",
	"amount":     "amount",
	"status":     "status",
	"paid_at":    "paid_at",
	"user_id":    "user_id",
	"kind":       "kind",
}

// List returns all orders (admin), with optional filtering/sorting.
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

// Get returns an order, enforcing that it belongs to userID.
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

// CloseMine lets the owner close their own pending order.
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

// PayMine regenerates a payment URL for the owner's pending order.
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
		plan, perr := s.planSvc.Get(order.PlanID)
		if perr != nil {
			return nil, nil, perr
		}
		owner, oerr := s.ownsPlan(order.UserID, order.PlanID)
		if oerr != nil {
			return nil, nil, oerr
		}
		allowDisabled := owner && plan.AllowRenewOffShelf
		price, perr := s.planSvc.loadPlanPrice(order.PlanID, order.PlanPriceID, allowDisabled)
		if perr != nil {
			return nil, nil, perr
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
		if err := applyOrderEffect(tx, &order); err != nil {
			return err
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
