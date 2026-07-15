package service

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/util"
)

var (
	// ErrInvalidCode is returned when no code matches the submitted string.
	ErrInvalidCode = errors.New("invalid redemption code")
	// ErrCodeExhausted is returned when the code has no remaining uses or expired.
	ErrCodeExhausted = errors.New("redemption code is used up or expired")
	// ErrAlreadyRedeemed is returned when the caller already redeemed this code.
	ErrAlreadyRedeemed = errors.New("you have already redeemed this code")
	// ErrInvalidRedeemType is returned when an unknown benefit type is submitted.
	ErrInvalidRedeemType = errors.New("unknown redemption code type")
)

// RedemptionService mints and redeems admin-issued redemption codes. Redeem
// applies the code's benefit to the caller's user record, reusing the same
// effect helpers as paid orders (applyPlanEffect / etc.).
type RedemptionService struct {
	db      *gorm.DB
	planSvc *PlanService
}

func NewRedemptionService(db *gorm.DB) *RedemptionService {
	return &RedemptionService{db: db, planSvc: NewPlanService(db)}
}

// validType reports whether t is a known redemption type.
func validRedeemType(t string) bool {
	switch t {
	case model.RedeemTypeTraffic, model.RedeemTypeDuration, model.RedeemTypePlan, model.RedeemTypeReset:
		return true
	}
	return false
}

// BatchGenerate mints Count distinct codes sharing the same benefit config.
// It validates that the type's required parameter is present before minting.
func (s *RedemptionService) BatchGenerate(adminID uint, req dto.AdminGenerateRedemptionRequest) ([]model.RedemptionCode, error) {
	if !validRedeemType(req.Type) {
		return nil, ErrInvalidRedeemType
	}
	if req.MaxUses < 1 {
		req.MaxUses = 1
	}
	if req.Count < 1 {
		req.Count = 1
	}
	// Pre-validate the type-specific parameter.
	switch req.Type {
	case model.RedeemTypeTraffic:
		if req.QuotaBytes <= 0 {
			return nil, errors.New("traffic code requires quota_bytes > 0")
		}
	case model.RedeemTypeDuration:
		if req.DurationDays <= 0 {
			return nil, errors.New("duration code requires duration_days > 0")
		}
	case model.RedeemTypePlan:
		if req.PlanID == "" {
			return nil, errors.New("plan code requires plan_id")
		}
		// Ensure the plan exists and is enabled up front.
		if _, err := s.planSvc.loadEnabledPlan(req.PlanID); err != nil {
			return nil, err
		}
	}

	codes := make([]model.RedemptionCode, 0, req.Count)
	for i := 0; i < req.Count; i++ {
		code := s.buildCode(&adminID, req)
		// Retry on a (vanishingly unlikely) unique Code collision.
		var err error
		for attempt := 0; attempt < 5; attempt++ {
			if err = s.db.Create(code).Error; err == nil {
				break
			}
			code.Code = util.NewRedemptionCode()
		}
		if err != nil {
			return nil, err
		}
		codes = append(codes, *code)
	}
	return codes, nil
}

// buildCode assembles a RedemptionCode from the generation request.
func (s *RedemptionService) buildCode(adminID *uint, req dto.AdminGenerateRedemptionRequest) *model.RedemptionCode {
	return &model.RedemptionCode{
		ID:               util.NewRedemptionID(),
		Code:             util.NewRedemptionCode(),
		Type:             req.Type,
		MaxUses:          req.MaxUses,
		ExpiresAt:        req.ExpiresAt,
		Note:             req.Note,
		QuotaBytes:       req.QuotaBytes,
		DurationDays:     req.DurationDays,
		PlanID:           req.PlanID,
		CreatedByAdminID: adminID,
	}
}

