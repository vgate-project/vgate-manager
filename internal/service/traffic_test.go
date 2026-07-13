package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"gorm.io/gorm"
)

func trafficTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.TrafficHourlyStat{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestHourlyForUserFirstHourIsDelta verifies the first series point equals the
// single-hour delta (snap[cutoff] - snap[cutoff-1h]) and is NOT the full
// cumulative total. This locks in the fix for the `hour >=` baseline query:
// previously the cutoff-1h snapshot was excluded, making prevUp/prevDown 0 and
// inflating the first point to the user's entire cumulative traffic.
func TestHourlyForUserFirstHourIsDelta(t *testing.T) {
	db := trafficTestDB(t)
	userID := "u1"

	hourNow := time.Now().UTC().Truncate(time.Hour)
	cutoff := hourNow.Add(-24 * time.Hour)
	baseline := cutoff.Add(-time.Hour)

	// Cumulative snapshots. Cumulative totals grow over time; the per-hour
	// delta for the [cutoff-1h, cutoff] bucket is up=500, down=1000.
	rows := []model.TrafficHourlyStat{
		{UserID: userID, Hour: baseline, UpTotal: 1000, DownTotal: 2000},
		{UserID: userID, Hour: cutoff, UpTotal: 1500, DownTotal: 3000},
		{UserID: userID, Hour: cutoff.Add(time.Hour), UpTotal: 1700, DownTotal: 3600},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	svc := NewTrafficService(db)
	series, err := svc.HourlyForUser(userID)
	if err != nil {
		t.Fatalf("HourlyForUser: %v", err)
	}

	var first *dto.HourlyStat
	for _, p := range series {
		if p.Hour.Equal(cutoff) {
			first = &p
			break
		}
	}
	if first == nil {
		t.Fatalf("expected a series point at cutoff %v", cutoff)
	}
	if first.Up != 500 || first.Down != 1000 {
		t.Errorf("first hour delta = (up=%d, down=%d), want (up=500, down=1000); got cumulative-looking values instead", first.Up, first.Down)
	}
}
