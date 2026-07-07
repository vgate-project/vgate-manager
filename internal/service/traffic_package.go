package service

import (
	"errors"

	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/util"
)

type TrafficPackageService struct {
	db *gorm.DB
}

func NewTrafficPackageService(db *gorm.DB) *TrafficPackageService {
	return &TrafficPackageService{db: db}
}

// List returns traffic packages. When activeOnly is true only enabled packages
// are returned (used by the user-facing catalog).
func (s *TrafficPackageService) List(activeOnly bool) ([]model.TrafficPackage, error) {
	var pkgs []model.TrafficPackage
	q := s.db.Order("created_at ASC")
	if activeOnly {
		q = q.Where("enabled = ?", true)
	}
	err := q.Find(&pkgs).Error
	return pkgs, err
}

func (s *TrafficPackageService) Get(id string) (*model.TrafficPackage, error) {
	var pkg model.TrafficPackage
	if err := s.db.First(&pkg, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &pkg, nil
}

// loadEnabled returns the package only if it exists and is enabled. Used when
// creating an order so a disabled package cannot be purchased.
func (s *TrafficPackageService) loadEnabled(id string) (*model.TrafficPackage, error) {
	pkg, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if !pkg.Enabled {
		return nil, errors.New("traffic package is not available")
	}
	return pkg, nil
}

func (s *TrafficPackageService) Create(p *model.TrafficPackage) error {
	if p.ID == "" {
		p.ID = util.NewTrafficPackageID()
	}
	return s.db.Create(p).Error
}

func (s *TrafficPackageService) Update(p *model.TrafficPackage) error {
	return s.db.Save(p).Error
}

func (s *TrafficPackageService) Delete(id string) error {
	return s.db.Delete(&model.TrafficPackage{}, "id = ?", id).Error
}
