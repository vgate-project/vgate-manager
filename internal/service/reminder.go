package service

import (
	"fmt"
	"strconv"
	"time"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// reminderEmailSender delivers a single HTML email. *EmailService satisfies it.
type reminderEmailSender interface {
	Send(to, subject, htmlBody string) error
}

// reminderTelegramSender delivers a single plain-text message to a user's
// linked Telegram chat. *TelegramService satisfies it.
type reminderTelegramSender interface {
	NotifyUser(userID, text string)
}

// ReminderService scans capped users and sends a best-effort traffic reminder
// when they cross a global usage-% threshold or have few days left before the
// monthly quota reset. Delivery uses the user's chosen channel (email or
// Telegram); the send frequency is bounded by a per-user cooldown so users
// aren't spammed. It is driven by an hourly background ticker (see router.go).
type ReminderService struct {
	db          *gorm.DB
	sysCfg      *SystemConfigService
	emailSvc    reminderEmailSender
	telegramSvc reminderTelegramSender
}

func NewReminderService(db *gorm.DB, sysCfg *SystemConfigService) *ReminderService {
	return &ReminderService{db: db, sysCfg: sysCfg}
}

// SetEmailService injects the email sender (best-effort). Nil ⇒ no email
// reminders are delivered.
func (s *ReminderService) SetEmailService(e reminderEmailSender) { s.emailSvc = e }

// SetTelegramService injects the Telegram bot sender. Nil ⇒ no Telegram
// reminders are delivered.
func (s *ReminderService) SetTelegramService(t reminderTelegramSender) { s.telegramSvc = t }

// CheckAndSend evaluates every capped user against the global reminder rules
// and delivers any due reminders. It is safe to call repeatedly (idempotent
// via the per-user cooldown). now should normally be time.Now().
func (s *ReminderService) CheckAndSend(now time.Time) {
	cfg, err := s.sysCfg.GetReminderConfig()
	if err != nil {
		log.Errorf("traffic reminder: reading config: %v", err)
		return
	}
	if !cfg.Enabled {
		return
	}
	resetDay := 1
	if v, err := s.sysCfg.Get(CfgKeyQuotaResetDay); err == nil {
		if n, e := strconv.Atoi(v); e == nil && n >= 1 && n <= 28 {
			resetDay = n
		}
	}

	var users []model.User
	if err := s.db.Where("quota_bytes > ?", 0).Find(&users).Error; err != nil {
		log.Errorf("traffic reminder: loading users: %v", err)
		return
	}

	sent := 0
	for i := range users {
		u := users[i]
		// Users who explicitly disabled reminders are skipped entirely.
		if u.ReminderChannel == "none" {
			continue
		}
		used := u.UpTotal + u.DownTotal
		var pct float64
		if u.QuotaBytes > 0 {
			pct = float64(used) / float64(u.QuotaBytes) * 100
		}
		usageTrig := cfg.PctThreshold > 0 && pct >= float64(cfg.PctThreshold)

		daysLeft := -1
		if u.QuotaResetEnabled {
			daysLeft = daysUntilReset(resetDay, now)
		}
		daysTrig := u.QuotaResetEnabled && cfg.DaysThreshold >= 0 &&
			daysLeft >= 0 && daysLeft <= cfg.DaysThreshold

		if !usageTrig && !daysTrig {
			continue
		}

		// Cooldown: skip if a reminder was sent within the cooldown window.
		if u.LastTrafficReminderAt != nil &&
			now.Sub(*u.LastTrafficReminderAt) < time.Duration(cfg.CooldownDays)*24*time.Hour {
			continue
		}

		ch := resolveReminderChannel(u, u.ReminderChannel)
		if ch == "none" {
			// No usable channel (e.g. auto with neither Telegram linked nor a
			// verified email). Leave last_traffic_reminder_at unset so a
			// reminder is sent once the user links a channel.
			continue
		}

		subject, html, text := buildReminderMessage(u, pct, daysLeft)
		delivered := false
		switch ch {
		case "email":
			if u.Email == "" || s.emailSvc == nil {
				continue
			}
			if err := s.emailSvc.Send(u.Email, subject, html); err != nil {
				log.Warnf("traffic reminder: email failed for user %s: %v", u.ID, err)
			} else {
				delivered = true
			}
		case "telegram":
			if s.telegramSvc == nil {
				continue
			}
			s.telegramSvc.NotifyUser(u.ID, text)
			delivered = true
		}

		if delivered {
			if err := s.db.Model(&model.User{}).
				Where("id = ?", u.ID).
				Update("last_traffic_reminder_at", now).Error; err != nil {
				log.Warnf("traffic reminder: stamping last_reminder for user %s: %v", u.ID, err)
			} else {
				sent++
			}
		}
	}
	if sent > 0 {
		log.Infof("traffic reminder: sent %d reminder(s)", sent)
	}
}

// daysUntilReset returns the whole number of calendar days from now until the
// next occurrence of the monthly reset day (1-28, in local time). Callers pass
// time.Now().
func daysUntilReset(resetDay int, now time.Time) int {
	now = now.Local()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var next time.Time
	if now.Day() < resetDay {
		next = time.Date(now.Year(), now.Month(), resetDay, 0, 0, 0, 0, now.Location())
	} else {
		next = time.Date(now.Year(), now.Month()+1, resetDay, 0, 0, 0, 0, now.Location())
	}
	return int(next.Sub(startOfDay).Hours() / 24)
}

// resolveReminderChannel maps a user's stored preference to a concrete channel.
// "" (auto) prefers Telegram when linked, then email when verified, otherwise
// none. Explicit "email"/"telegram" are honored as-is; "none" → none.
func resolveReminderChannel(u model.User, chosen string) string {
	switch chosen {
	case "none":
		return "none"
	case "email", "telegram":
		return chosen
	default: // "" or any other ⇒ auto
		if u.TelegramID != 0 {
			return "telegram"
		}
		if u.EmailVerified {
			return "email"
		}
		return "none"
	}
}

// buildReminderMessage renders the same reminder in email (HTML) and Telegram
// (plain text) form. daysLeft is -1 when the user is not on the monthly reset.
func buildReminderMessage(u model.User, pct float64, daysLeft int) (subject, html, text string) {
	used := formatBytes(u.UpTotal + u.DownTotal)
	quota := formatBytes(u.QuotaBytes)
	pctStr := fmt.Sprintf("%.0f%%", pct)

	subject = "[VGate] Traffic usage reminder"

	detail := fmt.Sprintf("You have used <b>%s</b> of your <b>%s</b> quota (<b>%s</b>).", used, quota, pctStr)
	reason := "your usage has reached the reminder threshold"
	if daysLeft >= 0 {
		detail += fmt.Sprintf(" Only <b>%d</b> day(s) remain until your quota resets.", daysLeft)
		reason = "your quota reset is approaching"
	}

	html = fmt.Sprintf("<p>Hello,</p><p>%s</p><p>This is a reminder that %s. "+
		"Please check the user portal for details or consider upgrading your plan.</p>", detail, reason)

	text = fmt.Sprintf("Hello,\n%s", stripTags(detail))
	if daysLeft >= 0 {
		text += fmt.Sprintf("\nOnly %d day(s) remain until your quota resets.", daysLeft)
	}
	text += "\n\nThis is a reminder that " + reason + ". Please check the user portal for details or consider upgrading your plan."

	return subject, html, text
}

// stripTags removes the small set of HTML tags we emit in the reminder detail
// so the Telegram (plain text) copy reads cleanly. It is intentionally minimal
// — buildReminderMessage only produces <b> and <p> tags.
func stripTags(s string) string {
	out := make([]rune, 0, len(s))
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			out = append(out, r)
		}
	}
	return string(out)
}
