package service

import (
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

func newTicketTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Ticket{}, &model.TicketMessage{}, &model.TicketReadState{}, &model.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Seed a user so notification paths have an owner to look up.
	if err := db.Create(&model.User{
		ID:       "u1",
		Email:    "user@example.com",
		SubToken: "sub1",
	}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return db
}

func TestTicketCreate(t *testing.T) {
	db := newTicketTestDB(t)
	svc := NewTicketService(db)

	tk, err := svc.Create("u1", "Cannot connect", "Help me", model.TicketPriorityHigh, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tk.ID == "" || tk.Status != model.TicketStatusOpen || tk.Priority != model.TicketPriorityHigh {
		t.Fatalf("unexpected ticket: %+v", tk)
	}
	// Invalid priority falls back to normal.
	tk2, err := svc.Create("u1", "Another", "Body", "bogus", "")
	if err != nil || tk2.Priority != model.TicketPriorityNormal {
		t.Fatalf("priority fallback failed: %v %+v", err, tk2)
	}
}

func TestTicketCreateValidation(t *testing.T) {
	db := newTicketTestDB(t)
	svc := NewTicketService(db)
	if _, err := svc.Create("u1", "", "body", "", ""); err == nil {
		t.Fatal("expected error on empty subject")
	}
	if _, err := svc.Create("u1", "subj", "", "", ""); err == nil {
		t.Fatal("expected error on empty content")
	}
}

func TestTicketUserReplyReopens(t *testing.T) {
	db := newTicketTestDB(t)
	svc := NewTicketService(db)
	tk, _ := svc.Create("u1", "S", "body", "", "")

	// Admin resolves it.
	if _, err := svc.SetStatus("1", tk.ID, model.TicketStatusResolved); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// User replies -> reopens to in_progress.
	upd, err := svc.AddUserMessage("u1", tk.ID, "still broken")
	if err != nil {
		t.Fatalf("user reply: %v", err)
	}
	if upd.Status != model.TicketStatusInProgress {
		t.Fatalf("expected reopen to in_progress, got %s", upd.Status)
	}
}

func TestTicketAdminReplyInProgress(t *testing.T) {
	db := newTicketTestDB(t)
	// Nil notification services must not panic.
	svc := NewTicketService(db)
	tk, _ := svc.Create("u1", "S", "body", "", "")

	upd, err := svc.AddAdminMessage("1", tk.ID, "looking into it")
	if err != nil {
		t.Fatalf("admin reply: %v", err)
	}
	if upd.Status != model.TicketStatusInProgress {
		t.Fatalf("expected in_progress, got %s", upd.Status)
	}
	// Notification path runs without panic even with nil services.
}

func TestTicketSetStatusTransitions(t *testing.T) {
	db := newTicketTestDB(t)
	svc := NewTicketService(db)
	tk, _ := svc.Create("u1", "S", "body", "", "")

	// open -> resolved
	if _, err := svc.SetStatus("1", tk.ID, model.TicketStatusResolved); err != nil {
		t.Fatalf("open->resolved: %v", err)
	}
	// resolved -> closed
	if _, err := svc.SetStatus("1", tk.ID, model.TicketStatusClosed); err != nil {
		t.Fatalf("resolved->closed: %v", err)
	}
	// closed -> in_progress (reopen) allowed
	if _, err := svc.SetStatus("1", tk.ID, model.TicketStatusInProgress); err != nil {
		t.Fatalf("closed->in_progress: %v", err)
	}
	// in_progress -> open is NOT allowed
	if err := mustBeInvalidTransition(svc, tk.ID, model.TicketStatusOpen); err == nil {
		t.Fatal("expected invalid transition error for in_progress->open")
	}
}

func mustBeInvalidTransition(svc *TicketService, id, to string) error {
	// Re-fetch current status then attempt the (disallowed) transition.
	var tk model.Ticket
	if err := svc.db.First(&tk, "id = ?", id).Error; err != nil {
		return err
	}
	_, err := svc.SetStatus("1", id, to)
	return err
}

func TestTicketCrossUserAccess(t *testing.T) {
	db := newTicketTestDB(t)
	svc := NewTicketService(db)
	tk, _ := svc.Create("u1", "S", "body", "", "")

	// Another user cannot read it (gorm.ErrRecordNotFound).
	if _, _, err := svc.GetForUser("other", tk.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected not-found for other user, got %v", err)
	}
	// And cannot reply.
	if _, err := svc.AddUserMessage("other", tk.ID, "hi"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected not-found on reply, got %v", err)
	}
}

func TestTicketUserClose(t *testing.T) {
	db := newTicketTestDB(t)
	svc := NewTicketService(db)
	tk, _ := svc.Create("u1", "S", "body", "", "")

	// Another user cannot close it.
	if _, err := svc.Close("other", tk.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected not-found for other user, got %v", err)
	}
	// Owner closes.
	closed, err := svc.Close("u1", tk.ID)
	if err != nil || closed.Status != model.TicketStatusClosed {
		t.Fatalf("close failed: %v %+v", err, closed)
	}
	// Closing again is a no-op, not an error.
	if _, err := svc.Close("u1", tk.ID); err != nil {
		t.Fatalf("repeat close should be no-op, got %v", err)
	}
	// A later user reply reopens it to in_progress.
	reopened, err := svc.AddUserMessage("u1", tk.ID, "reopen please")
	if err != nil || reopened.Status != model.TicketStatusInProgress {
		t.Fatalf("expected reopen to in_progress, got %v %+v", err, reopened)
	}
}

func TestTicketAdminListPopulatesEmail(t *testing.T) {
	db := newTicketTestDB(t)
	svc := NewTicketService(db)
	if _, err := svc.Create("u1", "S", "body", "", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	items, _, err := svc.ListForAdmin("", "", 1, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("list: %v len=%d", err, len(items))
	}
	if items[0].UserEmail != "user@example.com" {
		t.Fatalf("expected populated email, got %q", items[0].UserEmail)
	}
}

func TestTicketUnread(t *testing.T) {
	db := newTicketTestDB(t)
	svc := NewTicketService(db)
	tk, _ := svc.Create("u1", "S", "body", "", "")

	// After the user creates a ticket they are the last speaker: 0 unread for
	// them, but 1 unread for admins (a fresh ticket needs attention).
	if n, _ := svc.UnreadCountForUser("u1"); n != 0 {
		t.Fatalf("user unread after create: got %d, want 0", n)
	}
	if n, _ := svc.UnreadCountForAdmin(); n != 1 {
		t.Fatalf("admin unread after create: got %d, want 1", n)
	}

	// Admin replies: now it is unread for the user, not for the admin.
	if _, err := svc.AddAdminMessage("1", tk.ID, "looking into it"); err != nil {
		t.Fatalf("admin reply: %v", err)
	}
	if n, _ := svc.UnreadCountForUser("u1"); n != 1 {
		t.Fatalf("user unread after admin reply: got %d, want 1", n)
	}
	if n, _ := svc.UnreadCountForAdmin(); n != 0 {
		t.Fatalf("admin unread after own reply: got %d, want 0", n)
	}

	// User opens the ticket -> marks read -> dot clears.
	if _, _, err := svc.GetForUser("u1", tk.ID); err != nil {
		t.Fatalf("get for user: %v", err)
	}
	if n, _ := svc.UnreadCountForUser("u1"); n != 0 {
		t.Fatalf("user unread after opening: got %d, want 0", n)
	}
}

func TestTicketNotifyMethod(t *testing.T) {
	db := newTicketTestDB(t)
	// Seed a second user with a linked Telegram chat.
	if err := db.Create(&model.User{ID: "u2", Credential: "cred2", Email: "tg@example.com", SubToken: "sub2", TelegramID: 123}).Error; err != nil {
		t.Fatalf("seed u2: %v", err)
	}
	svc := NewTicketService(db)

	// Empty method -> "none" for a user without Telegram.
	tk1, err := svc.Create("u1", "no-tg", "body", "", "")
	if err != nil || tk1.NotifyMethod != model.TicketNotifyNone {
		t.Fatalf("expected none, got %q err=%v", tk1.NotifyMethod, err)
	}

	// Empty method -> "telegram" when the owner is linked.
	tk2, err := svc.Create("u2", "tg", "body", "", "")
	if err != nil || tk2.NotifyMethod != model.TicketNotifyTelegram {
		t.Fatalf("expected telegram, got %q err=%v", tk2.NotifyMethod, err)
	}

	// Explicit method is honored.
	tk3, err := svc.Create("u1", "email", "body", "", model.TicketNotifyEmail)
	if err != nil || tk3.NotifyMethod != model.TicketNotifyEmail {
		t.Fatalf("expected email, got %q err=%v", tk3.NotifyMethod, err)
	}

	// Unknown method is rejected.
	if _, err := svc.Create("u1", "bad", "body", "", "sms"); err == nil {
		t.Fatal("expected error on invalid notify_method")
	}
}
