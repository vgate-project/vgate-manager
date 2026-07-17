package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

func cfgDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestGetSubBaseURLs(t *testing.T) {
	seed := func(t *testing.T, value string) *SystemConfigService {
		db := cfgDB(t)
		if err := db.Create(&model.SystemConfig{Key: CfgKeySubBaseURLs, Value: value}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
		return NewSystemConfigService(db)
	}

	// Valid list: trailing slashes are trimmed, order preserved.
	svc := seed(t, `["https://sub1.example.com/", "https://sub2.example.com"]`)
	got, err := svc.GetSubBaseURLs()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"https://sub1.example.com", "https://sub2.example.com"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	// Invalid entries (bad scheme, blank) are filtered; valid one remains.
	svc = seed(t, `["ftp://x", "", "https://ok.example.com"]`)
	got, err = svc.GetSubBaseURLs()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "https://ok.example.com" {
		t.Fatalf("got %v, want [https://ok.example.com]", got)
	}

	// Non-JSON value is an error (broken admin edit is surfaced).
	svc = seed(t, "not-json")
	if _, err := svc.GetSubBaseURLs(); err == nil {
		t.Fatal("expected error for non-JSON value")
	}

	// Absent key ⇒ empty list, no error.
	svc = NewSystemConfigService(cfgDB(t))
	got, err = svc.GetSubBaseURLs()
	if err != nil || len(got) != 0 {
		t.Fatalf("absent key: got %v err %v, want empty", got, err)
	}
}

func TestSubscriptionService_SubscribeURL(t *testing.T) {
	ss := &SubscriptionService{}

	// Random base URL is used (single entry ⇒ deterministic).
	url, b64 := ss.SubscribeURL("tok123", []string{"https://sub.example.com"}, "http://fallback")
	if url != "https://sub.example.com/api/v1/sub/tok123" {
		t.Fatalf("unexpected url: %q", url)
	}
	if b64 != "https://sub.example.com/api/v1/sub/tok123?type=v2rayn" {
		t.Fatalf("unexpected base64 url: %q", b64)
	}

	// Trailing slash on base URL is tolerated.
	url, _ = ss.SubscribeURL("tok", []string{"https://sub.example.com/"}, "http://fallback")
	if url != "https://sub.example.com/api/v1/sub/tok" {
		t.Fatalf("trailing slash not trimmed: %q", url)
	}

	// Empty list ⇒ fallback (request origin) is used.
	url, _ = ss.SubscribeURL("tok", nil, "http://origin:8081")
	if url != "http://origin:8081/api/v1/sub/tok" {
		t.Fatalf("fallback not used: %q", url)
	}
}
