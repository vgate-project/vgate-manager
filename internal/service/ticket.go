package service

import (
	"errors"
	"strings"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/util"
)

// TicketService manages support tickets: users open them and reply; admins
// reply and move them through a status workflow. An admin reply notifies the
// ticket owner via the (optional) email / Telegram services.
type TicketService struct {
	db          *gorm.DB
	emailSvc    *EmailService
	telegramSvc *TelegramService
}

func NewTicketService(db *gorm.DB) *TicketService {
	return &TicketService{db: db}
}

// SetEmailService wires the email service so an admin reply can notify the
// ticket owner. Nil is allowed (notifications become no-ops).
func (s *TicketService) SetEmailService(svc *EmailService) {
	s.emailSvc = svc
}

// SetTelegramService wires the Telegram bot so an admin reply can notify the
// ticket owner. Nil is allowed (notifications become no-ops).
func (s *TicketService) SetTelegramService(svc *TelegramService) {
	s.telegramSvc = svc
}

// validPriority reports whether p is a known priority, defaulting blanks/unknowns.
func normalizePriority(p string) string {
	switch p {
	case model.TicketPriorityLow, model.TicketPriorityNormal,
		model.TicketPriorityHigh, model.TicketPriorityUrgent:
		return p
	default:
		return model.TicketPriorityNormal
	}
}

func validStatus(s string) bool {
	switch s {
	case model.TicketStatusOpen, model.TicketStatusInProgress,
		model.TicketStatusResolved, model.TicketStatusClosed:
		return true
	default:
		return false
	}
}

// resolveNotifyMethod decides the owner-notification channel for a ticket.
// An empty method defaults to "telegram" when the owner has a linked Telegram
// chat, else "none". Any other value must be one of the known methods.
func (s *TicketService) resolveNotifyMethod(userID, method string) (string, error) {
	if method == "" {
		var u model.User
		if err := s.db.First(&u, "id = ?", userID).Error; err != nil {
			return "", err
		}
		if u.TelegramID != 0 {
			return model.TicketNotifyTelegram, nil
		}
		return model.TicketNotifyNone, nil
	}
	switch method {
	case model.TicketNotifyNone, model.TicketNotifyEmail, model.TicketNotifyTelegram:
		return method, nil
	default:
		return "", errors.New("invalid notify_method")
	}
}

// validateTransition enforces the ticket status machine.
func validateTransition(from, to string) error {
	if from == to {
		return nil
	}
	allowed := map[string][]string{
		model.TicketStatusOpen:       {model.TicketStatusInProgress, model.TicketStatusResolved, model.TicketStatusClosed},
		model.TicketStatusInProgress: {model.TicketStatusResolved, model.TicketStatusClosed},
		model.TicketStatusResolved:   {model.TicketStatusInProgress, model.TicketStatusClosed},
		model.TicketStatusClosed:     {model.TicketStatusInProgress},
	}
	for _, next := range allowed[from] {
		if next == to {
			return nil
		}
	}
	return errors.New("invalid status transition")
}

// Create opens a new ticket authored by the given user, with an initial
// user message. Invalid priorities fall back to normal. notifyMethod selects
// this ticket's owner-notification channel; "" resolves to "telegram" when the
// owner has a linked Telegram chat, else "none".
func (s *TicketService) Create(userID, subject, content, priority, notifyMethod string) (*model.Ticket, error) {
	if strings.TrimSpace(subject) == "" {
		return nil, errors.New("subject is required")
	}
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("content is required")
	}

	notify, err := s.resolveNotifyMethod(userID, notifyMethod)
	if err != nil {
		return nil, err
	}

	t := &model.Ticket{
		ID:           util.NewTicketID(),
		UserID:       userID,
		Subject:      strings.TrimSpace(subject),
		Priority:     normalizePriority(priority),
		Status:       model.TicketStatusOpen,
		NotifyMethod: notify,
	}
	msg := &model.TicketMessage{
		ID:       util.NewTicketMessageID(),
		TicketID: t.ID,
		Sender:   model.TicketSenderUser,
		SenderID: userID,
		Content:  strings.TrimSpace(content),
	}
	if err := s.db.Create(t).Error; err != nil {
		return nil, err
	}
	if err := s.db.Create(msg).Error; err != nil {
		return nil, err
	}
	s.notifyAdmins(t, content)
	return t, nil
}

