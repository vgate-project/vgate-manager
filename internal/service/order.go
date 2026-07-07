package service

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/smartwalle/alipay/v3"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
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
)

// CreateOrderParams describes what a user wants to buy.
type CreateOrderParams struct {
	Kind             string // model.OrderKindPlan | model.OrderKindTraffic
	PlanID           string // required when Kind=plan
	PlanPriceID      string // required when Kind=plan
	TrafficPackageID string // required when Kind=traffic
	Channel          string // optional: "pc" | "wap" | "" (auto by UA)
}

// OrderService handles plan/traffic purchases, alipay payment-url generation,
// async notify reconciliation, and expired-order cleanup.
type OrderService struct {
	db         *gorm.DB
	sys        *SystemConfigService
	planSvc    *PlanService
	trafficSvc *TrafficPackageService

	// alipay client cache, guarded by mu, keyed by a signature of the config so
	// it is rebuilt automatically when admin edits the alipay credentials.
	mu    sync.RWMutex
	cache *alipay.Client
	ckey  string
}

func NewOrderService(db *gorm.DB, sys *SystemConfigService) *OrderService {
	return &OrderService{
		db:         db,
		sys:        sys,
		planSvc:    NewPlanService(db),
		trafficSvc: NewTrafficPackageService(db),
	}
}

