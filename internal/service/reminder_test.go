package service

import (
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// --- fakes ---

type fakeEmail struct {
	mu    sync.Mutex
	calls []fakeEmailCall
}

type fakeEmailCall struct {
	to, subject, body string
}

func (f *fakeEmail) Send(to, subject, htmlBody string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeEmailCall{to, subject, htmlBody})
	return nil
}

func (f *fakeEmail) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeTG struct {
	mu    sync.Mutex
	calls []fakeTGCall
}

type fakeTGCall struct {
	userID, text string
}

func (f *fakeTG) NotifyUser(userID, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeTGCall{userID, text})
}

func (f *fakeTG) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// --- helpers ---

func reminderDB(t *testing.T) (*gorm.DB, *SystemConfigService) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.SystemConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, NewSystemConfigService(db)
}

func seedReminder(t *testing.T, svc *SystemConfigService, vals map[string]string) {
	t.Helper()
	if err := svc.SetAll(vals); err != nil {
		t.Fatalf("seed reminder config: %v", err)
	}
}

func mustCreateUser(t *testing.T, db *gorm.DB, u *model.User) {
	t.Helper()
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
}

// --- daysUntilReset ---

func TestDaysUntilReset(t *testing.T) {
	// resetDay in the future this month.
	if got := daysUntilReset(28, time.Date(2026, 7, 23, 15, 0, 0, 0, time.Local)); got != 5 {
		t.Errorf("daysUntilReset(28, Jul23) = %d, want 5", got)
	}
	// resetDay already passed → next month.
	if got := daysUntilReset(15, time.Date(2026, 7, 23, 15, 0, 0, 0, time.Local)); got != 23 {
		t.Errorf("daysUntilReset(15, Jul23) = %d, want 23", got)
	}
	// resetDay = 1, late in month → next month.
	if got := daysUntilReset(1, time.Date(2026, 7, 23, 15, 0, 0, 0, time.Local)); got != 9 {
		t.Errorf("daysUntilReset(1, Jul23) = %d, want 9", got)
	}
	// resetDay tomorrow → 1 day.
	if got := daysUntilReset(24, time.Date(2026, 7, 23, 15, 0, 0, 0, time.Local)); got != 1 {
		t.Errorf("daysUntilReset(24, Jul23) = %d, want 1", got)
	}
}

// --- resolveReminderChannel ---

func TestResolveReminderChannel(t *testing.T) {
	linked := model.User{TelegramID: 123, EmailVerified: true}
	emailOnly := model.User{TelegramID: 0, EmailVerified: true}
	none := model.User{TelegramID: 0, EmailVerified: false}

	cases := []struct {
		name   string
		u      model.User
		chosen string
		want   string
	}{
		{"auto linked", linked, "", "telegram"},
		{"auto email only", emailOnly, "", "email"},
		{"auto none", none, "", "none"},
		{"explicit email", none, "email", "email"},
		{"explicit telegram", none, "telegram", "telegram"},
		{"explicit none", linked, "none", "none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveReminderChannel(c.u, c.chosen); got != c.want {
				t.Errorf("resolveReminderChannel(%q) = %q, want %q", c.chosen, got, c.want)
			}
		})
	}
}

// --- CheckAndSend ---

func TestCheckAndSendUsageTriggersTelegram(t *testing.T) {
	db, svc := reminderDB(t)
	seedReminder(t, svc, map[string]string{
		CfgKeyReminderEnabled:  "true",
		CfgKeyReminderPct:      "80",
		CfgKeyReminderDays:     "3",
		CfgKeyReminderCooldown: "1",
		CfgKeyQuotaResetDay:    "1",
	})
	// 90% used; reset is 9 days away so only the usage trigger fires.
	mustCreateUser(t, db, &model.User{
		ID: "u1", Credential: "cred-u1a", SubToken: "sub-u1a", Email: "u1@example.com", QuotaBytes: 1000, UpTotal: 900,
		QuotaResetEnabled: true, TelegramID: 99, ReminderChannel: "",
	})

	email := &fakeEmail{}
	tg := &fakeTG{}
	rs := NewReminderService(db, svc)
	rs.SetEmailService(email)
	rs.SetTelegramService(tg)
	rs.CheckAndSend(time.Date(2026, 7, 23, 15, 0, 0, 0, time.Local))

	if email.count() != 0 {
		t.Errorf("email sent = %d, want 0", email.count())
	}
	if tg.count() != 1 {
		t.Fatalf("telegram sent = %d, want 1", tg.count())
	}
	if tg.calls[0].userID != "u1" {
		t.Errorf("telegram userID = %q, want u1", tg.calls[0].userID)
	}

	// last_traffic_reminder_at must be stamped.
	var u model.User
	if err := db.First(&u, "id = ?", "u1").Error; err != nil {
		t.Fatal(err)
	}
	if u.LastTrafficReminderAt == nil {
		t.Fatal("last_traffic_reminder_at not stamped")
	}
}

func TestCheckAndSendCooldownSuppresses(t *testing.T) {
	db, svc := reminderDB(t)
	seedReminder(t, svc, map[string]string{
		CfgKeyReminderEnabled:  "true",
		CfgKeyReminderPct:      "80",
		CfgKeyReminderDays:     "3",
		CfgKeyReminderCooldown: "7",
		CfgKeyQuotaResetDay:    "1",
	})
	now := time.Date(2026, 7, 23, 15, 0, 0, 0, time.Local)
	mustCreateUser(t, db, &model.User{
		ID: "u1", Credential: "cred-u1b", SubToken: "sub-u1b", Email: "u1@example.com", QuotaBytes: 1000, UpTotal: 950,
		QuotaResetEnabled: true, TelegramID: 99, ReminderChannel: "telegram",
		LastTrafficReminderAt: &now, // just reminded → within 7-day cooldown
	})

	tg := &fakeTG{}
	rs := NewReminderService(db, svc)
	rs.SetTelegramService(tg)
	rs.CheckAndSend(now)

	if tg.count() != 0 {
		t.Errorf("telegram sent = %d during cooldown, want 0", tg.count())
	}
}