// AddUserMessage appends a user reply to a ticket the caller owns. A reply to a
// resolved/closed ticket reopens it to in_progress.
func (s *TicketService) AddUserMessage(userID, ticketID, content string) (*model.Ticket, error) {
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("content is required")
	}
	var t model.Ticket
	if err := s.db.First(&t, "id = ? AND user_id = ?", ticketID, userID).Error; err != nil {
		return nil, err
	}
	msg := &model.TicketMessage{
		ID:       util.NewTicketMessageID(),
		TicketID: t.ID,
		Sender:   model.TicketSenderUser,
		SenderID: userID,
		Content:  strings.TrimSpace(content),
	}
	if err := s.db.Create(msg).Error; err != nil {
		return nil, err
	}
	if t.Status == model.TicketStatusResolved || t.Status == model.TicketStatusClosed {
		t.Status = model.TicketStatusInProgress
		if err := s.db.Model(&t).Update("status", t.Status).Error; err != nil {
			return nil, err
		}
	}
	s.notifyAdmins(&t, content)
	return &t, nil
}

// AddAdminMessage appends an admin reply and advances the ticket status:
// open -> in_progress, or resolved/closed -> reopened to in_progress. It then
// notifies the ticket owner (best-effort).
func (s *TicketService) AddAdminMessage(adminID, ticketID, content string) (*model.Ticket, error) {
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("content is required")
	}
	var t model.Ticket
	if err := s.db.First(&t, "id = ?", ticketID).Error; err != nil {
		return nil, err
	}
	msg := &model.TicketMessage{
		ID:       util.NewTicketMessageID(),
		TicketID: t.ID,
		Sender:   model.TicketSenderAdmin,
		SenderID: adminID,
		Content:  strings.TrimSpace(content),
	}
	if err := s.db.Create(msg).Error; err != nil {
		return nil, err
	}
	if t.Status == model.TicketStatusOpen || t.Status == model.TicketStatusResolved || t.Status == model.TicketStatusClosed {
		t.Status = model.TicketStatusInProgress
		if err := s.db.Model(&t).Update("status", t.Status).Error; err != nil {
			return nil, err
		}
	}
	s.notifyUser(&t)
	return &t, nil
}