// buildAlipayClient returns an alipay client built from SystemConfig. The
// client is cached and rebuilt only when the config signature changes.
func (s *OrderService) buildAlipayClient() (*alipay.Client, AlipayConfig, error) {
	cfg, err := s.sys.GetAlipayConfig()
	if err != nil {
		return nil, cfg, err
	}
	if cfg.AppID == "" || cfg.PrivateKey == "" || cfg.PublicKey == "" || cfg.NotifyURL == "" {
		return nil, cfg, errors.New("alipay is not configured")
	}
	key := cfg.AppID + "|" + cfg.PrivateKey + "|" + cfg.PublicKey + "|" + strconv.FormatBool(cfg.Sandbox)

	s.mu.RLock()
	if s.cache != nil && s.ckey == key {
		c := s.cache
		s.mu.RUnlock()
		return c, cfg, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil && s.ckey == key {
		return s.cache, cfg, nil
	}
	client, err := alipay.New(cfg.AppID, cfg.PrivateKey, !cfg.Sandbox)
	if err != nil {
		return nil, cfg, err
	}
	if err := client.LoadAliPayPublicKey(cfg.PublicKey); err != nil {
		return nil, cfg, err
	}
	s.cache = client
	s.ckey = key
	return client, cfg, nil
}

// Create builds an order for the given user and returns a payment redirect
// URL. The amount is taken from the authoritative source (plan price or traffic
// package); any client-supplied amount is ignored.
func (s *OrderService) Create(userID string, p CreateOrderParams) (*model.Order, string, error) {
	return s.createFor(userID, p)
}

// AdminCreate is like Create but lets an admin place an order on behalf of any
// user. adminID is kept for audit only.
func (s *OrderService) AdminCreate(adminID, targetUserID string, p CreateOrderParams) (*model.Order, string, error) {
	return s.createFor(targetUserID, p)
}

func (s *OrderService) createFor(userID string, p CreateOrderParams) (*model.Order, string, error) {
	// Disallow a second open order: a user may only have one pending order.
	var pending int64
	s.db.Model(&model.Order{}).
		Where("user_id = ? AND status = ?", userID, model.OrderStatusPending).
		Count(&pending)
	if pending > 0 {
		return nil, "", ErrPendingOrderExists
	}

	if p.Channel != ChannelWap {
		p.Channel = ChannelPC
	}

	order := &model.Order{
		ID:         util.NewOrderID(),
		UserID:     userID,
		Kind:       p.Kind,
		Status:     model.OrderStatusPending,
		OutTradeNo: util.RandomToken(16),
		Channel:    p.Channel,
	}
	now := time.Now()
	expireAt := now.Add(orderTimeout)
	order.ExpiredAt = &expireAt

	var subject string

	switch p.Kind {
	case model.OrderKindPlan:
		price, err := s.planSvc.loadEnabledPlanPrice(p.PlanID, p.PlanPriceID)
		if err != nil {
			return nil, "", err
		}
		order.PlanID = price.PlanID
		order.PlanPriceID = price.ID
		order.Period = price.Period
		order.DurationDays = price.DurationDays
		order.Amount = price.Price
		subject = price.Period + " plan"
	case model.OrderKindTraffic:
		pkg, err := s.trafficSvc.loadEnabled(p.TrafficPackageID)
		if err != nil {
			return nil, "", err
		}
		order.TrafficPackageID = pkg.ID
		order.ValidityDays = pkg.ValidityDays
		order.Amount = pkg.Price
		subject = pkg.Name
	case model.OrderKindReset:
		plan, err := s.planSvc.loadEnabledPlan(p.PlanID)
		if err != nil {
			return nil, "", err
		}
		if !plan.ResetEnabled {
			return nil, "", errors.New("plan has no traffic reset package")
		}
		// Ownership: only the user whose active product is this plan may reset.
		var u model.User
		if err := s.db.First(&u, "id = ?", userID).Error; err != nil {
			return nil, "", err
		}
		if u.CurrentProductID != plan.ID {
			return nil, "", errors.New("traffic reset is only available for your active plan")
		}
		order.PlanID = plan.ID
		order.Amount = plan.ResetPrice
		subject = plan.Name + " traffic reset"
	default:
		return nil, "", ErrInvalidOrderKind
	}

	if err := s.db.Create(order).Error; err != nil {
		return nil, "", err
	}

	client, cfg, err := s.buildAlipayClient()
	if err != nil {
		return nil, "", err
	}
	payURL, err := s.payURL(client, cfg, order, subject)
	if err != nil {
		return nil, "", err
	}
	return order, payURL, nil
}

func (s *OrderService) payURL(client *alipay.Client, cfg AlipayConfig, order *model.Order, subject string) (string, error) {
	if order.Channel == ChannelWap {
		biz := alipay.TradeWapPay{
			Trade: alipay.Trade{
				Subject:        subject,
				OutTradeNo:     order.OutTradeNo,
				TotalAmount:    yuan(order.Amount),
				ProductCode:    "QUICK_WAP_WAY",
				NotifyURL:      cfg.NotifyURL,
				ReturnURL:      cfg.ReturnURL,
				TimeoutExpress: "30m",
			},
		}
		u, err := client.TradeWapPay(biz)
		if err != nil {
			return "", err
		}
		return u.String(), nil
	}
	biz := alipay.TradePagePay{
		Trade: alipay.Trade{
			Subject:        subject,
			OutTradeNo:     order.OutTradeNo,
			TotalAmount:    yuan(order.Amount),
			ProductCode:    "FAST_INSTANT_TRADE_PAY",
			NotifyURL:      cfg.NotifyURL,
			ReturnURL:      cfg.ReturnURL,
			TimeoutExpress: "30m",
		},
	}
	u, err := client.TradePagePay(biz)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// HandleNotify verifies an alipay async notification and, idempotently, marks
// the order paid and applies the purchase effect (plan: extends ExpireAt, sets
// quota to the plan amount, sets level; traffic: adds quota, optionally
// extends ExpireAt).
// Returning a non-nil error tells the caller to respond "failure" so alipay
// retries.
func (s *OrderService) HandleNotify(ctx context.Context, params url.Values) error {
	client, _, err := s.buildAlipayClient()
	if err != nil {
		return err
	}
	if err := client.VerifySign(ctx, params); err != nil {
		return err
	}

	outTradeNo := params.Get("out_trade_no")
	tradeNo := params.Get("trade_no")
	tradeStatus := params.Get("trade_status")
	// Only successful payments grant benefits; ignore transient states
	// (TRADE_CLOSED etc.) so they don't flip the order to paid.
	if tradeStatus != "TRADE_SUCCESS" && tradeStatus != "TRADE_FINISHED" {
		return nil
	}

	now := time.Now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Idempotent guard: only the writer that flips pending→paid continues.
		// SQLite-safe (no SELECT ... FOR UPDATE); concurrent notifies that
		// lose the race see RowsAffected==0 and bail out without re-applying.
		res := tx.Model(&model.Order{}).
			Where("out_trade_no = ? AND status = ?", outTradeNo, model.OrderStatusPending).
			Updates(map[string]any{
				"status":          model.OrderStatusPaid,
				"alipay_trade_no": tradeNo,
				"paid_at":         &now,
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

		if order.Kind == model.OrderKindTraffic {
			var pkg model.TrafficPackage
			if err := tx.Where("id = ?", order.TrafficPackageID).First(&pkg).Error; err != nil {
				return err
			}
			return applyTrafficEffect(tx, &user, &pkg, order.ValidityDays)
		}

		if order.Kind == model.OrderKindReset {
			var plan model.Plan
			if err := tx.Where("id = ?", order.PlanID).First(&plan).Error; err != nil {
				return err
			}
			return applyResetEffect(tx, &user, &plan)
		}

		var plan model.Plan
		if err := tx.Where("id = ?", order.PlanID).First(&plan).Error; err != nil {
			return err
		}
		return applyPlanEffect(tx, &user, &plan, order.DurationDays)
	})
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
	newExpire := base.AddDate(0, 0, durationDays)
	user.ExpireAt = &newExpire
	user.QuotaBytes = plan.QuotaBytes
	user.UpTotal = 0
	user.DownTotal = 0
	user.LastResetAt = &now
	user.Level = plan.Level
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
		newExpire := base.AddDate(0, 0, validityDays)
		user.ExpireAt = &newExpire
	}
	return tx.Save(user).Error
}

// applyResetEffect replenishes the user's plan quota by zeroing the used
// traffic counters (up_total/down_total) and stamping last_reset_at. It does
// NOT change quota_bytes, level, or expire_at — a reset package only restarts
// the usage window so a user who exhausted their plan traffic can continue.
// Called inside a transaction.
func applyResetEffect(tx *gorm.DB, user *model.User, plan *model.Plan) error {
	now := time.Now()
	user.UpTotal = 0
	user.DownTotal = 0
	user.LastResetAt = &now
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

// List returns all orders (admin), newest first.
func (s *OrderService) List(page, pageSize int) ([]model.Order, int64, error) {
	var orders []model.Order
	var total int64
	s.db.Model(&model.Order{}).Count(&total)
	err := s.db.Order("created_at DESC").
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
func (s *OrderService) PayMine(id, userID string) (*model.Order, string, error) {
	order, err := s.Get(id, userID)
	if err != nil {
		return nil, "", err
	}
	if order.Status != model.OrderStatusPending {
		return nil, "", ErrOrderNotPending
	}
	var subject string
	switch order.Kind {
	case model.OrderKindTraffic:
		pkg, err := s.trafficSvc.Get(order.TrafficPackageID)
		if err != nil {
			return nil, "", err
		}
		subject = pkg.Name
	default:
		price, err := s.planSvc.loadEnabledPlanPrice(order.PlanID, order.PlanPriceID)
		if err != nil {
			return nil, "", err
		}
		subject = price.Period + " plan"
	}
	client, cfg, err := s.buildAlipayClient()
	if err != nil {
		return nil, "", err
	}
	payURL, err := s.payURL(client, cfg, order, subject)
	if err != nil {
		return nil, "", err
	}
	return order, payURL, nil
}

// CloseExpired flips timed-out pending orders to closed. It never applies the
// plan effect, so re-running is safe.
func (s *OrderService) CloseExpired() (int64, error) {
	res := s.db.Model(&model.Order{}).
		Where("status = ? AND expired_at IS NOT NULL AND expired_at < ?", model.OrderStatusPending, time.Now()).
		Update("status", model.OrderStatusClosed)
	return res.RowsAffected, res.Error
}

// yuan formats a cents amount as a yuan string with 2 decimals, as required by
// alipay's total_amount field.
func yuan(cents int64) string {
	return strconv.FormatFloat(float64(cents)/100.0, 'f', 2, 64)
}
