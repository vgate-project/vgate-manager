package service

import (
	"errors"

	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/util"
)

// AnnouncementService manages site-wide announcements authored by admins and
// surfaced to users (e.g. in the user SPA). Admins see every announcement
// regardless of active state; users only see active ones (and pinned ones
// sort first).
type AnnouncementService struct {
	db *gorm.DB
}

func NewAnnouncementService(db *gorm.DB) *AnnouncementService {
	return &AnnouncementService{db: db}
}

// Create persists a new announcement authored by the given admin.
func (s *AnnouncementService) Create(title, content string, pinned, active bool, authorAdminID uint) (*model.Announcement, error) {
	if title == "" {
		return nil, errors.New("title is required")
	}
	a := &model.Announcement{
		ID:            util.NewAnnouncementID(),
		Title:         title,
		Content:       content,
		Pinned:        pinned,
		Active:        active,
		AuthorAdminID: authorAdminID,
	}
	if err := s.db.Create(a).Error; err != nil {
		return nil, err
	}
	return a, nil
}

// Update replaces the editable fields of an existing announcement.
func (s *AnnouncementService) Update(id, title, content string, pinned, active bool) (*model.Announcement, error) {
	var a model.Announcement
	if err := s.db.First(&a, "id = ?", id).Error; err != nil {
		return nil, err
	}
	if title == "" {
		return nil, errors.New("title is required")
	}
	a.Title = title
	a.Content = content
	a.Pinned = pinned
	a.Active = active
	if err := s.db.Save(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// Delete removes an announcement by primary key.
func (s *AnnouncementService) Delete(id string) error {
	res := s.db.Delete(&model.Announcement{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Get returns a single announcement by id (any state).
func (s *AnnouncementService) Get(id string) (*model.Announcement, error) {
	var a model.Announcement
	if err := s.db.First(&a, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// ListForAdmin returns every announcement (active or not), pinned first then
// newest-first.
func (s *AnnouncementService) ListForAdmin(page, pageSize int) ([]model.Announcement, int64, error) {
	var items []model.Announcement
	var total int64
	s.db.Model(&model.Announcement{}).Count(&total)
	err := s.db.Order("pinned DESC, created_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&items).Error
	return items, total, err
}

// ListActive returns only the active announcements for user-facing display,
// pinned first then newest-first.
func (s *AnnouncementService) ListActive() ([]model.Announcement, error) {
	var items []model.Announcement
	err := s.db.Where("active = ?", true).
		Order("pinned DESC, created_at DESC").
		Find(&items).Error
	return items, err
}
