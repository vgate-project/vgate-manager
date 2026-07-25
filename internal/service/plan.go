package service

import (
	"errors"
	"sort"

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
	q := s.db.Order("created_at ASC")
	if activeOnly {
		q = q.Where("enabled = ?", true)
	}
	if err := q.Find(&plans).Error; err != nil {
		return nil, err
	}
	// When activeOnly, filter Prices to enabled entries only.
	if activeOnly {
		for i := range plans {
			var enabled model.PlanPrices
			for _, pr := range plans[i].Prices {
				if pr.Enabled {
					enabled = append(enabled, pr)
				}
			}
			sort.Slice(enabled, func(a, b int) bool { return enabled[a].Sort < enabled[b].Sort })
			plans[i].Prices = enabled
		}
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
	if u.CurrentProductID == "" {
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
	enabled := make(model.PlanPrices, 0, len(plan.Prices))
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
	if err := s.db.First(&plan, "id = ?", id).Error; err != nil {
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

// loadPlanPrice resolves a single pricing entry from the plan's JSON Prices
// column. When period is empty it returns the first enabled entry (by Sort
// order). When allowDisabled is false the plan itself must be enabled — this
// is the catalog gate. When allowDisabled is true (off-shelf renewal) a
// disabled plan is accepted provided the requested period is enabled.
func (s *PlanService) loadPlanPrice(planID, period string, allowDisabled bool) (*model.PlanPriceEntry, error) {
	plan, err := s.Get(planID)
	if err != nil {
		return nil, errors.New("plan not found")
	}
	if !plan.Enabled && !allowDisabled {
		return nil, errors.New("plan is not available")
	}
	if period != "" {
		for i := range plan.Prices {
			if plan.Prices[i].Period == period && plan.Prices[i].Enabled {
				return &plan.Prices[i], nil
			}
		}
		return nil, errors.New("plan price is not available")
	}
	// No period requested: pick the first enabled price by sort order.
	var best *model.PlanPriceEntry
	var bestSort int
	for i := range plan.Prices {
		pr := &plan.Prices[i]
		if !pr.Enabled {
			continue
		}
		if best == nil || pr.Sort < bestSort {
			best = pr
			bestSort = pr.Sort
		}
	}
	if best == nil {
		return nil, errors.New("plan has no enabled prices")
	}
	return best, nil
}

// loadPlanPriceLegacy loads a PlanPrice row by its primary key from the
// historical plan_prices table. Only used as a fallback for old orders whose
// PlanPriceCents was not snapshotted.
func (s *PlanService) loadPlanPriceLegacy(priceID string) (*model.PlanPrice, error) {
	var price model.PlanPrice
	if err := s.db.First(&price, "id = ?", priceID).Error; err != nil {
		return nil, err
	}
	return &price, nil
}

func (s *PlanService) Create(p *model.Plan) error {
	if p.ID == "" {
		p.ID = util.NewPlanID()
	}
	return s.db.Create(p).Error
}

func (s *PlanService) Update(p *model.Plan) error {
	// Plan.Prices is a JSON column; a simple Save persists the new price array.
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(p).Error; err != nil {
			return err
		}
		// Sync the new speed limits to every user currently on this plan.
		if err := tx.Model(&model.User{}).
			Where("current_product_id = ?", p.ID).
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
