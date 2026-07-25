package cmd

import (
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// migrations runs idempotent data migrations after AutoMigrate. It is safe to
// call on every startup.
func migrations(db *gorm.DB) {
	if db.Migrator().HasTable("plan_prices") {
		backfillPlanPricesJSON(db)
	}
	if db.Migrator().HasTable("users") {
		migratePackageUsage(db)
	}
}

// migratePackageUsage backfills the per-grant used_bytes and the user-level
// package_used_bytes for data created before package-first deduction existed.
// It assumes all pre-existing usage was charged to packages first (FIFO), which
// matches the new rule. Idempotent: users already migrated (package_used_bytes
// > 0) or with no grants are skipped.
func migratePackageUsage(db *gorm.DB) {
	var users []model.User
	// Only users that have any grant and haven't been backfilled yet.
	if err := db.
		Where("package_used_bytes = ? AND id IN (SELECT DISTINCT user_id FROM traffic_grants)", 0).
		Find(&users).Error; err != nil {
		return
	}
	for _, u := range users {
		var grants []model.TrafficGrant
		if err := db.
			Where("user_id = ? AND reclaimed = ?", u.ID, false).
			Order("granted_at ASC").
			Find(&grants).Error; err != nil {
			continue
		}
		if len(grants) == 0 {
			continue
		}
		// Total capacity across active grants.
		capacity := int64(0)
		for _, g := range grants {
			capacity += g.QuotaBytes
		}
		used := u.UpTotal + u.DownTotal
		if used > capacity {
			used = capacity
		}
		if used <= 0 {
			continue
		}
		// Distribute `used` FIFO across grants.
		remaining := used
		for i := range grants {
			if remaining <= 0 {
				break
			}
			take := grants[i].QuotaBytes
			if take > remaining {
				take = remaining
			}
			if take > 0 {
				db.Model(&model.TrafficGrant{}).
					Where("id = ?", grants[i].ID).
					Update("used_bytes", take)
				remaining -= take
			}
		}
		db.Model(&model.User{}).
			Where("id = ?", u.ID).
			Update("package_used_bytes", used-remaining)
	}
}

// backfillPlanPricesJSON copies rows from the legacy plan_prices table into
// Plan.Prices (JSON column) for plans whose Prices column has not yet been
// populated. It is idempotent: plans with existing JSON prices are skipped.
func backfillPlanPricesJSON(db *gorm.DB) {
	var plans []model.Plan
	db.Where("prices IS NULL OR prices = ? OR prices = ?", "[]", "null").Find(&plans)
	for _, plan := range plans {
		var rows []model.PlanPrice
		if err := db.Where("plan_id = ?", plan.ID).
			Order("sort ASC, created_at ASC").Find(&rows).Error; err != nil {
			continue
		}
		if len(rows) == 0 {
			continue
		}
		entries := make(model.PlanPrices, len(rows))
		for i, r := range rows {
			entries[i] = model.PlanPriceEntry{
				Period:       r.Period,
				Price:        r.Price,
				DurationDays: r.DurationDays,
				Sort:         r.Sort,
				Enabled:      r.Enabled,
			}
		}
		plan.Prices = entries
		db.Save(&plan)
	}
}