// SetStatus moves a ticket to a new status, enforcing the transition rules.
func (s *TicketService) SetStatus(adminID, ticketID, status string) (*model.Ticket, error) {
	if !validStatus(status) {
		return nil, errors.New("invalid status")
	}
	var t model.Ticket
	if err := s.db.First(&t, "id = ?", ticketID).Error; err != nil {
		return nil, err
	}
	if err := validateTransition(t.Status, status); err != nil {
		return nil, err
	}
	t.Status = status
	if err := s.db.Model(&t).Update("status", status).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// Close lets the ticket owner mark their own ticket as closed. It enforces the
// status machine (closing is allowed from any non-closed state) and is a
// no-op when the ticket is already closed. A later user reply reopens it.
func (s *TicketService) Close(userID, ticketID string) (*model.Ticket, error) {
	var t model.Ticket
	if err := s.db.First(&t, "id = ? AND user_id = ?", ticketID, userID).Error; err != nil {
		return nil, err
	}
	if t.Status == model.TicketStatusClosed {
		return &t, nil
	}
	if err := validateTransition(t.Status, model.TicketStatusClosed); err != nil {
		return nil, err
	}
	t.Status = model.TicketStatusClosed
	if err := s.db.Model(&t).Update("status", t.Status).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// ListForUser returns the caller's tickets, newest first, optionally filtered
// by status.
func (s *TicketService) ListForUser(userID, statusFilter string, page, pageSize int) ([]model.Ticket, int64, error) {
	var items []model.Ticket
	var total int64
	q := s.db.Model(&model.Ticket{}).Where("user_id = ?", userID)
	if statusFilter != "" {
		q = q.Where("status = ?", statusFilter)
	}
	q.Count(&total)
	err := q.Order("created_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&items).Error
	return items, total, err
}

// ListForAdmin returns all tickets, newest first, optionally filtered by status
// and/or a free-text query across subject and owner email. UserEmail is
// populated for display.
func (s *TicketService) ListForAdmin(statusFilter, q string, page, pageSize int) ([]model.Ticket, int64, error) {
	var items []model.Ticket
	var total int64
	base := s.db.Model(&model.Ticket{})
	if statusFilter != "" {
		base = base.Where("status = ?", statusFilter)
	}
	if q != "" {
		like := "%" + strings.TrimSpace(q) + "%"
		base = base.Where("subject LIKE ? OR user_id IN (SELECT id FROM users WHERE email LIKE ?)", like, like)
	}
	base.Count(&total)
	if err := base.Order("created_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	s.populateUserEmails(items)
	return items, total, nil
}

// GetForUser returns a ticket and its messages, verifying ownership.
func (s *TicketService) GetForUser(userID, ticketID string) (*model.Ticket, []model.TicketMessage, error) {
	var t model.Ticket
	if err := s.db.First(&t, "id = ? AND user_id = ?", ticketID, userID).Error; err != nil {
		return nil, nil, err
	}
	msgs, err := s.messages(ticketID)
	if err != nil {
		return nil, nil, err
	}
	return &t, msgs, nil
}

// GetForAdmin returns a ticket and its messages, populating UserEmail.
func (s *TicketService) GetForAdmin(ticketID string) (*model.Ticket, []model.TicketMessage, error) {
	var t model.Ticket
	if err := s.db.First(&t, "id = ?", ticketID).Error; err != nil {
		return nil, nil, err
	}
	msgs, err := s.messages(ticketID)
	if err != nil {
		return nil, nil, err
	}
	s.populateUserEmails([]model.Ticket{t})
	return &t, msgs, nil
}

func (s *TicketService) messages(ticketID string) ([]model.TicketMessage, error) {
	var msgs []model.TicketMessage
	err := s.db.Where("ticket_id = ?", ticketID).Order("created_at ASC").Find(&msgs).Error
	return msgs, err
}

func (s *TicketService) populateUserEmails(tickets []model.Ticket) {
	if len(tickets) == 0 {
		return
	}
	ids := make([]string, 0, len(tickets))
	for _, t := range tickets {
		ids = append(ids, t.UserID)
	}
	var users []model.User
	if err := s.db.Select("id, email").Where("id IN ?", ids).Find(&users).Error; err != nil {
		return
	}
	emailByID := make(map[string]string, len(users))
	for _, u := range users {
		emailByID[u.ID] = u.Email
	}
	for i := range tickets {
		tickets[i].UserEmail = emailByID[tickets[i].UserID]
	}
}

// notifyUser delivers a best-effort notification to the ticket owner when an
// admin replies, using the channel stored on the ticket (NotifyMethod).
// Failures are logged but never returned to the caller.
func (s *TicketService) notifyUser(t *model.Ticket) {
	switch t.NotifyMethod {
	case model.TicketNotifyNone, "":
		return
	}
	var u model.User
	if err := s.db.First(&u, "id = ?", t.UserID).Error; err != nil {
		return
	}
	switch t.NotifyMethod {
	case model.TicketNotifyEmail:
		if s.emailSvc == nil || u.Email == "" {
			return
		}
		subject := "[VGate] Ticket reply: " + t.Subject
		body := "<p>You have a new reply on your ticket <b>" + t.Subject + "</b>.</p>" +
			"<p>Status is now: " + t.Status + ". Please check the user portal for details.</p>"
		if err := s.emailSvc.Send(u.Email, subject, body); err != nil {
			log.Warnf("ticket notify email failed for user %s: %v", u.ID, err)
		}
	case model.TicketNotifyTelegram:
		if s.telegramSvc == nil {
			return
		}
		s.telegramSvc.NotifyUser(u.ID, "New reply on your ticket \""+t.Subject+"\" (status: "+t.Status+").")
	}
}

// notifyAdmins delivers a best-effort Telegram notification to admins when a
// ticket is opened or a user replies. Failures are logged but never returned.
func (s *TicketService) notifyAdmins(t *model.Ticket, content string) {
	if s.telegramSvc != nil {
		s.telegramSvc.NotifyAdminsTicket(t, content)
	}
}
