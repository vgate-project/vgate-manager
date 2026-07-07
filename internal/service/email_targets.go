package service

import (
	"errors"

	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// CollectTargetEmails resolves a recipient descriptor into concrete user email
// addresses for the admin "send email" action.
//
//	descriptor "all"    → every user's email
//	descriptor "active" → enabled users' emails
//	descriptor "ids"    → only the userIDs provided
func CollectTargetEmails(db *gorm.DB, descriptor string, userIDs []string) ([]string, error) {
	var users []model.User
	var err error
	switch descriptor {
	case "all":
		err = db.Find(&users).Error
	case "active":
		err = db.Where("enabled = ?", true).Find(&users).Error
	case "ids":
		if len(userIDs) == 0 {
			return nil, errors.New("no recipients specified")
		}
		err = db.Where("id IN ?", userIDs).Find(&users).Error
	default:
		return nil, errors.New("invalid recipients descriptor (expected all|active|ids)")
	}
	if err != nil {
		return nil, err
	}
	emails := make([]string, 0, len(users))
	for _, u := range users {
		if u.Email != "" {
			emails = append(emails, u.Email)
		}
	}
	return emails, nil
}
