package service

import (
	"errors"

	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/util"
)

// BalanceService manages a user's spendable account-balance wallet. Every
// mutation writes a BalanceTransaction ledger row carrying the running
// BalanceAfter, so the history is auditable without recomputing a sum.
//
// Credit/Debit run INSIDE a caller-supplied *gorm.DB transaction so the ledger
// row, the cached User.BalanceCents, and any related plan effect stay atomic.
type BalanceService struct {
	db *gorm.DB
}

func NewBalanceService(db *gorm.DB) *BalanceService {
	return &BalanceService{db: db}
}

// Credit adds amountCents to the user's balance and appends a credit ledger
// row. It MUST be called inside tx. Returns the new balance. amountCents must
// be positive.
func (s *BalanceService) Credit(tx *gorm.DB, userID string, amountCents int64, reason, refOrderID, remark string) (int64, error) {
	if amountCents <= 0 {
		return 0, errors.New("credit amount must be positive")
	}
	var user model.User
	if err := tx.First(&user, "id = ?", userID).Error; err != nil {
		return 0, err
	}
	newBal := user.BalanceCents + amountCents
	if err := tx.Model(&user).Update("balance_cents", newBal).Error; err != nil {
		return 0, err
	}
	row := &model.BalanceTransaction{
		ID:           util.NewBalanceTxID(),
		UserID:       userID,
		Type:         model.BalanceTypeCredit,
		AmountCents:  amountCents,
		Reason:       reason,
		RefOrderID:   refOrderID,
		BalanceAfter: newBal,
		Remark:       remark,
	}
	if err := tx.Create(row).Error; err != nil {
		return 0, err
	}
	return newBal, nil
}

// Debit subtracts amountCents from the user's balance and appends a debit
// ledger row. It MUST be called inside tx and errors if the balance is
// insufficient (the caller should clamp to the available balance before
// calling, so this only fires on a genuine race). Returns the new balance.
func (s *BalanceService) Debit(tx *gorm.DB, userID string, amountCents int64, reason, refOrderID, remark string) (int64, error) {
	if amountCents <= 0 {
		return 0, errors.New("debit amount must be positive")
	}
	var user model.User
	if err := tx.First(&user, "id = ?", userID).Error; err != nil {
		return 0, err
	}
	if user.BalanceCents < amountCents {
		return 0, errors.New("insufficient balance")
	}
	newBal := user.BalanceCents - amountCents
	if err := tx.Model(&user).Update("balance_cents", newBal).Error; err != nil {
		return 0, err
	}
	row := &model.BalanceTransaction{
		ID:           util.NewBalanceTxID(),
		UserID:       userID,
		Type:         model.BalanceTypeDebit,
		AmountCents:  amountCents,
		Reason:       reason,
		RefOrderID:   refOrderID,
		BalanceAfter: newBal,
		Remark:       remark,
	}
	if err := tx.Create(row).Error; err != nil {
		return 0, err
	}
	return newBal, nil
}

// GetBalance returns the user's current wallet balance (cents).
func (s *BalanceService) GetBalance(userID string) (int64, error) {
	var user model.User
	if err := s.db.Select("balance_cents").First(&user, "id = ?", userID).Error; err != nil {
		return 0, err
	}
	return user.BalanceCents, nil
}

// ListLedger returns a user's balance transactions, newest first.
func (s *BalanceService) ListLedger(userID string, page, pageSize int) ([]model.BalanceTransaction, int64, error) {
	var total int64
	s.db.Model(&model.BalanceTransaction{}).Where("user_id = ?", userID).Count(&total)
	var rows []model.BalanceTransaction
	err := s.db.Where("user_id = ?", userID).Order("created_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&rows).Error
	return rows, total, err
}

// AdminAdjust grants (deltaCents > 0) or deducts (deltaCents < 0) from the
// user's wallet. A deduction that would drive the balance below zero is
// rejected. It runs in its own transaction.
func (s *BalanceService) AdminAdjust(userID string, deltaCents int64, remark string) error {
	if deltaCents == 0 {
		return errors.New("adjustment amount must be non-zero")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if deltaCents > 0 {
			_, err := s.Credit(tx, userID, deltaCents, model.BalanceReasonAdminAdjust, "", remark)
			return err
		}
		_, err := s.Debit(tx, userID, -deltaCents, model.BalanceReasonAdminAdjust, "", remark)
		return err
	})
}
