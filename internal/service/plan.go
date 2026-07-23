package service

import (
	"errors"

	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/util"
)

type PlanService struct {
	db *gorm.DB
}

func NewPlanService(db *gorm.DB) *PlanService {
	return &PlanService{db: db}
}

// List returns plans. When activeOnly is true only enabled plans (and their
// enabled prices) are returned (used by the user-facing catalog). When
// activeOnly is true, the caller's currently owned disabled plan is appended
// (with its enabled prices) if that plan allows off-shelf renewal, so its owner
// can still renew it.
func (s *PlanService) List(activeOnly bool, userID string) ([]model.Plan, error) {
	var plans []model.Plan
	q := s.db.Order("created_at ASC").Preload("Prices", func(tx *gorm.DB) *gorm.DB {
		if activeOnly {
			return tx.Where("enabled = ?", true).Order("sort ASC, created_at ASC")
		}
		return tx.Order("sort ASC, created_at ASC")
	})
	if activeOnly {
		q = q.Where("enabled = ?", true)
	}
	if err := q.Find(&plans).Error; err != nil {
		return nil, err
	}
	if activeOnly && userID != "" {
		if extra, ok := s.currentPlanForUser(userID, plans); ok {
			plans = append(plans, *extra)
		}
	}
	return plans, nil
}

// currentPlanForUser returns the caller's currently active plan when it is a
// disabled (off-shelf) plan that allows off-shelf renewal, and is not already
// present in the active catalog, so the owner can still renew it. Enabled
// current plans are already in the catalog, and a non-plan current product (or
// no current product) yields nothing.
func (s *PlanService) currentPlanForUser(userID string, existing []model.Plan) (*model.Plan, bool) {
	var u model.User
	if err := s.db.First(&u, "id = ?", userID).Error; err != nil {
		return nil, false
	}
	if u.CurrentProductKind != model.OrderKindPlan || u.CurrentProductID == "" {
		return nil, false
	}
	for _, p := range existing {
		if p.ID == u.CurrentProductID {
			return nil, false
		}
	}
	plan, err := s.Get(u.CurrentProductID)
	if err != nil || plan.Enabled || !plan.AllowRenewOffShelf {
		return nil, false
	}
	enabled := make([]model.PlanPrice, 0, len(plan.Prices))
	for _, pr := range plan.Prices {
		if pr.Enabled {
			enabled = append(enabled, pr)
		}
	}
	plan.Prices = enabled
	if len(plan.Prices) == 0 {
		return nil, false
	}
	return plan, true
}

func (s *PlanService) Get(id string) (*model.Plan, error) {
	var plan model.Plan
	if err := s.db.Preload("Prices").First(&plan, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &plan, nil
}

// loadEnabledPlan returns the plan only if it exists and is enabled. Used when
// creating an order so a disabled plan cannot be purchased.
func (s *PlanService) loadEnabledPlan(id string) (*model.Plan, error) {
	plan, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if !plan.Enabled {
		return nil, errors.New("plan is not available")
	}
	return plan, nil
}

// loadPlanPrice resolves the billing price for an order. When allowDisabled is
// false the plan must be enabled — this is the legacy purchase gate that keeps
// off-shelf plans out of the catalog. When allowDisabled is true (off-shelf
// renewal), a disabled plan is accepted provided it still has at least one
// enabled price; the requested price (or, when none is given, the first enabled
// price) is returned.
func (s *PlanService) loadPlanPrice(planID, priceID string, allowDisabled bool) (*model.PlanPrice, error) {
	var plan model.Plan
	if err := s.db.Preload("Prices", func(tx *gorm.DB) *gorm.DB {
		return tx.Order("sort ASC, created_at ASC")
	}).First(&plan, "id = ?", planID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("plan not found")
		}
		return nil, err
	}
	if !plan.Enabled && !allowDisabled {
		return nil, errors.New("plan is not available")
	}
	var price model.PlanPrice
	if priceID != "" {
		// A specific price was requested; resolve it and ensure it belongs to
		// this plan and is enabled.
		err := s.db.Where("id = ? AND plan_id = ? AND enabled = ?", priceID, plan.ID, true).
			First(&price).Error
		if err != nil {
			return nil, errors.New("plan price is not available")
		}
	} else {
		// No price requested (e.g. an admin order created with only a plan id):
		// fall back to the first enabled price for the plan so creation still
		// succeeds instead of failing on a missing price id.
		err := s.db.Where("plan_id = ? AND enabled = ?", plan.ID, true).
			Order("sort ASC, created_at ASC").
			First(&price).Error
		if err != nil {
			return nil, errors.New("plan price is not available")
		}
	}
	return &price, nil
}

func (s *PlanService) Create(p *model.Plan) error {
	if p.ID == "" {
		p.ID = util.NewPlanID()
	}
	for i := range p.Prices {
		if p.Prices[i].ID == "" {
			p.Prices[i].ID = util.NewPlanPriceID()
		}
		p.Prices[i].PlanID = p.ID
	}
	return s.db.Create(p).Error
}

func (s *PlanService) Update(p *model.Plan) error {
	// Replace the price set atomically: delete existing prices for the plan,
	// then re-create from p.Prices. This keeps the catalog in sync with the
	// submitted price list without needing per-row upsert bookkeeping.
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("plan_id = ?", p.ID).Delete(&model.PlanPrice{}).Error; err != nil {
			return err
		}
		prices := p.Prices
		p.Prices = nil
		if err := tx.Save(p).Error; err != nil {
			return err
		}
		for i := range prices {
			if prices[i].ID == "" {
				prices[i].ID = util.NewPlanPriceID()
			}
			prices[i].PlanID = p.ID
		}
		if len(prices) > 0 {
			if err := tx.Create(&prices).Error; err != nil {
				return err
			}
		}
		// Sync the new speed limits to every user currently on this plan.
		// Full overwrite (including any manually-set limits) is the intended
		// behavior: editing a plan's limit redefines the cap for its users.
		if err := tx.Model(&model.User{}).
			Where("current_product_id = ? AND current_product_kind = ?", p.ID, model.OrderKindPlan).
			Updates(map[string]any{
				"speed_limit_up_bps":   p.SpeedLimitUpBps,
				"speed_limit_down_bps": p.SpeedLimitDownBps,
			}).Error; err != nil {
			return err
		}
		return nil
	})
}

func (s *PlanService) Delete(id string) error {
	return s.db.Delete(&model.Plan{}, "id = ?", id).Error
}
