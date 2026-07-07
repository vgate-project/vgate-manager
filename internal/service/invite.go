package service

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/util"
)

// InviteService manages invite codes and enforces per-user invite quotas.
// Admin-generated codes have no quota; user-generated codes must stay within
// the user's remaining quota (Σ UsedCount of their codes + requested ≤ quota).
type InviteService struct {
	db     *gorm.DB
	sysCfg *SystemConfigService
}

func NewInviteService(db *gorm.DB, sysCfg *SystemConfigService) *InviteService {
	return &InviteService{db: db, sysCfg: sysCfg}
}

// effectiveQuota returns the per-user invite cap: the user's own MaxInvites
// override when set (>0), otherwise the global default from system_config.
func (s *InviteService) effectiveQuota(user *model.User) int {
	if user.MaxInvites > 0 {
		return user.MaxInvites
	}
	return s.sysCfg.GetInviteDefaultUserQuota()
}

// UsedCount returns the total number of successful registrations already
// sponsored by a user's codes.
func (s *InviteService) UsedCount(userID string) (int, error) {
	var used int64
	if err := s.db.Model(&model.InviteCode{}).
		Where("created_by_user_id = ?", userID).
		Select("COALESCE(SUM(used_count), 0)").
		Scan(&used).Error; err != nil {
		return 0, err
	}
	return int(used), nil
}

// IssuedCapacity returns the total invite capacity a user has issued across
// their codes (Σ MaxUses). This bounds how many more codes they may generate,
// independent of whether those codes have been redeemed yet.
func (s *InviteService) IssuedCapacity(userID string) (int, error) {
	var issued int64
	if err := s.db.Model(&model.InviteCode{}).
		Where("created_by_user_id = ?", userID).
		Select("COALESCE(SUM(max_uses), 0)").
		Scan(&issued).Error; err != nil {
		return 0, err
	}
	return int(issued), nil
}

// InviteStatus reports how many invites a user has used and their effective cap.
func (s *InviteService) InviteStatus(userID string) (used, quota int, err error) {
	var user model.User
	if err = s.db.First(&user, "id = ?", userID).Error; err != nil {
		return 0, 0, err
	}
	used, err = s.UsedCount(userID)
	if err != nil {
		return 0, 0, err
	}
	return used, s.effectiveQuota(&user), nil
}

// Status returns a fuller picture for the user-facing invite UI: used is the
// number of sponsored registrations, issued is the total capacity already
// minted (Σ MaxUses), and quota is the effective cap. Remaining issuance
// capacity is quota - issued.
func (s *InviteService) Status(userID string) (used, issued, quota int, err error) {
	var user model.User
	if err = s.db.First(&user, "id = ?", userID).Error; err != nil {
		return 0, 0, 0, err
	}
	used, err = s.UsedCount(userID)
	if err != nil {
		return 0, 0, 0, err
	}
	issued, err = s.IssuedCapacity(userID)
	if err != nil {
		return 0, 0, 0, err
	}
	return used, issued, s.effectiveQuota(&user), nil
}

// CreateForUser mints a code owned by a user, enforcing their invite quota.
func (s *InviteService) CreateForUser(userID string, maxUses int, expiresAt *time.Time, note string) (*model.InviteCode, error) {
	if maxUses < 1 {
		maxUses = 1
	}
	var user model.User
	if err := s.db.First(&user, "id = ?", userID).Error; err != nil {
		return nil, err
	}
	quota := s.effectiveQuota(&user)
	if quota <= 0 {
		return nil, errors.New("user invite generation is disabled")
	}
	issued, err := s.IssuedCapacity(userID)
	if err != nil {
		return nil, err
	}
	if issued+maxUses > quota {
		return nil, fmt.Errorf("invite quota exceeded: issued %d + requested %d > limit %d", issued, maxUses, quota)
	}
	return s.create(&userID, nil, maxUses, expiresAt, note)
}

// CreateForAdmin mints an admin-generated code (no quota applies).
func (s *InviteService) CreateForAdmin(adminID uint, maxUses int, expiresAt *time.Time, note string) (*model.InviteCode, error) {
	if maxUses < 1 {
		maxUses = 1
	}
	return s.create(nil, &adminID, maxUses, expiresAt, note)
}

func (s *InviteService) create(userID *string, adminID *uint, maxUses int, expiresAt *time.Time, note string) (*model.InviteCode, error) {
	code := &model.InviteCode{
		ID:               util.NewInviteID(),
		Code:             util.NewInviteCode(),
		CreatedByUserID:  userID,
		CreatedByAdminID: adminID,
		Note:             note,
		MaxUses:          maxUses,
		ExpiresAt:        expiresAt,
	}
	if err := s.db.Create(code).Error; err != nil {
		return nil, err
	}
	return code, nil
}

// ListForAdmin returns all codes paginated (newest first).
func (s *InviteService) ListForAdmin(page, pageSize int) ([]model.InviteCode, int64, error) {
	var codes []model.InviteCode
	var total int64
	s.db.Model(&model.InviteCode{}).Count(&total)
	err := s.db.Order("created_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&codes).Error
	return codes, total, err
}

// ListForUser returns codes owned by a specific user.
func (s *InviteService) ListForUser(userID string) ([]model.InviteCode, error) {
	var codes []model.InviteCode
	err := s.db.Where("created_by_user_id = ?", userID).
		Order("created_at DESC").Find(&codes).Error
	return codes, err
}

// Get returns a code by primary key.
func (s *InviteService) Get(id string) (*model.InviteCode, error) {
	var code model.InviteCode
	if err := s.db.First(&code, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &code, nil
}

// DeleteAdmin removes any code (admin action).
func (s *InviteService) DeleteAdmin(id string) error {
	return s.db.Delete(&model.InviteCode{}, "id = ?", id).Error
}

// DeleteOwned removes a code owned by the given user (used only if unused).
func (s *InviteService) DeleteOwned(id, userID string) error {
	res := s.db.Where("id = ? AND created_by_user_id = ? AND used_count = 0", id, userID).
		Delete(&model.InviteCode{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("invite code not found or already used")
	}
	return nil
}

// ValidateAndConsume verifies a raw code is present, unused, and unexpired,
// then increments its UsedCount. Returns the consumed code or an error.
func (s *InviteService) ValidateAndConsume(rawCode string) (*model.InviteCode, error) {
	if rawCode == "" {
		return nil, errors.New("invite code required")
	}
	var code model.InviteCode
	if err := s.db.Where("code = ?", rawCode).First(&code).Error; err != nil {
		return nil, errors.New("invalid invite code")
	}
	if code.Exhausted(time.Now()) {
		return nil, errors.New("invite code is used up or expired")
	}
	before := code.UsedCount
	// Update via the table (not the instance) so GORM does not sync the field
	// back onto `code`, letting us set the returned value deterministically.
	res := s.db.Model(&model.InviteCode{}).
		Where("id = ?", code.ID).
		Update("used_count", before+1)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, errors.New("invite code changed, please retry")
	}
	code.UsedCount = before + 1
	return &code, nil
}