// ListForAdmin returns all codes paginated (newest first).
func (s *RedemptionService) ListForAdmin(page, pageSize int) ([]model.RedemptionCode, int64, error) {
	var codes []model.RedemptionCode
	var total int64
	s.db.Model(&model.RedemptionCode{}).Count(&total)
	err := s.db.Order("created_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&codes).Error
	return codes, total, err
}

// Get returns a code by primary key.
func (s *RedemptionService) Get(id string) (*model.RedemptionCode, error) {
	var code model.RedemptionCode
	if err := s.db.First(&code, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &code, nil
}

// DeleteAdmin removes any code (admin action).
func (s *RedemptionService) DeleteAdmin(id string) error {
	return s.db.Delete(&model.RedemptionCode{}, "id = ?", id).Error
}

// ListRecordsForAdmin returns all redemption records for a code.
func (s *RedemptionService) ListRecordsForAdmin(codeID string) ([]model.RedemptionRecord, error) {
	var recs []model.RedemptionRecord
	err := s.db.Where("code_id = ?", codeID).Order("redeemed_at DESC").Find(&recs).Error
	return recs, err
}

// ListRecordsForUser returns the caller's own redemption history (newest first).
func (s *RedemptionService) ListRecordsForUser(userID string) ([]model.RedemptionRecord, error) {
	var recs []model.RedemptionRecord
	err := s.db.Where("user_id = ?", userID).Order("redeemed_at DESC").Find(&recs).Error
	return recs, err
}

// Redeem validates the raw code and, within a transaction, applies its benefit
// to the caller's user, records the redemption, and increments UsedCount. The
// (code_id, user_id) unique index guarantees one redemption per user per code,
// so MaxUses counts distinct users and a single user cannot drain a multi-use
// code. Returns the created record or an error.
func (s *RedemptionService) Redeem(userID, rawCode string) (*model.RedemptionRecord, string, error) {
	if rawCode == "" {
		return nil, "", ErrInvalidCode
	}

	var code model.RedemptionCode
	if err := s.db.Where("code = ?", rawCode).First(&code).Error; err != nil {
		return nil, "", ErrInvalidCode
	}
	if code.Exhausted(time.Now()) {
		return nil, "", ErrCodeExhausted
	}

	var dup int64
	s.db.Model(&model.RedemptionRecord{}).
		Where("code_id = ? AND user_id = ?", code.ID, userID).
		Count(&dup)
	if dup > 0 {
		return nil, "", ErrAlreadyRedeemed
	}

	// Resolve the plan (for plan-type codes) BEFORE opening the transaction so
	// the lookup does not contend with the connection the tx holds. This keeps
	// Redeem deadlock-free even on a single-connection database.
	var plan *model.Plan
	if code.Type == model.RedeemTypePlan {
		p, err := s.planSvc.loadEnabledPlan(code.PlanID)
		if err != nil {
			return nil, "", err
		}
		plan = p
	}

	var rec *model.RedemptionRecord
	var message string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Where("id = ?", userID).First(&user).Error; err != nil {
			return err
		}

		var applyErr error
		message, applyErr = s.applyBenefit(tx, &user, &code, plan)
		if applyErr != nil {
			return applyErr
		}

		rec = &model.RedemptionRecord{
			ID:         util.NewRedemptionID(),
			CodeID:     code.ID,
			UserID:     userID,
			Type:       code.Type,
			RedeemedAt: time.Now(),
		}
		if err := tx.Create(rec).Error; err != nil {
			return err
		}

		// Race-safe increment (mirrors invite.go ValidateAndConsume).
		before := code.UsedCount
		res := tx.Model(&model.RedemptionCode{}).
			Where("id = ?", code.ID).
			Update("used_count", before+1)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errors.New("redemption code changed, please retry")
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return rec, message, nil
}

// applyBenefit mutates the user in-place according to the code's type and
// persists it via the transaction. Called from within Redeem's transaction.
// plan is the pre-resolved plan for plan-type codes (may be nil for other
// types); it is loaded by Redeem before the transaction to avoid opening a
// second connection while the tx holds one.
func (s *RedemptionService) applyBenefit(tx *gorm.DB, user *model.User, code *model.RedemptionCode, plan *model.Plan) (string, error) {
	now := time.Now()
	switch code.Type {
	case model.RedeemTypeTraffic:
		user.QuotaBytes += code.QuotaBytes
		// A granted quota add-on must not be wiped by the monthly reset.
		user.QuotaResetEnabled = false
		if err := tx.Save(user).Error; err != nil {
			return "", err
		}
		return fmt.Sprintf("granted %d bytes of traffic", code.QuotaBytes), nil

	case model.RedeemTypeDuration:
		base := now
		if user.ExpireAt != nil && user.ExpireAt.After(now) {
			base = *user.ExpireAt
		}
		user.ExpireAt = new(base.AddDate(0, 0, code.DurationDays))
		user.Enabled = true
		if err := tx.Save(user).Error; err != nil {
			return "", err
		}
		return fmt.Sprintf("extended subscription by %d days", code.DurationDays), nil

	case model.RedeemTypePlan:
		if plan == nil {
			return "", errors.New("plan not found")
		}
		duration := planDurationDays(plan)
		if err := applyPlanEffect(tx, user, plan, duration); err != nil {
			return "", err
		}
		return fmt.Sprintf("applied plan %q (%d days)", plan.Name, duration), nil

	case model.RedeemTypeReset:
		user.UpTotal = 0
		user.DownTotal = 0
		user.LastResetAt = &now
		if err := tx.Save(user).Error; err != nil {
			return "", err
		}
		return "reset traffic usage", nil
	}
	return "", ErrInvalidRedeemType
}

// planDurationDays returns the longest enabled price period for a plan, used as
// the effective free-subscription duration when a plan code is redeemed. Falls
// back to 30 days when no price durations are configured.
func planDurationDays(plan *model.Plan) int {
	best := 0
	for _, p := range plan.Prices {
		if !p.Enabled {
			continue
		}
		if p.DurationDays > best {
			best = p.DurationDays
		}
	}
	if best <= 0 {
		return 30
	}
	return best
}