func TestCheckAndSendNoChannelSkips(t *testing.T) {
	db, svc := reminderDB(t)
	seedReminder(t, svc, map[string]string{
		CfgKeyReminderEnabled:  "true",
		CfgKeyReminderPct:      "80",
		CfgKeyReminderDays:     "3",
		CfgKeyReminderCooldown: "1",
		CfgKeyQuotaResetDay:    "1",
	})
	// 90% used but auto channel with neither Telegram linked nor email verified.
	mustCreateUser(t, db, &model.User{
		ID: "u1", Credential: "cred-u1c", SubToken: "sub-u1c", Email: "", QuotaBytes: 1000, UpTotal: 900,
		QuotaResetEnabled: true, TelegramID: 0, EmailVerified: false, ReminderChannel: "",
	})

	email := &fakeEmail{}
	tg := &fakeTG{}
	rs := NewReminderService(db, svc)
	rs.SetEmailService(email)
	rs.SetTelegramService(tg)
	rs.CheckAndSend(time.Date(2026, 7, 23, 15, 0, 0, 0, time.Local))

	if email.count() != 0 || tg.count() != 0 {
		t.Errorf("sent email=%d tg=%d, want 0/0 (no usable channel)", email.count(), tg.count())
	}
	var u model.User
	if err := db.First(&u, "id = ?", "u1").Error; err != nil {
		t.Fatal(err)
	}
	if u.LastTrafficReminderAt != nil {
		t.Error("last_traffic_reminder_at stamped despite no channel")
	}
}

func TestCheckAndSendDaysTrigger(t *testing.T) {
	db, svc := reminderDB(t)
	seedReminder(t, svc, map[string]string{
		CfgKeyReminderEnabled:  "true",
		CfgKeyReminderPct:      "80",
		CfgKeyReminderDays:     "3",
		CfgKeyReminderCooldown: "1",
		CfgKeyQuotaResetDay:    "28", // reset on the 28th
	})
	now := time.Date(2026, 7, 26, 15, 0, 0, 0, time.Local) // 2 days before reset
	// Low usage (10%) so only the days-remaining trigger fires; email channel.
	mustCreateUser(t, db, &model.User{
		ID: "u1", Credential: "cred-u1d", SubToken: "sub-u1d", Email: "u1@example.com", QuotaBytes: 1000, UpTotal: 100,
		QuotaResetEnabled: true, TelegramID: 0, EmailVerified: true, ReminderChannel: "email",
	})

	email := &fakeEmail{}
	rs := NewReminderService(db, svc)
	rs.SetEmailService(email)
	rs.CheckAndSend(now)

	if email.count() != 1 {
		t.Fatalf("email sent = %d, want 1 (days trigger)", email.count())
	}
	if email.calls[0].to != "u1@example.com" {
		t.Errorf("email to = %q, want u1@example.com", email.calls[0].to)
	}
}

func TestCheckAndSendSkipsUnlimitedAndBlocked(t *testing.T) {
	db, svc := reminderDB(t)
	seedReminder(t, svc, map[string]string{
		CfgKeyReminderEnabled:  "true",
		CfgKeyReminderPct:      "80",
		CfgKeyReminderDays:     "3",
		CfgKeyReminderCooldown: "1",
		CfgKeyQuotaResetDay:    "1",
	})
	mustCreateUser(t, db, &model.User{
		ID: "unlimited", Credential: "cred-unl", SubToken: "sub-unl", Email: "u@example.com", QuotaBytes: -1, UpTotal: 999999,
		TelegramID: 1, ReminderChannel: "telegram",
	})
	mustCreateUser(t, db, &model.User{
		ID: "blocked", Credential: "cred-blk", SubToken: "sub-blk", Email: "b@example.com", QuotaBytes: 0, UpTotal: 0,
		TelegramID: 1, ReminderChannel: "telegram",
	})

	tg := &fakeTG{}
	rs := NewReminderService(db, svc)
	rs.SetTelegramService(tg)
	rs.CheckAndSend(time.Date(2026, 7, 23, 15, 0, 0, 0, time.Local))

	if tg.count() != 0 {
		t.Errorf("telegram sent = %d, want 0 (unlimited/blocked excluded)", tg.count())
	}
}

func TestCheckAndSendDisabledSendsNothing(t *testing.T) {
	db, svc := reminderDB(t)
	seedReminder(t, svc, map[string]string{
		CfgKeyReminderEnabled:  "false",
		CfgKeyReminderPct:      "80",
		CfgKeyReminderCooldown: "1",
		CfgKeyQuotaResetDay:    "1",
	})
	mustCreateUser(t, db, &model.User{
		ID: "u1", Credential: "cred-u1b", SubToken: "sub-u1b", Email: "u1@example.com", QuotaBytes: 1000, UpTotal: 950,
		TelegramID: 1, ReminderChannel: "telegram",
	})
	tg := &fakeTG{}
	rs := NewReminderService(db, svc)
	rs.SetTelegramService(tg)
	rs.CheckAndSend(time.Date(2026, 7, 23, 15, 0, 0, 0, time.Local))
	if tg.count() != 0 {
		t.Errorf("telegram sent = %d, want 0 when disabled", tg.count())
	}
}
