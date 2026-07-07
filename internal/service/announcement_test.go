package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

func newAnnouncementTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Announcement{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestAnnouncementCRUD(t *testing.T) {
	db := newAnnouncementTestDB(t)
	svc := NewAnnouncementService(db)

	a, err := svc.Create("Hello", "world", true, true, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if a.ID == "" || a.Pinned != true || a.Active != true {
		t.Fatalf("create returned unexpected announcement: %+v", a)
	}

	// Active listing shows it.
	active, err := svc.ListActive()
	if err != nil || len(active) != 1 {
		t.Fatalf("ListActive: %v len=%d", err, len(active))
	}

	// Update to inactive.
	upd, err := svc.Update(a.ID, "Hi", "there", false, false)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if upd.Active || upd.Pinned || upd.Title != "Hi" {
		t.Fatalf("update returned unexpected: %+v", upd)
	}

	// Now it is hidden from users but visible to admins.
	if active, _ := svc.ListActive(); len(active) != 0 {
		t.Fatalf("expected no active announcements, got %d", len(active))
	}
	all, _, err := svc.ListForAdmin(1, 20)
	if err != nil || len(all) != 1 {
		t.Fatalf("ListForAdmin: %v len=%d", err, len(all))
	}

	if err := svc.Delete(a.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.Get(a.ID); err == nil {
		t.Fatal("expected not-found after delete")
	}
}

func TestAnnouncementEmptyTitle(t *testing.T) {
	db := newAnnouncementTestDB(t)
	svc := NewAnnouncementService(db)
	if _, err := svc.Create("", "x", false, true, 1); err == nil {
		t.Fatal("expected error on empty title")
	}
}
