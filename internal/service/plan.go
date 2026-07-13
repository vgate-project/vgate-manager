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
// enabled prices) are returned (used by the user-facing catalog).
func (s *PlanService) List(activeOnly bool) ([]model.Plan, error) {
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
	err := q.Find(&plans).Error
	return plans, err
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

// loadEnabledPlanPrice returns the price row only if it belongs to an enabled
// plan and is itself enabled. Used when creating an order so a disabled price
// (or a price for a disabled plan) cannot be purchased.
func (s *PlanService) loadEnabledPlanPrice(planID, priceID string) (*model.PlanPrice, error) {
	plan, err := s.loadEnabledPlan(planID)
	if err != nil {
		return nil, err
	}
	var price model.PlanPrice
	if priceID != "" {
		// A specific price was requested; resolve it and ensure it belongs to
		// this plan and is enabled.
		err = s.db.Where("id = ? AND plan_id = ? AND enabled = ?", priceID, plan.ID, true).
			First(&price).Error
	} else {
		// No price requested (e.g. an admin order created with only a plan id):
		// fall back to the first enabled price for the plan so creation still
		// succeeds instead of failing on a missing price id.
		err = s.db.Where("plan_id = ? AND enabled = ?", plan.ID, true).
			Order("sort ASC, created_at ASC").
			First(&price).Error
	}
	if err != nil {
		return nil, err
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
		return nil
	})
}

func (s *PlanService) Delete(id string) error {
	return s.db.Delete(&model.Plan{}, "id = ?", id).Error
}
